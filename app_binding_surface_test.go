package main

import (
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"
)

// ---- Wails 綁定面的結構守門（reviewer 2026-08-19 P1）----
//
// 前一輪用**手寫的 13 個 binding 清單**界定「哪些入口要進 state 交易」，複核直接
// 打穿：RegisterMutation、ValidateTestCommit、Spec／Plan／AnalysisBaseBump 的
// Preview／Confirm、PlanAssist 都不在清單上，而它們照樣開得了 gate journal、寫得了
// evidence.jsonl——實測 lease 釋放後仍能把 evidence.jsonl 從 0 寫到 209 bytes。
//
// 手寫清單的問題不是「這次漏了幾個」，是它**無法在新增 binding 時失敗**。所以改成
// 反向推導：
//
//	Wails 綁定 = *App 的所有 exported method
//	   ↓ 套用 package 內的呼叫圖（型別解析，不是名字比對）
//	能碰到 state（gate／escalation／evidence 的 service、journal、CAS 路徑）的
//	   ↓
//	必須在**自己的 body** 直接呼叫 beginStateTxn
//
// 新增一個會碰 state 的 binding 卻忘了接交易閘，這條就會紅，而且會指名是哪一個。

// stateFieldNames：App 上代表「gate／escalation／evidence 狀態」的欄位。碰得到
// 這些欄位的函式即視為 state sink（呼叫圖的終點）。
//
// 刻意用欄位而不是「有沒有呼叫某個 helper」：helper 會被改名、被拆、被繞過，
// 欄位才是狀態本身。新增這類狀態時要一併加進來——漏加的後果是守門變寬，所以
// 下面另外斷言這幾個名字都真的存在於 App 上。
var stateFieldNames = []string{
	"gateSvc", "gateJournal", "gateReg", "specRepo", "planRepo",
	"escSvc", "escJournal",
	"evidenceJournal", "evidenceCASDir", "evidenceRegistryPath",
}

// bindingGateExempt：允許不進交易閘的 exported method ＋ 理由。
//
// 每一筆都必須是「碰得到 state 欄位、但不構成 state 操作」的具體理由，不是
// 「還沒改到」。空的最好。
var bindingGateExempt = map[string]string{}

// TestEveryStateTouchingBindingEntersStateTxn
//
// 正題斷言：每一個能碰到 state 的 *App exported method 都在自己的 body 直接呼叫
// beginStateTxn。
func TestEveryStateTouchingBindingEntersStateTxn(t *testing.T) {
	fset, files, info, pkg := typeCheckProductionPackage(t)
	_ = fset

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
			t.Fatalf("App 上沒有欄位 %q：守門的 state 定義已經與實作脫節，請同步更新 stateFieldNames", name)
		}
	}

	// 1) package 內所有函式／方法的 body。
	bodies := map[*types.Func]*ast.FuncDecl{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fd.Body == nil {
				continue
			}
			if obj, _ := info.Defs[fd.Name].(*types.Func); obj != nil {
				bodies[obj] = fd
			}
		}
	}

	// 2) 每個函式的「直接呼叫」與「是否直接碰 state 欄位」。
	calls := map[*types.Func][]*types.Func{}
	touches := map[*types.Func]bool{}
	callsGate := map[*types.Func]bool{}
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
				if callee.Name() == "beginStateTxn" {
					callsGate[fn] = true
				}
				calls[fn] = append(calls[fn], callee)
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

	// 3) 可達性：定點迭代，從「自己碰 state」的函式往回擴散到呼叫者。
	//
	// 用定點而不是遞迴 DFS：這個 package 的呼叫圖有環（收尾路徑互相呼叫），
	// 遞迴版要嘛在環上回傳假的 false、要嘛快取到被污染的中間結果——兩種都會讓
	// 守門靜默變寬。定點迭代對環天然正確。
	reachesState := map[*types.Func]bool{}
	for fn := range touches {
		reachesState[fn] = true
	}
	for changed := true; changed; {
		changed = false
		for fn := range bodies {
			if reachesState[fn] {
				continue
			}
			for _, callee := range calls[fn] {
				if reachesState[callee] {
					reachesState[fn] = true
					changed = true
					break
				}
			}
		}
	}

	// 4) Wails 綁定面：*App 的 exported method。
	var offenders, covered []string
	for fn, fd := range bodies {
		if fd.Recv == nil || !fn.Exported() {
			continue
		}
		sig, _ := fn.Type().(*types.Signature)
		if sig == nil || sig.Recv() == nil {
			continue
		}
		recv := types.Unalias(sig.Recv().Type())
		if p, isPtr := recv.(*types.Pointer); isPtr {
			recv = types.Unalias(p.Elem())
		}
		if named, isNamed := recv.(*types.Named); !isNamed || named.Obj() != appType.Obj() {
			continue
		}
		if !reachesState[fn] {
			continue
		}
		if why, exempt := bindingGateExempt[fn.Name()]; exempt {
			covered = append(covered, fn.Name()+"（豁免："+why+"）")
			continue
		}
		if callsGate[fn] {
			covered = append(covered, fn.Name())
			continue
		}
		offenders = append(offenders, fn.Name())
	}
	sort.Strings(offenders)
	sort.Strings(covered)
	if len(offenders) != 0 {
		t.Fatalf("以下 Wails binding 碰得到 gate／escalation／evidence 狀態，卻沒有進 beginStateTxn"+
			"——它們在 lease 釋放之後仍寫得進磁碟：\n  %s\n（已接上的：%s）",
			strings.Join(offenders, "\n  "), strings.Join(covered, ", "))
	}
	// 反向：守門必須真的量到東西。全部都「碰不到 state」代表可達性分析壞了。
	if len(covered) == 0 {
		t.Fatal("沒有任何 binding 被判定為碰得到 state：可達性分析失效（守門變成恆真）")
	}
	t.Logf("state binding %d 個全部已接上交易閘：%s", len(covered), strings.Join(covered, ", "))
}
