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
//	第二版：改成「呼叫圖可達 state 欄位就必須呼叫 beginTxn」。只確認**出現
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

// readOnlyBindings：**唯一**不必是薄包裝的類別——只讀，不改動任何 durable state。
//
// 前一版分成 appTxn／readOnly／runtimeMutating 三類，各自套不同契約。複核指出
// 那樣分仍然漏（reviewer 2026-08-20）：
//
//   - appTxn 的契約只要求「某條路徑可達交易閘」，於是 StartLogin("claude") 直接
//     開 Terminal、Logout("claude") 直接執行 CLI，兩條 Claude 分支從未進交易，
//     收尾之後照樣執行外部程式。
//   - runtimeMutating 的契約只看 gate／escalation／evidence 三組欄位，於是
//     SendMessage 經 Manager.AcceptSubmit 寫 events.jsonl 與 replay index、
//     ResolveApproval 經 callback 寫 Manager sink，全都在契約之外。
//
// 所以不再按「改的是哪一種狀態」分類，改成一條界線：**會不會改動 durable
// state**。會的一律是薄包裝（交易閘在任何分支之前），不會的列在這裡。
//
// 每一筆的理由必須是「為什麼它完全不寫」，不能是「它有自己的交易」。
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
	"RestoreViews":      "讀 restore store 的 view 快照",
	"SpecList":          "讀 spec 樹",
	"SpecRead":          "讀 spec 檔",
}

// bindingClass：名稱 → 類別（空字串＝必須是薄包裝）。
func bindingClass(name string) string {
	if readOnlyBindings[name] != "" {
		return "readOnly"
	}
	return ""
}

// TestEveryBindingIsClassifiedAndHonoursItsShape
//
// 正題斷言：每個 *App exported method 恰好落在一類，且 state 薄包裝符合完整形狀。
//
// 形狀**逐一驗到底**（reviewer 2026-08-19）：先前只確認「body 裡有 beginTxn
// 且恰好三個敘述」，於是下面這種寫法照樣通過——
//
//	if err := a.beginTxn(); err == nil { return err }   // 條件反了
//	defer a.endTxn()
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
	for _, m := range []map[string]string{readOnlyBindings} {
		for name := range m {
			if fd := methods[name]; fd == nil || !fd.Name.IsExported() {
				t.Errorf("分類清單列了不存在的 exported method %q：請同步清理", name)
			}
		}
	}
	t.Logf("薄包裝 %d 個、唯讀 %d 個（合計 %d）",
		len(gated), len(readOnlyBindings), len(gated)+len(readOnlyBindings))
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
//  2. if err := a.beginTxn(); err != nil { return <零值…>, err }
//     —— 條件必須是 err != nil，且必須**原樣**回傳那個 err
//  3. defer a.endTxn()
//  4. return a.<unexported>(<原樣轉送的參數>)
//     —— 實作必須是同名的 unexported method，簽章相同
func thinWrapperViolation(fd *ast.FuncDecl, methods map[string]*ast.FuncDecl) string {
	body := fd.Body.List
	if len(body) != 3 {
		return "body 有 " + itoa(len(body)) + " 個敘述，薄包裝必須恰好三個"
	}
	ifs, isIf := body[0].(*ast.IfStmt)
	if !isIf {
		return "第一個敘述必須是 `if err := a.beginTxn(); err != nil { … }`"
	}
	gateCall := initCall(ifs.Init)
	if !callsAppMethod(gateCall, "beginTxn") {
		return "第一個敘述必須是 `if err := a.beginTxn(); err != nil { … }`"
	}
	if len(gateCall.Args) != 0 {
		return "beginTxn 必須零參數呼叫（帶參數的變體可以讓閘的行為被呼叫端改掉）"
	}
	errName := assignedErrName(ifs.Init)
	if errName == "" {
		return "beginTxn 的結果必須指派給一個變數再判斷"
	}
	if !isNotNilCheck(ifs.Cond, errName) {
		return "條件必須是 `" + errName + " != nil`（寫成 == nil 會在成功時直接返回、失敗時反而繼續執行）"
	}
	if ifs.Body == nil || len(ifs.Body.List) != 1 {
		return "beginTxn 失敗時必須直接返回，不得做其他事"
	}
	ret, isReturn := ifs.Body.List[0].(*ast.ReturnStmt)
	if !isReturn {
		return "beginTxn 失敗時必須 return"
	}
	if len(ret.Results) == 0 {
		return "拒絕時必須把錯誤回給呼叫端"
	}
	last, isIdent := ret.Results[len(ret.Results)-1].(*ast.Ident)
	if !isIdent || last.Name != errName {
		return "拒絕時必須原樣回傳 " + errName + "（換成別的錯誤會蓋掉 lifecycle 的拒絕原因）"
	}
	// 其餘回傳值必須是零值字面量。`return a.startupErrText(), err` 這種寫法在
	// 交易被拒之後仍去讀 app 狀態，而且最後一項是 err 就過關（reviewer 2026-08-20）。
	for i, r := range ret.Results[:len(ret.Results)-1] {
		if !isZeroLiteral(r) {
			return "拒絕時第 " + itoa(i+1) + " 個回傳值必須是零值字面量，不得執行任何運算"
		}
	}
	def, isDefer := body[1].(*ast.DeferStmt)
	if !isDefer || !callsAppMethod(def.Call, "endTxn") {
		return "第二個敘述必須是 `defer a.endTxn()`（少了它 shutdown 會永遠等不到 inflight 歸零）"
	}
	if len(def.Call.Args) != 0 {
		return "endTxn 必須零參數呼叫"
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

// isZeroLiteral：零值字面量（nil／""／0／false／T{}）。刻意不接受任何呼叫或
// 選取——拒絕路徑上不該再去讀 app 的任何狀態。
func isZeroLiteral(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "nil" || x.Name == "false"
	case *ast.BasicLit:
		return x.Value == `""` || x.Value == "0"
	case *ast.CompositeLit:
		return len(x.Elts) == 0
	}
	return false
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

// durableWriters：會把東西寫進 durable state 的入口（函式／方法名）。唯讀那一類
// 不得可達。名稱比對是刻意的取捨——這些型別分散在 internal/ 各套件，用名稱抓得
// 到跨套件的呼叫，代價是同名的無害方法也會被抓（目前沒有）。
var durableWriters = map[string]bool{
	"AcceptSubmit": true, "BeginNewSessionSubmit": true, "BeginSubmit": true,
	"Emit": true, "EmitApprovalDecision": true, "EmitWorkspace": true, "EmitGateEvent": true,
	"Append": true, "AppendReceipt": true, "Observe": true, "Flush": true,
	"Put": true, "Sync": true, "MarkMigrated": true, "CommitCreate": true,
}

// osMutators：會改動磁碟的 os 函式（readOnly 那一類不得可達）。
var osMutators = map[string]bool{
	"WriteFile": true, "Create": true, "OpenFile": true, "Rename": true,
	"Remove": true, "RemoveAll": true, "MkdirAll": true, "Mkdir": true,
}

// TestProtectedImplementationsAreOnlyReachedThroughTheirWrapper
//
// 受保護的實作（薄包裝指向的 unexported method）必須滿足兩條，用 go/types 的
// Uses 逐一比對 object，不用「呼叫圖有沒有這條邊」推論——function value、method
// value、closure 捕捉都會在 Uses 留下引用，呼叫圖卻看不到（reviewer 2026-08-20
// 實測那幾種寫法都繞得過前一版）：
//
//	(1) 每一個引用都必須是**直接呼叫**，不得把它當值傳遞。取成 function value
//	    之後可以在任何地方、任何時間被叫起來，交易涵蓋範圍就失去意義。
//	(2) 引用它的地方不得是**另一個 exported binding**。exported 的入口一律是薄
//	    包裝，只會呼叫自己的實作；別的 exported method 直接叫它就是繞過閘門。
//	    unexported 的內部呼叫則允許——那些程式碼本身只從已持有交易的實作進得去
//	    （例如 gateList 這種投影讀取，7 個實作共用）。
//
// 另外反向驗一條：受保護的實作**不得回頭呼叫任何 exported binding**，否則交易
// 會被重複開啟，而且 wrapper↔impl 的一一對應被繞成環。
func TestProtectedImplementationsAreOnlyReachedThroughTheirWrapper(t *testing.T) {
	fset, files, info, pkg := typeCheckProductionPackage(t)
	appType := namedType(t, pkg, "App")
	methods := appMethodsByName(files, info, appType)

	protected := map[types.Object]string{}     // impl object → 它的 wrapper 名稱
	legitCall := map[types.Object]*ast.Ident{} // impl object → wrapper 裡那個合法引用
	exported := map[types.Object]bool{}
	for name, fd := range methods {
		if obj, _ := info.Defs[fd.Name].(*types.Func); obj != nil && fd.Name.IsExported() {
			exported[obj] = true
		}
		if !fd.Name.IsExported() || thinWrapperViolation(fd, methods) != "" {
			continue
		}
		sel := fd.Body.List[2].(*ast.ReturnStmt).Results[0].(*ast.CallExpr).Fun.(*ast.SelectorExpr)
		obj := info.Uses[sel.Sel]
		if obj == nil {
			t.Fatalf("%s：解析不到實作的型別物件", name)
		}
		protected[obj] = name
		legitCall[obj] = sel.Sel
	}
	if len(protected) == 0 {
		t.Fatal("找不到任何受保護的實作：守門失效")
	}

	// 逐檔走，維護「目前所在的 exported binding」與父節點鏈。
	var offenders []string
	note := func(pos token.Pos, msg string) {
		offenders = append(offenders, fset.Position(pos).String()+"："+msg)
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fd.Body == nil {
				continue
			}
			inExported := fd.Recv != nil && fd.Name.IsExported()
			implOfThisWrapper := types.Object(nil)
			if inExported {
				if obj := info.Defs[fd.Name]; obj != nil {
					for impl, owner := range protected {
						if owner == fd.Name.Name {
							implOfThisWrapper = impl
						}
					}
					_ = obj
				}
			}
			var parents []ast.Node
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if n == nil {
					parents = parents[:len(parents)-1]
					return true
				}
				defer func() { parents = append(parents, n) }()
				id, isIdent := n.(*ast.Ident)
				if !isIdent {
					return true
				}
				obj := info.Uses[id]
				if obj == nil {
					return true
				}
				if _, isProtected := protected[obj]; isProtected {
					if id == legitCall[obj] {
						return true // 自己那個薄包裝的呼叫
					}
					if !isDirectCallee(id, parents) {
						note(id.Pos(), "受保護的實作 "+obj.Name()+" 被當成值取用（function value／method value）")
						return true
					}
					if inExported && obj != implOfThisWrapper {
						note(id.Pos(), "exported binding "+fd.Name.Name+" 直接呼叫別的受保護實作 "+obj.Name())
					}
					return true
				}
				// 反向：受保護的實作不得回頭呼叫 exported binding。
				if exported[obj] && protected[info.Defs[fd.Name]] != "" && isDirectCallee(id, parents) {
					note(id.Pos(), "受保護的實作 "+fd.Name.Name+" 回頭呼叫 exported binding "+obj.Name())
				}
				return true
			})
		}
	}
	sort.Strings(offenders)
	if len(offenders) != 0 {
		t.Fatalf("受保護的實作只能經由它自己的薄包裝進入：\n  %s", strings.Join(offenders, "\n  "))
	}
}

// isDirectCallee：這個 ident 是不是「被直接呼叫」的那個位置（a.foo() 的 foo）。
func isDirectCallee(id *ast.Ident, parents []ast.Node) bool {
	if len(parents) < 2 {
		return false
	}
	sel, isSel := parents[len(parents)-1].(*ast.SelectorExpr)
	if !isSel || sel.Sel != id {
		return false
	}
	call, isCall := parents[len(parents)-2].(*ast.CallExpr)
	return isCall && call.Fun == ast.Expr(sel)
}

// TestReadOnlyBindingsWriteNothing
//
// 唯讀那一類的契約：碰不到 gate／escalation／evidence 的 App 欄位、不進交易閘、
// 不可達 os 的寫入函式，也不可達會寫 durable state 的 Manager 入口。
//
// 最後一項是這一輪補的（reviewer 2026-08-20）：先前只看三組 App 欄位，於是
// SendMessage 經 Manager.AcceptSubmit 寫 events.jsonl 與 replay index、
// ResolveApproval 經 callback 寫 Manager sink，兩者都在契約之外。那兩個現在已經
// 是薄包裝，但這條契約要留著——否則下一個被歸進唯讀的 binding 又會從這裡漏。
func TestReadOnlyBindingsWriteNothing(t *testing.T) {
	fset, files, info, pkg := typeCheckProductionPackage(t)
	_ = fset
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
				if callee.Name() == "beginTxn" {
					mark(fn, "txn")
				}
				if callee.Pkg() != nil && callee.Pkg().Name() == "os" && osMutators[callee.Name()] {
					mark(fn, "osWrite")
				}
				if durableWriters[callee.Name()] {
					mark(fn, "durableWrite")
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
	for changed := true; changed; { // 定點迭代（呼叫圖有環）
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
		if bindingClass(fd.Name.Name) != "readOnly" {
			continue
		}
		for _, c := range []struct{ key, why string }{
			{"stateField", "碰得到 gate／escalation／evidence 狀態"},
			{"txn", "進得了交易閘（那代表它會改動狀態，分類錯了）"},
			{"osWrite", "可達 os 的寫入函式"},
			{"durableWrite", "可達會寫 durable state 的入口"},
		} {
			if marks[fn][c.key] {
				offenders = append(offenders, fd.Name.Name+"（readOnly）："+c.why)
			}
		}
	}
	sort.Strings(offenders)
	if len(offenders) != 0 {
		t.Fatalf("以下 binding 宣稱唯讀，但它做得到別的事：\n  %s", strings.Join(offenders, "\n  "))
	}
}
