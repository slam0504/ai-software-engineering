package main

import (
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"
)

// ---- Wails 綁定面的結構守門（reviewer 2026-08-19 兩輪 P1）----
//
// 這條守門被打穿兩次，每次都是「判準比它宣稱的弱」：
//
//	第一版：手寫 13 個 binding 的清單。漏了 RegisterMutation（實測 lease 釋放後
//	        仍把 evidence.jsonl 從 0 寫到 209 bytes）等 9 個，而且清單無法在
//	        新增 binding 時失敗。
//	第二版：改成「呼叫圖可達 state 欄位就必須呼叫 beginStateTxn」。只確認
//	        **出現呼叫**，於是：參數驗證排在交易之前（blocked 狀態回 unknown
//	        kind，蓋掉真正原因）、拿掉 defer endAppTxn 照樣通過、把 state 存取
//	        搬到 package helper 再以 function value 呼叫就從 22 個靜默變 21 個、
//	        新增一個直接寫 filepath.Join(a.stateDir, "gate.jsonl") 的 method 也
//	        不會被發現。
//
// 第三版改成**兩個方向一起守**，不再依賴可達性分析的完整性：
//
//	(1) 形狀：每個 exported method 要嘛是固定形狀的薄包裝（開交易 → defer 收尾
//	    → 呼叫 unexported 實作，body 恰好三個敘述），要嘛列在 statelessBindings
//	    白名單裡。**預設是必須守門**，例外要有人具名決定。
//	(2) 交叉稽核：呼叫圖可達性反過來查白名單——白名單裡的 method 若碰得到 state
//	    欄位，直接紅。可達性在這裡是輔助證據，不是唯一判準。
//
// 於是那四個繞過方式都被涵蓋：參數驗證進不了薄包裝、defer 少一行形狀就不對、
// 新增的 exported method 不在白名單就必須是薄包裝（不論它怎麼碰 state）。

// statelessBindings：**不碰 gate／escalation／evidence 狀態**的 exported method。
//
// 每一筆都是明確的決定：這個 binding 不需要 state 交易。新增 exported method 時
// 若不加進來，守門會要求它是薄包裝——這正是預期的預設值（fail closed）。
var statelessBindings = map[string]string{
	"CLIInfo":                    "只回報 CLI／node 解析結果，不碰 journal",
	"CreateSession":              "session registry 交易，自己走 beginAppTxn／crToken",
	"ListSessions":               "讀 session registry 快照",
	"RemoveSession":              "session 生命週期，走 crToken ＋ beginAppTxn",
	"PaneLayout":                 "UI 版面（pane pins）",
	"SetPaneLayout":              "UI 版面（pane pins）",
	"StartSession":               "provider 啟動，走 beginAppTxn",
	"EndSession":                 "provider 收尾，走 beginAppTxn",
	"NewSession":                 "provider 重啟，走 beginAppTxn",
	"SendMessage":                "turn 輸入",
	"AuthStatus":                 "codex 登入狀態",
	"StartLogin":                 "codex 登入",
	"CancelLogin":                "codex 登入",
	"Logout":                     "codex 登出",
	"ReadDiagram":                "讀 workspace 圖檔",
	"ReadWorkspaceFile":          "讀 workspace 檔案",
	"SpecList":                   "讀 spec 樹（檔案，不經 gate journal）",
	"SpecRead":                   "讀 spec 檔",
	"SpecWrite":                  "寫 spec 檔（workspace 檔案，非 app state）",
	"PlanList":                   "讀 plan 樹",
	"PlanRead":                   "讀 plan 檔",
	"PlanWrite":                  "寫 plan 檔（workspace 檔案，非 app state）",
	"SpecAssist":                 "一次性草擬，走 beginAppTxn；不碰 gate／escalation journal",
	"LoadTurnsBefore":            "replay index 讀取",
	"ListWorkspace":              "讀 workspace 檔案樹",
	"RecoverCodexRecording":      "錄流 generation 修復（wire log，非 journal）",
	"ResolveApproval":            "approval broker 決議",
	"RestartCodexServerRecorded": "codex server 受控替換，走 beginAppTxn",
	"RestoreViews":               "restore store 的 view 還原",
	"TerminateSession":           "session 收尾，走 crToken／beginAppTxn",
}

// stateFieldNames：App 上代表「gate／escalation／evidence 狀態」的欄位（交叉稽核
// 用）。碰得到這些欄位的函式即視為 state sink。
//
// 含惰性初始化的 once／err 與 evidence 的 in-flight registry——reviewer 2026-08-19
// 指出漏了它們。**刻意不含 phase／shutMu**：那是交易閘自己的狀態，納入的話每個
// 已守門的 binding 都會「可達 state」，交叉稽核退化成恆真。
var stateFieldNames = []string{
	"gateSvc", "gateJournal", "gateReg", "gateOnce", "gateInitErr",
	"specRepo", "planRepo",
	"escSvc", "escJournal", "escOnce", "escInitErr",
	"evidenceJournal", "evidenceCASDir", "evidenceRegistryPath",
	"evidenceActive", "evidenceMu",
}

// TestEveryStateBindingIsAThinGatedWrapper
//
// 正題斷言：每個 *App exported method 要嘛在 statelessBindings 裡，要嘛 body 是
// 恰好三個敘述的薄包裝：
//
//	if err := a.beginStateTxn(); err != nil { return … }
//	defer a.endAppTxn()
//	return a.<unexported>(…)
func TestEveryStateBindingIsAThinGatedWrapper(t *testing.T) {
	_, files, info, pkg := typeCheckProductionPackage(t)
	appType := namedType(t, pkg, "App")

	var offenders, gated []string
	seen := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fd.Body == nil || fd.Recv == nil || !fd.Name.IsExported() {
				continue
			}
			fn, _ := info.Defs[fd.Name].(*types.Func)
			if fn == nil || !isAppMethod(fn, appType) {
				continue
			}
			name := fd.Name.Name
			seen[name] = true
			if _, stateless := statelessBindings[name]; stateless {
				continue
			}
			if why := thinWrapperViolation(fd); why != "" {
				offenders = append(offenders, name+"："+why)
				continue
			}
			gated = append(gated, name)
		}
	}
	sort.Strings(offenders)
	sort.Strings(gated)
	if len(offenders) != 0 {
		t.Fatalf("以下 Wails binding 既不在 statelessBindings 白名單、也不是固定形狀的薄包裝：\n  %s\n"+
			"（薄包裝＝開交易 → defer a.endAppTxn() → return a.<unexported>(…)，body 恰好三個敘述）",
			strings.Join(offenders, "\n  "))
	}
	if len(gated) == 0 {
		t.Fatal("沒有任何 binding 被判定為 state binding：守門失效")
	}
	// 白名單不得列出不存在的 method（改名後留下的殘骸會讓守門靜默變寬）。
	for name := range statelessBindings {
		if !seen[name] {
			t.Errorf("statelessBindings 列了不存在的 method %q：請同步清理", name)
		}
	}
	t.Logf("state binding %d 個：%s", len(gated), strings.Join(gated, ", "))
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

// thinWrapperViolation：檢查薄包裝形狀，回傳違規說明（空字串＝合格）。
//
// 三個敘述**逐一**驗，不是「body 裡有出現 beginStateTxn」——後者讓「參數先驗證」
// 與「少了 defer」都能通過（reviewer 2026-08-19）。
func thinWrapperViolation(fd *ast.FuncDecl) string {
	body := fd.Body.List
	if len(body) != 3 {
		return "body 有 " + itoa(len(body)) + " 個敘述，薄包裝必須恰好三個（多出來的敘述會在交易之外執行）"
	}
	// 1) if err := a.beginStateTxn(); err != nil { return … }
	ifs, ok := body[0].(*ast.IfStmt)
	if !ok || !callsAppMethod(initCall(ifs.Init), "beginStateTxn") {
		return "第一個敘述必須是 `if err := a.beginStateTxn(); err != nil { … }`"
	}
	if ifs.Body == nil || len(ifs.Body.List) != 1 {
		return "beginStateTxn 失敗時必須直接返回，不得做其他事"
	}
	if _, isReturn := ifs.Body.List[0].(*ast.ReturnStmt); !isReturn {
		return "beginStateTxn 失敗時必須 return"
	}
	// 2) defer a.endAppTxn()
	def, ok := body[1].(*ast.DeferStmt)
	if !ok || !callsAppMethod(def.Call, "endAppTxn") {
		return "第二個敘述必須是 `defer a.endAppTxn()`（少了它 shutdown 會永遠等不到 inflight 歸零）"
	}
	// 3) return a.<unexported>(…)
	ret, ok := body[2].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return "第三個敘述必須是 `return a.<unexported>(…)`"
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		return "第三個敘述必須直接回傳實作的呼叫結果"
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "實作必須是 App 上的方法"
	}
	if recv, isIdent := sel.X.(*ast.Ident); !isIdent || recv.Name != "a" {
		return "實作必須以 receiver 呼叫（a.<unexported>）"
	}
	if sel.Sel.IsExported() {
		return "實作必須是 unexported method（exported 的話它自己又是一個沒守門的 binding）"
	}
	return ""
}

// initCall：`if err := a.f(); …` 的那個呼叫（形狀不符回 nil）。
func initCall(init ast.Stmt) *ast.CallExpr {
	as, ok := init.(*ast.AssignStmt)
	if !ok || len(as.Rhs) != 1 {
		return nil
	}
	call, _ := as.Rhs[0].(*ast.CallExpr)
	return call
}

func callsAppMethod(call *ast.CallExpr, name string) bool {
	if call == nil {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
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

// TestStatelessBindingsDoNotReachState
//
// 交叉稽核：白名單宣稱「不碰 state」，用呼叫圖驗證這個宣稱。
//
// 這是**輔助證據**不是唯一判準——可達性分析對 function value、間接呼叫、直接
// 用 filepath.Join(a.stateDir, …) 開檔都會低估（reviewer 2026-08-19）。真正兜底
// 的是上一條的「預設必須守門」。這裡的價值在反方向：白名單被隨手加進一個其實
// 會碰 state 的 method 時，這條會紅。
func TestStatelessBindingsDoNotReachState(t *testing.T) {
	_, files, info, pkg := typeCheckProductionPackage(t)
	appType := namedType(t, pkg, "App")
	appStruct, ok := appType.Underlying().(*types.Struct)
	if !ok {
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
	touches := map[*types.Func]bool{}
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
				if callee, _ := info.Uses[id].(*types.Func); callee != nil {
					calls[fn] = append(calls[fn], callee)
				}
			case *ast.SelectorExpr:
				if sel := info.Selections[x]; sel != nil {
					if v, isVar := sel.Obj().(*types.Var); isVar && stateFields[v] {
						touches[fn] = true
					}
				}
			}
			return true
		})
	}
	// 定點迭代：對呼叫圖的環天然正確（遞迴 DFS 會在環上回傳假的 false）。
	reaches := map[*types.Func]bool{}
	for fn := range touches {
		reaches[fn] = true
	}
	for changed := true; changed; {
		changed = false
		for fn := range bodies {
			if reaches[fn] {
				continue
			}
			for _, callee := range calls[fn] {
				if reaches[callee] {
					reaches[fn] = true
					changed = true
					break
				}
			}
		}
	}

	var wrong []string
	for fn, fd := range bodies {
		if fd.Recv == nil || !fd.Name.IsExported() || !isAppMethod(fn, appType) {
			continue
		}
		if _, stateless := statelessBindings[fd.Name.Name]; !stateless {
			continue
		}
		if reaches[fn] {
			wrong = append(wrong, fd.Name.Name)
		}
	}
	sort.Strings(wrong)
	if len(wrong) != 0 {
		t.Fatalf("以下 binding 被列在 statelessBindings，但呼叫圖顯示它們碰得到 gate／escalation／evidence 狀態：\n  %s",
			strings.Join(wrong, "\n  "))
	}
}
