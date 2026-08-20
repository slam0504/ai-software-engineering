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

// startupSafeBindings：**唯一**不必是薄包裝的 exported method。
//
// 前一版把 12 個「唯讀」全部放行，複核指出唯讀不等於 startup-safe
// （reviewer 2026-08-20）：LoadTurnsBefore 與 startup 同時讀寫 a.replayIndex 是
// 真的 data race；RestoreViews 在 a.restore 尚未初始化時 nil pointer panic；
// ListSessions／PaneLayout 讀尚未發布的 a.wsReg；ReadDiagram 讀尚未設定的
// diagramPath。Wails 的 OnStartup 與 bindings 並行，這些都是 production 可達。
//
// 所以判準不是「寫不寫」，是「**依不依賴 startup 發布的東西**」。依賴的一律進閘
// （唯讀也一樣，交易只是短暫持有）；只有刻意支援啟動診斷、且自己把讀到的每個
// 欄位都放進同一把鎖的，才留在這裡。
//
// 目前只有一個，而且它的契約由 TestStartupSafeBindingTouchesOnlyGuardedFields
// 逐欄位驗證——不是靠這句話。
var startupSafeBindings = map[string]string{
	"CLIInfo": "啟動診斷：只讀 startupMu 保護的快照（含 workspace 的受鎖副本）",
}

// bindingClass：名稱 → 類別（空字串＝必須是薄包裝）。
func bindingClass(name string) string {
	if startupSafeBindings[name] != "" {
		return "startupSafe"
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

	// **用真正的 method set 枚舉**（reviewer 2026-08-20）：只掃 receiver 直接寫成
	// App／*App 的 FuncDecl，會漏掉匿名嵌入型別 promote 上來的 exported method
	// ——`go doc` 看得到、Wails 的 reflection 也會綁它，分類測試卻數不到。
	var offenders, gated []string
	for _, name := range exportedMethodSetNames(t, appType) {
		fd := methods[name]
		if fd == nil {
			offenders = append(offenders,
				name+"：出現在 *App 的 method set 裡（Wails 會綁定它），但不是 App 自己宣告的方法"+
					"——嵌入型別 promote 上來的 method 無法套用薄包裝契約，請改成 App 上的明確宣告")
			continue
		}
		class := bindingClass(name)
		why := thinWrapperViolationTyped(fd, methods, methodSignature(info, fd))
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
	for _, m := range []map[string]string{startupSafeBindings} {
		for name := range m {
			if fd := methods[name]; fd == nil || !fd.Name.IsExported() {
				t.Errorf("分類清單列了不存在的 exported method %q：請同步清理", name)
			}
		}
	}
	t.Logf("薄包裝 %d 個、startup-safe %d 個（合計 %d）",
		len(gated), len(startupSafeBindings), len(gated)+len(startupSafeBindings))
}

// TestOnlyThinWrappersAdmit
//
// `beginTxn` 只能被合格的薄包裝呼叫。
//
// 這條擋的是**巢狀 admission**（reviewer 2026-08-20）：外層取得 ownership 之後
// shutdown 可能切換 phase，內層 admission 隨即以「shutting down」拒絕，操作做到
// 一半才失敗。SpecAssist／PlanAssist 的內層交易與 ensureAppServer 的自有交易都是
// 這個形狀，已經移除；這條讓它們回不來。
//
// 非 admission 的 phase 查詢（例如 ensureAppServer「收尾後不建新 server」的資源
// 政策）走 phaseNow()，不在這條規則之內——它不登記交易，也不會否決已被放行的
// 操作。
func TestOnlyThinWrappersAdmit(t *testing.T) {
	fset, files, info, pkg := typeCheckProductionPackage(t)
	appType := namedType(t, pkg, "App")
	methods := appMethodsByName(files, info, appType)

	wrapper := map[string]bool{}
	for name, fd := range methods {
		if fd.Name.IsExported() && thinWrapperViolationTyped(fd, methods, methodSignature(info, fd)) == "" {
			wrapper[name] = true
		}
	}
	var offenders []string
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				id, isIdent := n.(*ast.Ident)
				if !isIdent || id.Name != "beginTxn" {
					return true
				}
				if fn, _ := info.Uses[id].(*types.Func); fn == nil || fn.Name() != "beginTxn" {
					return true
				}
				if wrapper[fd.Name.Name] {
					return true
				}
				offenders = append(offenders, fd.Name.Name+" 於 "+fset.Position(id.Pos()).String())
				return true
			})
		}
	}
	sort.Strings(offenders)
	if len(offenders) != 0 {
		t.Fatalf("只有合格的薄包裝可以呼叫 beginTxn（巢狀 admission 會讓操作做到一半才失敗）：\n  %s",
			strings.Join(offenders, "\n  "))
	}
	if len(wrapper) == 0 {
		t.Fatal("找不到任何薄包裝：守門失效")
	}
}

// assistImplementations：會做大量前置阻塞工作的 assist 實作。
var assistImplementations = []string{"specAssist", "planAssist"}

// assistPreRootAllowed：assist 實作在取得 root context **之前**允許出現的呼叫。
//
// 只有純參數驗證。清單刻意極小：每加一筆就等於承認「這件事在不可取消的範圍內
// 做也沒關係」，那必須是有人明確判斷過的。
var assistPreRootAllowed = map[string]bool{
	"knownProvider": true, // provider 白名單（純比對，無 I/O）
	"Errorf":        true, // fmt.Errorf
	// 取得 root 的那一行本身：`context.WithTimeout(a.procRoot(), …)` 的外層呼叫
	// 會先被走訪到，內層才是 procRoot。
	"WithTimeout": true,
	"WithCancel":  true,
}

// TestAssistImplementationsTakeRootContextFirst
//
// assist 實作在取得 procRoot 之前**不得有任何其他呼叫**。
//
// 前一版只找「第一個對 App 的呼叫」，於是在 procRoot() 之前插一個裸
// exec.Command 照樣通過（reviewer 2026-08-20）。判準因此改成「取得 root 之前只
// 能出現 assistPreRootAllowed 裡那幾個純函式」——不分是不是 App method。
func TestAssistImplementationsTakeRootContextFirst(t *testing.T) {
	fset, files, info, pkg := typeCheckProductionPackage(t)
	_ = files
	appType := namedType(t, pkg, "App")
	methods := appMethodsByName(files, info, appType)

	for _, name := range assistImplementations {
		fd := methods[name]
		if fd == nil {
			t.Fatalf("找不到 %s（守門與實作脫節）", name)
		}
		seenRoot := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if seenRoot {
				return false
			}
			call, isCall := n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			var id *ast.Ident
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				id = fun
			case *ast.SelectorExpr:
				id = fun.Sel
			}
			if id == nil {
				return true
			}
			if id.Name == "procRoot" {
				if fn, _ := info.Uses[id].(*types.Func); fn != nil && isAppMethod(fn, appType) {
					seenRoot = true
					return false
				}
			}
			if !assistPreRootAllowed[id.Name] {
				t.Errorf("%s 在取得 procRoot 之前呼叫了 %s（%s）——那段工作落在不可取消的範圍內",
					name, id.Name, fset.Position(id.Pos()).String())
				seenRoot = true // 只報第一筆，避免整串雜訊
				return false
			}
			return true
		})
		if !seenRoot {
			t.Errorf("%s 必須取得 procRoot 作為可取消的根 context", name)
		}
	}
}

// methodSignature：FuncDecl 的型別簽章（拿不到回 nil，此時零值判定退回字面量層級）。
func methodSignature(info *types.Info, fd *ast.FuncDecl) *types.Signature {
	fn, _ := info.Defs[fd.Name].(*types.Func)
	if fn == nil {
		return nil
	}
	sig, _ := fn.Type().(*types.Signature)
	return sig
}

// exportedMethodSetNames：*App 的**真實** method set 裡所有 exported method 名稱
// ——與 Wails 綁定看到的是同一份（含嵌入型別 promote 上來的）。
func exportedMethodSetNames(t *testing.T, appType *types.Named) []string {
	t.Helper()
	ms := types.NewMethodSet(types.NewPointer(appType))
	var out []string
	for i := range ms.Len() {
		fn, isFunc := ms.At(i).Obj().(*types.Func)
		if !isFunc || !fn.Exported() {
			continue
		}
		out = append(out, fn.Name())
	}
	sort.Strings(out)
	return out
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
	return thinWrapperViolationTyped(fd, methods, nil)
}

// thinWrapperViolationTyped：同上，但帶型別資訊時另外用**型別語意**判定零值
// （nilable 只能 nil、struct／array 才能用空 composite literal）。
func thinWrapperViolationTyped(fd *ast.FuncDecl, methods map[string]*ast.FuncDecl, sig *types.Signature) string {
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
		return "beginTxn 的結果必須以 `:=` 指派給區域變數再判斷（`=` 會共用外層變數，並行時互相覆寫）"
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
		if sig == nil || i >= sig.Results().Len() {
			continue
		}
		if why := zeroValueMismatch(r, sig.Results().At(i).Type()); why != "" {
			return "拒絕時第 " + itoa(i+1) + " 個回傳值" + why
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

// zeroValueMismatch：字面量與型別的零值是否真的相同（空字串＝相同）。
//
// `[]GateEntryDTO{}` 是**非 nil** 的空 slice，與 nil 不是同一個值；呼叫端據此
// 分辨「沒有資料」與「沒查」的話，契約就被改掉了（reviewer 2026-08-20）。
func zeroValueMismatch(e ast.Expr, typ types.Type) string {
	switch types.Unalias(typ).Underlying().(type) {
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan, *types.Signature, *types.Interface:
		if id, isIdent := e.(*ast.Ident); !isIdent || id.Name != "nil" {
			return "必須是 nil（這個型別的零值是 nil，空的 composite literal 是另一個值）"
		}
	case *types.Basic:
		if _, isLit := e.(*ast.BasicLit); !isLit {
			if id, isIdent := e.(*ast.Ident); !isIdent || id.Name != "false" {
				return "必須是該基本型別的零值常數"
			}
		}
	case *types.Struct, *types.Array:
		if lit, isLit := e.(*ast.CompositeLit); !isLit || len(lit.Elts) != 0 {
			return "必須是空的 composite literal"
		}
	}
	return ""
}

// assignedErrName：`if err := a.f(); …` 裡被指派的變數名（形狀不符回空字串）。
func assignedErrName(init ast.Stmt) string {
	as, isAssign := init.(*ast.AssignStmt)
	if !isAssign || len(as.Lhs) != 1 {
		return ""
	}
	// **必須是 :=**（reviewer 2026-08-20）：改成 `err =` 指到 package-global 就
	// 會讓所有 binding 共用同一個變數，並行呼叫互相覆寫彼此的 admission 結果。
	if as.Tok != token.DEFINE {
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

// protectedImplInternalCallers：允許直接呼叫受保護實作的**具名**內部呼叫端，每筆
// 附 ownership 理由。
//
// 比對用解析後的 caller→callee **物件對**，不是名稱字串（reviewer 2026-08-20）：
// 名稱比對會放行「另一個同名的 package function」與「經 local interface 呼叫」。
// 另外每一筆都必須真的被用到——未使用的預授權是放寬了卻沒人知道的洞。
//
// 同步性也一併驗：呼叫不得位於 go／defer／closure 內。那三種寫法會讓工作跑在
// 呼叫端的交易之外，ownership 的理由就不成立了。
//
// key＝實作名稱，value＝允許的 App method 名 → 理由。
var protectedImplInternalCallers = map[string]map[string]string{
	"gateList": {
		"submitPlanForApproval":    "同一交易內的 gate projection 讀取",
		"runEvidence":              "同上",
		"evidenceCommitCandidates": "同上",
		"submitTestContract":       "同上",
		"gateDecisionContext":      "同上",
		"planAssist":               "同上",
		"validateTestCommit":       "同上",
	},
	"resolveApproval": {
		"denyApprovals": "shutdown 與 RemoveSession 兩條 ownership 之下的 fail-closed deny" +
			"（此刻交易閘必然拒絕，不能走 binding）",
	},
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

	// 具名白名單解析成物件對（名稱字串比對擋不住同名的 package function）。
	approvedPairs := map[callerCalleePair]bool{}
	usedPairs := map[callerCalleePair]bool{}
	for implName, callers := range protectedImplInternalCallers {
		implFD := methods[implName]
		if implFD == nil {
			t.Fatalf("protectedImplInternalCallers 列了不存在的實作 %q", implName)
		}
		implObj := types.Object(info.Defs[implFD.Name])
		for callerName := range callers {
			callerFD := methods[callerName]
			if callerFD == nil {
				t.Fatalf("protectedImplInternalCallers[%q] 列了不存在的呼叫端 %q", implName, callerName)
			}
			callerObj := types.Object(info.Defs[callerFD.Name])
			approvedPairs[callerCalleePair{caller: callerObj, callee: implObj}] = true
		}
	}

	exportedNames := map[string]bool{}         // exported binding 的名稱（介面派送比對用）
	protected := map[types.Object]string{}     // impl object → 它的 wrapper 名稱
	legitCall := map[types.Object]*ast.Ident{} // impl object → wrapper 裡那個合法引用
	exported := map[types.Object]bool{}
	for name, fd := range methods {
		if obj, _ := info.Defs[fd.Name].(*types.Func); obj != nil && fd.Name.IsExported() {
			exported[obj] = true
			exportedNames[fd.Name.Name] = true
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
						return true
					}
					if !inExported {
						pair := callerCalleePair{caller: info.Defs[fd.Name], callee: obj}
						if !approvedPairs[pair] {
							note(id.Pos(), "未具名的內部呼叫端 "+fd.Name.Name+" 直接呼叫受保護實作 "+
								obj.Name()+"（要允許就加進 protectedImplInternalCallers 並寫下 ownership 理由）")
							return true
						}
						usedPairs[pair] = true
						if why := asyncContext(parents); why != "" {
							note(id.Pos(), fd.Name.Name+" 在"+why+"內呼叫受保護實作 "+obj.Name()+
								"——那會跑在呼叫端的交易之外，ownership 理由不成立")
						}
					}
					return true
				}
				// 介面派送：Uses 指向的是 interface 的 method object，比對具體
				// object 抓不到（reviewer 2026-08-20）。所以另外看「receiver 是
				// interface 且方法名撞到 exported binding」——那正是「宣告一個
				// 剛好長得像 App 的 local interface，再拿 a 去滿足它」的形狀。
				//
				// 只在 receiver 是 interface 時才判：具體型別上的同名方法
				// （spec.GitRepo.PreviewSpecCommit、Manager.RemoveSession）是不同
				// 的東西，用名稱一律擋會把它們也誤殺。
				if protected[info.Defs[fd.Name]] != "" && exportedNames[id.Name] && !exported[obj] {
					if sel, isSel := parents[len(parents)-1].(*ast.SelectorExpr); isSel && sel.Sel == id {
						if s := info.Selections[sel]; s != nil && types.IsInterface(s.Recv()) {
							note(id.Pos(), "受保護的實作 "+fd.Name.Name+
								" 經介面呼叫與 exported binding 同名的 "+id.Name+
								"（local interface 可以讓 a 滿足它，等於繞過薄包裝）")
						}
					}
				}
				// 反向：受保護的實作不得**以任何形式**引用 exported binding。
				// 只擋直接呼叫的話，`_ = a.CreateSession` 這種取值仍然通過，而
				// function value／callback／間接遞迴都是從取值開始的
				// （reviewer 2026-08-20）。
				if exported[obj] && protected[info.Defs[fd.Name]] != "" {
					how := "引用"
					if isDirectCallee(id, parents) {
						how = "呼叫"
					}
					note(id.Pos(), "受保護的實作 "+fd.Name.Name+" 回頭"+how+" exported binding "+obj.Name())
				}
				return true
			})
		}
	}
	// 未被用到的預授權＝放寬了卻沒人知道的洞。
	for pair := range approvedPairs {
		if !usedPairs[pair] {
			offenders = append(offenders, "protectedImplInternalCallers 有未使用的預授權："+
				pair.caller.Name()+" → "+pair.callee.Name()+"（請移除）")
		}
	}
	sort.Strings(offenders)
	if len(offenders) != 0 {
		t.Fatalf("受保護的實作只能經由它自己的薄包裝進入：\n  %s", strings.Join(offenders, "\n  "))
	}
}

// callerCalleePair：解析後的呼叫關係（物件對，不是名稱）。
type callerCalleePair struct{ caller, callee types.Object }

// asyncContext：這個位置是不是在 go／defer／closure 內（回傳描述；空字串＝同步）。
func asyncContext(parents []ast.Node) string {
	for _, n := range parents {
		switch n.(type) {
		case *ast.GoStmt:
			return " goroutine"
		case *ast.DeferStmt:
			return " defer"
		case *ast.FuncLit:
			return " closure"
		}
	}
	return ""
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

// TestStartupSafeBindingHasFixedShape
//
// startup-safe binding 的契約改成**一層固定形狀**（reviewer 2026-08-20）：
//
//  1. 對 App 的呼叫只能有一個，而且必須是 startupSnapshot()。
//  2. 其餘呼叫只能是 package function，且簽章不得含 *App／App。
//  3. 不得直接讀取任何 App 欄位（只能用 startupSnapshot 回來的那份純資料）。
//
// 為什麼不繼續擴充呼叫圖分析：前一版逐層追欄位，但只涵蓋直接呼叫——把不安全的
// helper 改成 function value（`f := a.cliUnsafeProbe; _ = f()`）就整條溜過去，
// interface dispatch、callback、method value 也一樣。與其宣稱「能完整分析所有 Go
// 呼叫形式」，不如把可分析的範圍縮到一層：能碰到的東西被形狀限死，就不需要那個
// 宣稱。
func TestStartupSafeBindingHasFixedShape(t *testing.T) {
	fset, files, info, pkg := typeCheckProductionPackage(t)
	_ = files
	appType := namedType(t, pkg, "App")
	methods := appMethodsByName(files, info, appType)

	for name := range startupSafeBindings {
		fd := methods[name]
		if fd == nil {
			t.Fatalf("startupSafeBindings 列了不存在的 method %q", name)
		}
		// **限制 receiver 的所有 Uses**（reviewer 2026-08-20）：形狀只看 selector
		// 的話，`cliUnsafeWholeApp(a)` 沒有 selector——把整個 *App 傳出去，helper
		// 收 any／interface 再 type assert 回來讀 replayIndex，測試照樣通過。
		recvObj := receiverObject(info, fd)
		if recvObj == nil {
			t.Fatalf("%s 沒有具名 receiver（守門前提不成立）", name)
		}
		appSelectors := 0
		var parents []ast.Node
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if n == nil {
				parents = parents[:len(parents)-1]
				return true
			}
			defer func() { parents = append(parents, n) }()
			switch x := n.(type) {
			case *ast.Ident:
				// receiver 只能出現在那一個合法位置：a.startupSnapshot() 的 X。
				if info.Uses[x] != recvObj {
					return true
				}
				if len(parents) > 0 {
					if sel, isSel := parents[len(parents)-1].(*ast.SelectorExpr); isSel &&
						sel.X == ast.Expr(x) && sel.Sel.Name == "startupSnapshot" {
						return true
					}
				}
				t.Errorf("%s 把 receiver 用在 a.startupSnapshot() 以外的位置（%s）——"+
					"傳給函式／介面／泛型或建立 alias 都會讓 *App 逃出這一層形狀",
					name, fset.Position(x.Pos()).String())
			case *ast.SelectorExpr:
				// 規則 1／3：對 App 的**任何**選取（欄位讀取、method value、
				// method call）都必須是那唯一的 a.startupSnapshot() 呼叫。
				//
				// 用「選取」而不是「呼叫」當判準是刻意的：`f := a.probe` 是
				// method value，沒有 CallExpr，只看呼叫會整條漏掉
				// （reviewer 2026-08-20）。
				sel := info.Selections[x]
				if sel == nil || !receiverIsApp(sel.Recv(), appType) {
					return true
				}
				appSelectors++
				ok := x.Sel.Name == "startupSnapshot" && sel.Kind() == types.MethodVal &&
					isCalleePosition(x, parents)
				if !ok {
					t.Errorf("%s 只能有一個對 App 的存取（直接呼叫 a.startupSnapshot()），實得 %s（%s）",
						name, x.Sel.Name, fset.Position(x.Pos()).String())
				}
			case *ast.CallExpr:
				// 規則 2：其餘呼叫只能是不碰 App 的 package function。
				id, isIdent := x.Fun.(*ast.Ident)
				if !isIdent {
					return true
				}
				if fn, _ := info.Uses[id].(*types.Func); fn != nil && signatureTouchesApp(fn, appType) {
					t.Errorf("%s 呼叫了會碰 App 的 package function %s（%s）",
						name, id.Name, fset.Position(id.Pos()).String())
				}
			}
			return true
		})
		if appSelectors != 1 {
			t.Errorf("%s 對 App 的存取必須恰好一次（startupSnapshot），實得 %d 次", name, appSelectors)
		}
	}
}

// receiverObject：FuncDecl 的具名 receiver 物件（匿名或無 receiver 回 nil）。
func receiverObject(info *types.Info, fd *ast.FuncDecl) types.Object {
	if fd.Recv == nil || len(fd.Recv.List) == 0 || len(fd.Recv.List[0].Names) == 0 {
		return nil
	}
	return info.Defs[fd.Recv.List[0].Names[0]]
}

// receiverIsApp：選取的 receiver 型別是不是 App／*App。
func receiverIsApp(recv types.Type, appType *types.Named) bool {
	t := types.Unalias(recv)
	if p, isPtr := t.(*types.Pointer); isPtr {
		t = types.Unalias(p.Elem())
	}
	named, isNamed := t.(*types.Named)
	return isNamed && named.Obj() == appType.Obj()
}

// isCalleePosition：這個 selector 是不是某個 CallExpr 的 Fun（＝被直接呼叫，
// 不是被當成值取用）。
func isCalleePosition(sel *ast.SelectorExpr, parents []ast.Node) bool {
	if len(parents) == 0 {
		return false
	}
	call, isCall := parents[len(parents)-1].(*ast.CallExpr)
	return isCall && call.Fun == ast.Expr(sel)
}

// signatureTouchesApp：函式的 receiver 或任一參數是否是 App／*App。
func signatureTouchesApp(fn *types.Func, appType *types.Named) bool {
	sig, isSig := fn.Type().(*types.Signature)
	if !isSig {
		return false
	}
	isApp := func(t types.Type) bool {
		t = types.Unalias(t)
		if p, isPtr := t.(*types.Pointer); isPtr {
			t = types.Unalias(p.Elem())
		}
		named, isNamed := t.(*types.Named)
		return isNamed && named.Obj() == appType.Obj()
	}
	if sig.Recv() != nil && isApp(sig.Recv().Type()) {
		return true
	}
	for i := range sig.Params().Len() {
		if isApp(sig.Params().At(i).Type()) {
			return true
		}
	}
	return false
}

// startupGuardedFields：startupMu 保護的欄位。startupSnapshot 只能碰這些，寫入端
// 也必須持同一把鎖。
var startupGuardedFields = []string{
	"startupErr", "startupBlockers",
	"toolsDirPath", "toolsSource", "nodePath",
	"workspaceSnap", "workspaceSrcSnap",
}

// TestStartupSnapshotHoldsItsContract
//
// CLIInfo 的形狀把「安全」全部押在 startupSnapshot 上，所以那個函式本身要有契約
// （reviewer 2026-08-20：在它裡面加一個未上鎖的 a.replayIndex 讀取，形狀測試與
// 併發測試都照樣通過）。
//
// 四條，各自可獨立打紅：
//   - 只能讀 startupGuardedFields（外加 startupMu 本身）。
//   - 進場即 Lock、且以 defer Unlock 成對。
//   - 不得呼叫任何 App method，也不得在鎖內做 I/O／外部指令。
//   - 那些欄位的**寫入端**也必須持同一把鎖。
func TestStartupSnapshotHoldsItsContract(t *testing.T) {
	fset, files, info, pkg := typeCheckProductionPackage(t)
	appType := namedType(t, pkg, "App")
	appStruct, isStruct := appType.Underlying().(*types.Struct)
	if !isStruct {
		t.Fatal("App 不是 struct（守門前提不成立）")
	}
	guarded := map[*types.Var]bool{}
	for _, name := range append([]string{"startupMu"}, startupGuardedFields...) {
		found := false
		for i := range appStruct.NumFields() {
			if appStruct.Field(i).Name() == name {
				guarded[appStruct.Field(i)] = true
				found = true
			}
		}
		if !found {
			t.Fatalf("App 上沒有欄位 %q：契約已與實作脫節", name)
		}
	}
	methods := appMethodsByName(files, info, appType)
	fd := methods["startupSnapshot"]
	if fd == nil {
		t.Fatal("找不到 startupSnapshot")
	}

	// (2) 進場即 Lock ＋ defer Unlock。
	if len(fd.Body.List) < 2 {
		t.Fatal("startupSnapshot 必須以 Lock ＋ defer Unlock 開頭")
	}
	if !isLockCall(fd.Body.List[0], "startupMu", "Lock") {
		t.Error("startupSnapshot 第一個敘述必須是 a.startupMu.Lock()")
	}
	def, isDefer := fd.Body.List[1].(*ast.DeferStmt)
	if !isDefer || !isLockCallExpr(def.Call, "startupMu", "Unlock") {
		t.Error("startupSnapshot 第二個敘述必須是 defer a.startupMu.Unlock()")
	}

	// (1)(3) 欄位與呼叫。
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			sel, isSel := x.Fun.(*ast.SelectorExpr)
			if !isSel {
				return true
			}
			if fn, _ := info.Uses[sel.Sel].(*types.Func); fn != nil && isAppMethod(fn, appType) {
				t.Errorf("startupSnapshot 不得呼叫 App method %s（%s）——鎖內的每一步都要是純複製",
					sel.Sel.Name, fset.Position(sel.Pos()).String())
			}
		case *ast.SelectorExpr:
			s := info.Selections[x]
			if s == nil || s.Kind() != types.FieldVal || !receiverIsApp(s.Recv(), appType) {
				return true
			}
			if v, isVar := s.Obj().(*types.Var); !isVar || !guarded[v] {
				t.Errorf("startupSnapshot 讀到未受 startupMu 保護的欄位 %s（%s）",
					x.Sel.Name, fset.Position(x.Pos()).String())
			}
		}
		return true
	})

	// (4) 寫入端也必須持同一把鎖。
	for _, f := range files {
		for _, decl := range f.Decls {
			wfd, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || wfd.Body == nil {
				continue
			}
			locked := false
			ast.Inspect(wfd.Body, func(n ast.Node) bool {
				if call, isCall := n.(*ast.CallExpr); isCall && isLockCallExpr(call, "startupMu", "Lock") {
					locked = true
				}
				as, isAssign := n.(*ast.AssignStmt)
				if !isAssign {
					return true
				}
				for _, lhs := range as.Lhs {
					sel, isSel := lhs.(*ast.SelectorExpr)
					if !isSel {
						continue
					}
					s := info.Selections[sel]
					if s == nil || s.Kind() != types.FieldVal || !receiverIsApp(s.Recv(), appType) {
						continue
					}
					v, isVar := s.Obj().(*types.Var)
					if !isVar || !guarded[v] || v.Name() == "startupMu" {
						continue
					}
					if !locked {
						t.Errorf("%s 在未持有 startupMu 的情況下寫入 %s（%s）",
							wfd.Name.Name, sel.Sel.Name, fset.Position(sel.Pos()).String())
					}
				}
				return true
			})
		}
	}
}

// isLockCall／isLockCallExpr：`a.<mu>.<Lock|Unlock>()` 的形狀判定。
func isLockCall(stmt ast.Stmt, mu, method string) bool {
	es, isExpr := stmt.(*ast.ExprStmt)
	if !isExpr {
		return false
	}
	call, isCall := es.X.(*ast.CallExpr)
	return isCall && isLockCallExpr(call, mu, method)
}

func isLockCallExpr(call *ast.CallExpr, mu, method string) bool {
	if call == nil {
		return false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel || sel.Sel.Name != method {
		return false
	}
	inner, isInner := sel.X.(*ast.SelectorExpr)
	return isInner && inner.Sel.Name == mu
}
