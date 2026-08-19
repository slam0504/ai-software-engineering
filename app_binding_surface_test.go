package main

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"
)

// ---- Wails 綁定面的結構守門（reviewer 2026-08-19 三輪 P1）----
//
// 這條守門被打穿三次，每次都是「判準比它宣稱的弱」：
//
//	第一版：手寫 13 個 binding 的清單。漏了 RegisterMutation（實測 lease 釋放後
//	        仍把 evidence.jsonl 從 0 寫到 209 bytes）等 9 個，而且清單無法在新增
//	        binding 時失敗。
//	第二版：改成「呼叫圖可達 state 欄位就必須呼叫 beginStateTxn」。只確認**出現
//	        呼叫**，於是參數驗證前置、少一行 defer、function value 間接、直接寫
//	        filepath.Join(a.stateDir, "gate.jsonl") 通通照樣通過。
//	第三版：改成薄包裝＋二分白名單。形狀只驗到「三個敘述」，條件寫反、參數不
//	        轉送、實作換一個都能過；而「stateless」白名單 30 筆裡只有約 8 筆真的
//	        唯讀，其餘含 session／layout／auth／workspace 寫入等會改動狀態的操作，
//	        把「自己走 beginAppTxn」寫成 stateless 的理由，分類本身不成立。
//
// 第四版守兩件事，各自有可獨立打紅的斷言：
//
//	(1) 分類：每個 exported method 恰好落在一類——state 薄包裝，或 appTxn／
//	    readOnly／runtimeMutating 三個具名清單之一。預設是「必須是薄包裝」，
//	    要歸到其他類必須有人具名決定並寫下理由。
//	(2) 契約：薄包裝的 AST 形狀逐項驗到底（條件、原樣回傳 err、原樣轉送參數、
//	    實作一一對應且簽章相同）；其餘三類用呼叫圖驗各自宣稱的性質。
//
// 四類而不是三類，是因為有一組 binding 既不走 app 交易也不是唯讀（寫 workspace
// 檔案、送 turn、終止子行程）——硬塞進唯讀會讓那一類的斷言必須放寬到沒有意義。

// appTxnBindings：走 shutdown 交易閘（beginAppTxn）的 session／provider 操作。
// 契約：可達 beginAppTxn，且碰不到 gate／escalation／evidence 狀態。
var appTxnBindings = map[string]string{
	"AuthStatus":                 "登入狀態查詢會建 server，走 server 交易",
	"CreateSession":              "session 建立交易（crToken ＋ beginAppTxn）",
	"EndSession":                 "session 收尾交易",
	"Logout":                     "登出會操作 server",
	"NewSession":                 "session 重啟交易",
	"RecoverCodexRecording":      "錄流 generation 修復，經 server 交易",
	"RemoveSession":              "session 移除交易",
	"RestartCodexServerRecorded": "受控 server 替換",
	"SetPaneLayout":              "pane pins 落盤，走 app 交易",
	"SpecAssist":                 "一次性草擬的 lifecycle 交易",
	"StartLogin":                 "登入會建 server",
	"StartSession":               "provider 啟動交易",
}

// readOnlyBindings：只讀。契約：碰不到 state 欄位、不進任何交易閘、不可達 os 的
// 寫入函式。
var readOnlyBindings = map[string]string{
	"CLIInfo":           "回報 CLI／node 解析結果",
	"ListSessions":      "讀 session registry 快照",
	"ListWorkspace":     "讀 workspace 檔案樹",
	"LoadTurnsBefore":   "讀 replay index",
	"PaneLayout":        "讀 pane pins",
	"PlanList":          "讀 plan 樹",
	"PlanRead":          "讀 plan 檔",
	"ReadDiagram":       "讀 workspace 圖檔",
	"ReadWorkspaceFile": "讀 workspace 檔案",
	"SpecList":          "讀 spec 樹",
	"SpecRead":          "讀 spec 檔",
}

// runtimeMutatingBindings：會改動狀態，但改的**不是**受 lease 保護的 app state
// journal，也不需要 app 交易。契約：碰不到 gate／escalation／evidence 狀態，也
// 不得繞過薄包裝直接呼叫 state 實作。
//
// 每一筆都要說清楚它改的是什麼、為什麼那件事不需要 state 交易。
var runtimeMutatingBindings = map[string]string{
	"CancelLogin":      "取消進行中的登入流程（記憶體狀態，無落盤）",
	"PlanWrite":        "寫 plan/ 下的 workspace 檔案——受 git 管理的來源，不是 app state",
	"ResolveApproval":  "approval broker 的決議（socket 上的回覆，非 journal）",
	"RestoreViews":     "restore store 的 view 還原（session 檢視狀態）",
	"SendMessage":      "把使用者輸入送進 provider 的 turn",
	"SpecWrite":        "寫 spec/ 下的 workspace 檔案——同 PlanWrite",
	"TerminateSession": "終止 provider 子行程（runtime，不落 journal）",
}

// bindingClass：名稱 → 類別（空字串＝不在任何清單，必須是 state 薄包裝）。
func bindingClass(name string) string {
	switch {
	case appTxnBindings[name] != "":
		return "appTxn"
	case readOnlyBindings[name] != "":
		return "readOnly"
	case runtimeMutatingBindings[name] != "":
		return "runtimeMutating"
	}
	return ""
}

// TestEveryBindingIsClassifiedAndHonoursItsShape
//
// 正題斷言：每個 *App exported method 恰好落在一類，且 state 薄包裝符合完整形狀。
//
// 形狀**逐一驗到底**（reviewer 2026-08-19）：先前只確認「body 裡有 beginStateTxn
// 且恰好三個敘述」，於是下面這種寫法照樣通過——
//
//	if err := a.beginStateTxn(); err == nil { return err }   // 條件反了
//	defer a.endAppTxn()
//	return a.escalationAck("different-id")                   // 參數沒轉送
func TestEveryBindingIsClassifiedAndHonoursItsShape(t *testing.T) {
	_, files, info, pkg := typeCheckProductionPackage(t)
	appType := namedType(t, pkg, "App")
	methods := appMethodsByName(files, info, appType)

	var offenders, gated []string
	for _, name := range sortedMethodNames(methods) {
		fd := methods[name]
		if !fd.Name.IsExported() {
			continue
		}
		class := bindingClass(name)
		why := thinWrapperViolation(fd, methods)
		switch {
		case why == "" && class != "":
			offenders = append(offenders, name+"：是 state 薄包裝卻又被列在 "+class+" 清單裡（分類必須互斥）")
		case why == "":
			gated = append(gated, name)
		case class == "":
			offenders = append(offenders, name+"：未分類，且不是合格的 state 薄包裝——"+why)
		}
	}
	if len(offenders) != 0 {
		t.Fatalf("綁定分類／形狀不合契約：\n  %s", strings.Join(offenders, "\n  "))
	}
	if len(gated) == 0 {
		t.Fatal("沒有任何 state 薄包裝：守門失效")
	}
	// 清單不得列出不存在的 method（改名後的殘骸會讓守門靜默變寬）。
	for _, m := range []map[string]string{appTxnBindings, readOnlyBindings, runtimeMutatingBindings} {
		for name := range m {
			if fd := methods[name]; fd == nil || !fd.Name.IsExported() {
				t.Errorf("分類清單列了不存在的 exported method %q：請同步清理", name)
			}
		}
	}
	t.Logf("state 薄包裝 %d 個；appTxn %d、readOnly %d、runtimeMutating %d（合計 %d）",
		len(gated), len(appTxnBindings), len(readOnlyBindings), len(runtimeMutatingBindings),
		len(gated)+len(appTxnBindings)+len(readOnlyBindings)+len(runtimeMutatingBindings))
}

// appMethodsByName：*App 的所有方法（含 unexported）。
func appMethodsByName(files []*ast.File, info *types.Info, appType *types.Named) map[string]*ast.FuncDecl {
	out := map[string]*ast.FuncDecl{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fd.Body == nil || fd.Recv == nil {
				continue
			}
			if fn, _ := info.Defs[fd.Name].(*types.Func); fn != nil && isAppMethod(fn, appType) {
				out[fd.Name.Name] = fd
			}
		}
	}
	return out
}

func sortedMethodNames(m map[string]*ast.FuncDecl) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isAppMethod：fn 是不是 *App／App 的方法。
func isAppMethod(fn *types.Func, appType *types.Named) bool {
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil || sig.Recv() == nil {
		return false
	}
	recv := types.Unalias(sig.Recv().Type())
	if p, isPtr := recv.(*types.Pointer); isPtr {
		recv = types.Unalias(p.Elem())
	}
	named, isNamed := recv.(*types.Named)
	return isNamed && named.Obj() == appType.Obj()
}

// thinWrapperViolation：驗證薄包裝的完整契約，回傳違規說明（空字串＝合格）。
//
//  1. body 恰好三個敘述（多一個就是「在交易之外執行」）
//  2. if err := a.beginStateTxn(); err != nil { return <零值…>, err }
//     —— 條件必須是 err != nil，且必須**原樣**回傳那個 err
//  3. defer a.endAppTxn()
//  4. return a.<unexported>(<原樣轉送的參數>)
//     —— 實作必須是同名的 unexported method，簽章相同
func thinWrapperViolation(fd *ast.FuncDecl, methods map[string]*ast.FuncDecl) string {
	body := fd.Body.List
	if len(body) != 3 {
		return "body 有 " + itoa(len(body)) + " 個敘述，薄包裝必須恰好三個"
	}
	ifs, isIf := body[0].(*ast.IfStmt)
	if !isIf || !callsAppMethod(initCall(ifs.Init), "beginStateTxn") {
		return "第一個敘述必須是 `if err := a.beginStateTxn(); err != nil { … }`"
	}
	errName := assignedErrName(ifs.Init)
	if errName == "" {
		return "beginStateTxn 的結果必須指派給一個變數再判斷"
	}
	if !isNotNilCheck(ifs.Cond, errName) {
		return "條件必須是 `" + errName + " != nil`（寫成 == nil 會在成功時直接返回、失敗時反而繼續執行）"
	}
	if ifs.Body == nil || len(ifs.Body.List) != 1 {
		return "beginStateTxn 失敗時必須直接返回，不得做其他事"
	}
	ret, isReturn := ifs.Body.List[0].(*ast.ReturnStmt)
	if !isReturn {
		return "beginStateTxn 失敗時必須 return"
	}
	if len(ret.Results) == 0 {
		return "拒絕時必須把錯誤回給呼叫端"
	}
	last, isIdent := ret.Results[len(ret.Results)-1].(*ast.Ident)
	if !isIdent || last.Name != errName {
		return "拒絕時必須原樣回傳 " + errName + "（換成別的錯誤會蓋掉 lifecycle 的拒絕原因）"
	}
	def, isDefer := body[1].(*ast.DeferStmt)
	if !isDefer || !callsAppMethod(def.Call, "endAppTxn") {
		return "第二個敘述必須是 `defer a.endAppTxn()`（少了它 shutdown 會永遠等不到 inflight 歸零）"
	}
	tail, isTailReturn := body[2].(*ast.ReturnStmt)
	if !isTailReturn || len(tail.Results) != 1 {
		return "第三個敘述必須是 `return a.<unexported>(…)`"
	}
	call, isCall := tail.Results[0].(*ast.CallExpr)
	if !isCall {
		return "第三個敘述必須直接回傳實作的呼叫結果"
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "實作必須是 App 上的方法"
	}
	if recv, isRecv := sel.X.(*ast.Ident); !isRecv || recv.Name != "a" {
		return "實作必須以 receiver 呼叫（a.<unexported>）"
	}
	implName := sel.Sel.Name
	if ast.IsExported(implName) {
		return "實作必須是 unexported method（exported 的話它自己又是一個沒守門的 binding）"
	}
	if want := lowerFirst(fd.Name.Name); implName != want {
		return "實作必須叫 " + want + "（與 binding 一一對應），實得 " + implName
	}
	impl := methods[implName]
	if impl == nil {
		return "找不到實作 " + implName
	}
	if why := sameSignature(fd, impl); why != "" {
		return "實作簽章與 binding 不符：" + why
	}
	return forwardsParamsVerbatim(fd, call)
}

// assignedErrName：`if err := a.f(); …` 裡被指派的變數名（形狀不符回空字串）。
func assignedErrName(init ast.Stmt) string {
	as, isAssign := init.(*ast.AssignStmt)
	if !isAssign || len(as.Lhs) != 1 {
		return ""
	}
	id, isIdent := as.Lhs[0].(*ast.Ident)
	if !isIdent {
		return ""
	}
	return id.Name
}

// isNotNilCheck：cond 是不是 `<name> != nil`。
func isNotNilCheck(cond ast.Expr, name string) bool {
	bin, isBinary := cond.(*ast.BinaryExpr)
	if !isBinary || bin.Op != token.NEQ {
		return false
	}
	x, isIdent := bin.X.(*ast.Ident)
	y, isNil := bin.Y.(*ast.Ident)
	return isIdent && x.Name == name && isNil && y.Name == "nil"
}

// sameSignature：binding 與實作的參數／回傳型別是否逐字相同。
func sameSignature(binding, impl *ast.FuncDecl) string {
	if got, want := fieldListText(impl.Type.Params), fieldListText(binding.Type.Params); got != want {
		return "參數 " + got + " ≠ " + want
	}
	if got, want := fieldListText(impl.Type.Results), fieldListText(binding.Type.Results); got != want {
		return "回傳 " + got + " ≠ " + want
	}
	return ""
}

func fieldListText(fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	parts := make([]string, 0, len(fl.List))
	for _, f := range fl.List {
		names := make([]string, 0, len(f.Names))
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
		parts = append(parts, strings.Join(names, ",")+" "+exprText(f.Type))
	}
	return strings.Join(parts, "|")
}

func exprText(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return "*" + exprText(x.X)
	case *ast.SelectorExpr:
		return exprText(x.X) + "." + x.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprText(x.Elt)
	case *ast.MapType:
		return "map[" + exprText(x.Key) + "]" + exprText(x.Value)
	case *ast.Ellipsis:
		return "..." + exprText(x.Elt)
	case *ast.InterfaceType:
		return "interface{}"
	}
	return "?"
}

// forwardsParamsVerbatim：實作呼叫的引數必須就是 binding 的參數，順序相同。
//
// 這條擋的是 `return a.escalationAck("different-id")` 這種「形狀對、但送出去的
// 根本不是使用者給的東西」（reviewer 2026-08-19）。
func forwardsParamsVerbatim(fd *ast.FuncDecl, call *ast.CallExpr) string {
	var want []string
	if fd.Type.Params != nil {
		for _, f := range fd.Type.Params.List {
			for _, n := range f.Names {
				want = append(want, n.Name)
			}
		}
	}
	if len(call.Args) != len(want) {
		return "實作呼叫的引數個數（" + itoa(len(call.Args)) + "）與 binding 參數（" + itoa(len(want)) + "）不符"
	}
	for i, arg := range call.Args {
		id, isIdent := arg.(*ast.Ident)
		if !isIdent || id.Name != want[i] {
			return "第 " + itoa(i+1) + " 個引數必須原樣轉送參數 " + want[i]
		}
	}
	return ""
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// initCall：`if err := a.f(); …` 的那個呼叫（形狀不符回 nil）。
func initCall(init ast.Stmt) *ast.CallExpr {
	as, isAssign := init.(*ast.AssignStmt)
	if !isAssign || len(as.Rhs) != 1 {
		return nil
	}
	call, _ := as.Rhs[0].(*ast.CallExpr)
	return call
}

func callsAppMethod(call *ast.CallExpr, name string) bool {
	if call == nil {
		return false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel || sel.Sel.Name != name {
		return false
	}
	recv, isIdent := sel.X.(*ast.Ident)
	return isIdent && recv.Name == "a"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// ---- 各類別的契約（呼叫圖）----

// stateFieldNames：App 上代表「gate／escalation／evidence 狀態」的欄位。碰得到
// 這些欄位的函式即視為 state sink。
//
// 含惰性初始化的 once／err 與 evidence 的 in-flight registry。**刻意不含 phase／
// shutMu**：那是交易閘自己的狀態，納入的話每個守好門的 binding 都會「可達
// state」，契約退化成恆真。
var stateFieldNames = []string{
	"gateSvc", "gateJournal", "gateReg", "gateOnce", "gateInitErr",
	"specRepo", "planRepo",
	"escSvc", "escJournal", "escOnce", "escInitErr",
	"evidenceJournal", "evidenceCASDir", "evidenceRegistryPath",
	"evidenceActive", "evidenceMu",
}

// osMutators：會改動磁碟的 os 函式（readOnly 那一類不得可達）。
var osMutators = map[string]bool{
	"WriteFile": true, "Create": true, "OpenFile": true, "Rename": true,
	"Remove": true, "RemoveAll": true, "MkdirAll": true, "Mkdir": true,
}

// TestBindingClassContracts
//
// 每一類各自的契約，用呼叫圖驗：
//
//	appTxn          → 必須可達 beginAppTxn；不得碰 state 欄位。
//	readOnly        → 不得可達任何交易閘、state 欄位或 os 寫入函式。
//	runtimeMutating → 不得碰 state 欄位。
//	以上三類一律 → 不得**繞過薄包裝**直接呼叫 state 實作。
//
// 繞過的判定方式：建圖時刻意**不連** wrapper→impl 那條邊（那是唯一合法的路徑），
// 於是任何仍然可達 impl 的路徑都是繞過。
func TestBindingClassContracts(t *testing.T) {
	_, files, info, pkg := typeCheckProductionPackage(t)
	appType := namedType(t, pkg, "App")
	appStruct, isStruct := appType.Underlying().(*types.Struct)
	if !isStruct {
		t.Fatal("App 不是 struct（守門前提不成立）")
	}
	stateFields := map[*types.Var]bool{}
	for _, name := range stateFieldNames {
		found := false
		for i := range appStruct.NumFields() {
			if appStruct.Field(i).Name() == name {
				stateFields[appStruct.Field(i)] = true
				found = true
			}
		}
		if !found {
			t.Fatalf("App 上沒有欄位 %q：守門的 state 定義已與實作脫節", name)
		}
	}
	methods := appMethodsByName(files, info, appType)
	stateImpls := map[string]bool{} // 合格薄包裝所指向的 unexported 實作
	for name, fd := range methods {
		if fd.Name.IsExported() && thinWrapperViolation(fd, methods) == "" {
			stateImpls[lowerFirst(name)] = true
		}
	}

	bodies := map[*types.Func]*ast.FuncDecl{}
	for _, f := range files {
		for _, decl := range f.Decls {
			if fd, isFunc := decl.(*ast.FuncDecl); isFunc && fd.Body != nil {
				if obj, _ := info.Defs[fd.Name].(*types.Func); obj != nil {
					bodies[obj] = fd
				}
			}
		}
	}
	calls := map[*types.Func][]*types.Func{}
	marks := map[*types.Func]map[string]bool{}
	mark := func(fn *types.Func, k string) {
		if marks[fn] == nil {
			marks[fn] = map[string]bool{}
		}
		marks[fn][k] = true
	}
	for fn, fd := range bodies {
		legit := "" // 這個函式自己的合法 wrapper→impl 邊
		if fd.Name.IsExported() && stateImpls[lowerFirst(fd.Name.Name)] {
			legit = lowerFirst(fd.Name.Name)
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				var id *ast.Ident
				switch fun := x.Fun.(type) {
				case *ast.Ident:
					id = fun
				case *ast.SelectorExpr:
					id = fun.Sel
				}
				if id == nil {
					return true
				}
				callee, _ := info.Uses[id].(*types.Func)
				if callee == nil {
					return true
				}
				switch callee.Name() {
				case "beginAppTxn":
					mark(fn, "appTxn")
				case "beginStateTxn":
					mark(fn, "stateTxn")
				}
				if callee.Pkg() != nil && callee.Pkg().Name() == "os" && osMutators[callee.Name()] {
					mark(fn, "osWrite")
				}
				if callee.Name() == legit {
					return true // 合法的 wrapper→impl 邊：不建
				}
				if stateImpls[callee.Name()] {
					mark(fn, "stateImpl")
				}
				calls[fn] = append(calls[fn], callee)
			case *ast.SelectorExpr:
				if sel := info.Selections[x]; sel != nil {
					if v, isVar := sel.Obj().(*types.Var); isVar && stateFields[v] {
						mark(fn, "stateField")
					}
				}
			}
			return true
		})
	}
	// 定點迭代（呼叫圖有環，遞迴 DFS 會在環上回傳假的 false）。
	for changed := true; changed; {
		changed = false
		for fn := range bodies {
			for _, callee := range calls[fn] {
				if _, known := bodies[callee]; !known {
					continue
				}
				for k := range marks[callee] {
					if !marks[fn][k] {
						mark(fn, k)
						changed = true
					}
				}
			}
		}
	}

	var offenders []string
	for fn, fd := range bodies {
		if fd.Recv == nil || !fd.Name.IsExported() || !isAppMethod(fn, appType) {
			continue
		}
		name := fd.Name.Name
		class := bindingClass(name)
		if class == "" {
			continue // state 薄包裝：由形狀那條測試負責
		}
		m := marks[fn]
		if m["stateField"] {
			offenders = append(offenders, name+"（"+class+"）：碰得到 gate／escalation／evidence 狀態，應改成 state 薄包裝")
		}
		if m["stateImpl"] {
			offenders = append(offenders, name+"（"+class+"）：繞過薄包裝直接呼叫 state 實作")
		}
		switch class {
		case "appTxn":
			if !m["appTxn"] {
				offenders = append(offenders, name+"（appTxn）：宣稱走 app 交易，但呼叫圖上到不了 beginAppTxn")
			}
		case "readOnly":
			if m["appTxn"] || m["stateTxn"] {
				offenders = append(offenders, name+"（readOnly）：宣稱唯讀卻進了交易閘——分類錯了")
			}
			if m["osWrite"] {
				offenders = append(offenders, name+"（readOnly）：宣稱唯讀卻可達 os 的寫入函式")
			}
		}
	}
	sort.Strings(offenders)
	if len(offenders) != 0 {
		t.Fatalf("以下 binding 不符合它所屬類別的契約：\n  %s", strings.Join(offenders, "\n  "))
	}
}
