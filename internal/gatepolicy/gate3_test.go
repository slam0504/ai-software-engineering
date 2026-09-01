package gatepolicy

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/gate"
)

func gate3Bindings() []gate.Binding {
	d := "sha256:" + strings.Repeat("ab", 32)
	oid := "git:sha1:" + strings.Repeat("a", 40)
	return []gate.Binding{
		{Kind: "task_run", Ref: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Digest: d},
		{Kind: "promotion_head", Ref: oid, Digest: oid},
		{Kind: "main_base", Ref: oid, Digest: oid},
		{Kind: "oracle_surface", Digest: d},
		{Kind: "required_check_manifest", Digest: d},
		{Kind: "review_evidence_provenance", Digest: d},
	}
}

func TestGate3ValidateRequestBindingShapes(t *testing.T) {
	p := NewGate3Policy(Gate3Deps{})
	ok := gate.GateRequest{Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Bindings: gate3Bindings()}
	if err := p.ValidateRequest(ok); err != nil {
		t.Fatal(err)
	}
	badSubject := ok
	badSubject.Subject = "plan:x"
	if err := p.ValidateRequest(badSubject); err == nil {
		t.Fatal("subject 形狀錯誤應拒絕")
	}
	crossed := ok
	crossed.Subject = "taskrun:01BX5ZZKBKACTAV9WEVGEMMVRZ" // 形狀合法但 ≠ task_run binding
	if err := p.ValidateRequest(crossed); err == nil {
		t.Fatal("subject 與 task_run binding 不一致應拒絕（rev2 P1）")
	}
	dup := ok
	dup.Bindings = append(append([]gate.Binding{}, ok.Bindings...), ok.Bindings[0])
	if err := p.ValidateRequest(dup); err == nil {
		t.Fatal("binding 重複應拒絕")
	}
	unknown := ok
	unknown.Bindings = append(append([]gate.Binding{}, ok.Bindings...),
		gate.Binding{Kind: "unknown_kind", Digest: "sha256:" + strings.Repeat("cd", 32)})
	if err := p.ValidateRequest(unknown); err == nil {
		t.Fatal("未知 binding（第 7 筆）應拒絕")
	}
	badTaskRunDigest := ok
	badTaskRunDigest.Bindings = append([]gate.Binding{}, ok.Bindings...)
	badTaskRunDigest.Bindings[0] = gate.Binding{Kind: "task_run", Ref: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Digest: "not-sha256"}
	if err := p.ValidateRequest(badTaskRunDigest); err == nil {
		t.Fatal("task_run digest 非 sha256 形狀應拒絕")
	}
	badPromotionHeadDigest := ok
	badPromotionHeadDigest.Bindings = append([]gate.Binding{}, ok.Bindings...)
	badPromotionHeadDigest.Bindings[1] = gate.Binding{
		Kind:   "promotion_head",
		Ref:    "git:sha1:" + strings.Repeat("a", 40),
		Digest: "sha256:" + strings.Repeat("ab", 32), // gitOID 欄位誤填 sha256 形狀
	}
	if err := p.ValidateRequest(badPromotionHeadDigest); err == nil {
		t.Fatal("promotion_head digest 非 gitOID 形狀應拒絕")
	}
	badTaskRunRef := ok
	badTaskRunRef.Bindings = append([]gate.Binding{}, ok.Bindings...)
	badTaskRunRef.Bindings[0] = gate.Binding{Kind: "task_run", Ref: "not-a-taskrun-ref", Digest: "sha256:" + strings.Repeat("ab", 32)}
	if err := p.ValidateRequest(badTaskRunRef); err == nil {
		t.Fatal("task_run ref 形狀不符 taskrun:<ULID> 應拒絕（與 subject 形狀檢查各自獨立）")
	}

	// Task 6 施工依據補測（P2，design re-review 發現）：reTaskRunRef 用
	// Crockford base32（排除 I/L/O/U，與 contract.NewULID 產生器一致，
	// internal/contract/envelope.go:10）。上面兩案例只測「前綴整個錯」與
	// 「形狀整個錯」，沒有「前綴正確、26 碼、僅字元集違規」的值——若 regex
	// 被放寬成 [0-9A-Z]{26}（即 tca.go:65 reApprovalRef 現行形狀），既有
	// 測試一個都不會紅。reTaskRunRef 同時用於 subject 與 task_run binding
	// 的 ref，補兩條獨立案例。

	// 案例 A：subject 與 task_run binding 的 Ref 使用同一個 26 碼、前綴
	// 正確，但含 Crockford 排除字母（I）的值——兩者相等使交叉比對也不會
	// 擋，只有 subject 形狀檢查能抓到 regex 放寬的 mutation（ValidateRequest
	// 的 subject 檢查在最前面）。
	excludedCharSubjectAndRef := ok
	excludedCharSubjectAndRef.Subject = "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAI" // 'I' 屬 Crockford 排除字元
	excludedCharSubjectAndRef.Bindings = append([]gate.Binding{}, ok.Bindings...)
	excludedCharSubjectAndRef.Bindings[0] = gate.Binding{Kind: "task_run", Ref: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAI", Digest: "sha256:" + strings.Repeat("ab", 32)}
	if err := p.ValidateRequest(excludedCharSubjectAndRef); err == nil || !strings.Contains(err.Error(), "subject 形狀必須為") {
		t.Fatalf("subject 含 Crockford 排除字母應拒絕且命中 subject 形狀訊息：%v", err)
	}

	// 案例 B：subject 合法、task_run binding 的 Ref 含排除字母（I）——
	// 獨立守住 binding ref 格式檢查。斷言必須命中「ref 形狀不符」而非僅
	// err != nil：若移除 refRe 檢查，subject 與 ref 不相等仍會被最後的
	// 交叉比對擋下（訊息為「不一致」），只判斷 err != nil 會讓這個 mutation
	// 誤通過。
	excludedCharTaskRunRefOnly := ok
	excludedCharTaskRunRefOnly.Bindings = append([]gate.Binding{}, ok.Bindings...)
	excludedCharTaskRunRefOnly.Bindings[0] = gate.Binding{Kind: "task_run", Ref: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAI", Digest: "sha256:" + strings.Repeat("ab", 32)}
	if err := p.ValidateRequest(excludedCharTaskRunRefOnly); err == nil || !strings.Contains(err.Error(), "ref 形狀不符") {
		t.Fatalf("task_run ref 含 Crockford 排除字母應拒絕且命中 ref 形狀訊息：%v", err)
	}

	// 六種 binding 缺一測一（owner 明訂：成本低且避免代表案例漏掉特殊 ref，
	// 例如 task_run 有 refRe、其餘沒有）。
	for _, kind := range []string{
		"task_run", "promotion_head", "main_base",
		"oracle_surface", "required_check_manifest", "review_evidence_provenance",
	} {
		kind := kind
		t.Run("missing_"+kind, func(t *testing.T) {
			req := ok
			var bindings []gate.Binding
			for _, b := range ok.Bindings {
				if b.Kind != kind {
					bindings = append(bindings, b)
				}
			}
			req.Bindings = bindings
			if err := p.ValidateRequest(req); err == nil {
				t.Fatalf("缺 binding %q 應拒絕", kind)
			}
		})
	}
}

func TestGate3SupersessionKey(t *testing.T) {
	// SupersessionKey 回歸 repo 預設慣例（gate1／gate2／TCA／stubPolicy 與
	// GatePolicy interface doc 皆為 gateName+"|"+subject）——Task 6
	// implementation preflight erratum①，gate3 的 subject taskrun:<ULID>
	// 不可能含 "|"，無另用 "\x00" 分隔的理由。
	p := NewGate3Policy(Gate3Deps{})
	got := p.SupersessionKey("gate3_promotion", "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV")
	want := "gate3_promotion|taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestGate3BuildDecisionApprovedRunsAllChecksFailClosed(t *testing.T) {
	var order []string
	deps := Gate3Deps{
		VerifyTaskRun:    func(id, dg string) error { order = append(order, "taskrun"); return nil },
		VerifyForge:      func(h, b, d string) error { order = append(order, "forge"); return nil },
		VerifyProvenance: func(id, d, h string) error { order = append(order, "provenance"); return nil },
	}
	p := NewGate3Policy(deps)
	req := gate.GateRequest{Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Bindings: gate3Bindings()}
	if _, err := p.BuildDecision(req, "approved", gate.DecisionInput{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "taskrun,forge,provenance" {
		t.Fatalf("重驗順序應為 §5.3 序：%v", order)
	}
	// 任一 deps 失敗 → fail closed
	deps.VerifyForge = func(h, b, d string) error { return errors.New("head moved") }
	p = NewGate3Policy(deps)
	if _, err := p.BuildDecision(req, "approved", gate.DecisionInput{}); err == nil {
		t.Fatal("forge 重驗失敗應 fail closed")
	}
}

func TestGate3BuildDecisionEachDepErrorStopsDownstream(t *testing.T) {
	// 三個 deps error 各自獨立案例，並斷言失敗後下游 deps 未執行（spy）。
	req := gate.GateRequest{Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Bindings: gate3Bindings()}
	sentinel := errors.New("boom")

	t.Run("VerifyTaskRun_error", func(t *testing.T) {
		var spy []string
		p := NewGate3Policy(Gate3Deps{
			VerifyTaskRun:    func(id, dg string) error { spy = append(spy, "taskrun"); return sentinel },
			VerifyForge:      func(h, b, d string) error { spy = append(spy, "forge"); return nil },
			VerifyProvenance: func(id, d, h string) error { spy = append(spy, "provenance"); return nil },
		})
		if _, err := p.BuildDecision(req, "approved", gate.DecisionInput{}); err == nil {
			t.Fatal("taskrun 重驗失敗應 fail closed")
		}
		if strings.Join(spy, ",") != "taskrun" {
			t.Fatalf("taskrun 失敗後不得執行 forge／provenance，實際=%v", spy)
		}
	})

	t.Run("VerifyForge_error", func(t *testing.T) {
		var spy []string
		p := NewGate3Policy(Gate3Deps{
			VerifyTaskRun:    func(id, dg string) error { spy = append(spy, "taskrun"); return nil },
			VerifyForge:      func(h, b, d string) error { spy = append(spy, "forge"); return sentinel },
			VerifyProvenance: func(id, d, h string) error { spy = append(spy, "provenance"); return nil },
		})
		if _, err := p.BuildDecision(req, "approved", gate.DecisionInput{}); err == nil {
			t.Fatal("forge 重驗失敗應 fail closed")
		}
		if strings.Join(spy, ",") != "taskrun,forge" {
			t.Fatalf("forge 失敗後不得執行 provenance，實際=%v", spy)
		}
	})

	t.Run("VerifyProvenance_error", func(t *testing.T) {
		var spy []string
		p := NewGate3Policy(Gate3Deps{
			VerifyTaskRun:    func(id, dg string) error { spy = append(spy, "taskrun"); return nil },
			VerifyForge:      func(h, b, d string) error { spy = append(spy, "forge"); return nil },
			VerifyProvenance: func(id, d, h string) error { spy = append(spy, "provenance"); return sentinel },
		})
		if _, err := p.BuildDecision(req, "approved", gate.DecisionInput{}); err == nil {
			t.Fatal("provenance 重驗失敗應 fail closed")
		}
		if strings.Join(spy, ",") != "taskrun,forge,provenance" {
			t.Fatalf("provenance 是最後一步，前兩步應已執行，實際=%v", spy)
		}
	})
}

func TestGate3BuildDecisionEachNilDepFailsClosed(t *testing.T) {
	// 三個 nil deps 各自獨立案例；nil 檢查在任何 deps 呼叫前執行，
	// 斷言失敗後下游 deps 未執行（spy 應為空）。
	req := gate.GateRequest{Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Bindings: gate3Bindings()}

	cases := []struct {
		name string
		deps func(spy *[]string) Gate3Deps
	}{
		{name: "VerifyTaskRun_nil", deps: func(spy *[]string) Gate3Deps {
			return Gate3Deps{
				VerifyForge:      func(h, b, d string) error { *spy = append(*spy, "forge"); return nil },
				VerifyProvenance: func(id, d, h string) error { *spy = append(*spy, "provenance"); return nil },
			}
		}},
		{name: "VerifyForge_nil", deps: func(spy *[]string) Gate3Deps {
			return Gate3Deps{
				VerifyTaskRun:    func(id, dg string) error { *spy = append(*spy, "taskrun"); return nil },
				VerifyProvenance: func(id, d, h string) error { *spy = append(*spy, "provenance"); return nil },
			}
		}},
		{name: "VerifyProvenance_nil", deps: func(spy *[]string) Gate3Deps {
			return Gate3Deps{
				VerifyTaskRun: func(id, dg string) error { *spy = append(*spy, "taskrun"); return nil },
				VerifyForge:   func(h, b, d string) error { *spy = append(*spy, "forge"); return nil },
			}
		}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			var spy []string
			p := NewGate3Policy(c.deps(&spy))
			if _, err := p.BuildDecision(req, "approved", gate.DecisionInput{}); err == nil ||
				!strings.Contains(err.Error(), "not wired") {
				t.Fatalf("%s 應 fail closed 且錯誤具名：%v", c.name, err)
			}
			if len(spy) != 0 {
				t.Fatalf("%s：nil 檢查應在任何 deps 呼叫前擋下，實際執行=%v", c.name, spy)
			}
		})
	}
}

func TestGate3BuildDecisionRejectsNonEmptyRiskSelections(t *testing.T) {
	// Task 6 implementation preflight erratum②：Gate 3 是無 risk policy，
	// 應對齊 gate1／TCA 與 DecisionInput 契約——approved／rejected 兩條
	// 路徑都必須拒絕非空 RiskSelections（gate2 才是「approved 消費、
	// rejected 禁止」的例外）。
	p := NewGate3Policy(Gate3Deps{
		VerifyTaskRun:    func(id, dg string) error { return nil },
		VerifyForge:      func(h, b, d string) error { return nil },
		VerifyProvenance: func(id, d, h string) error { return nil },
	})
	req := gate.GateRequest{Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Bindings: gate3Bindings()}
	risk := gate.DecisionInput{RiskSelections: []gate.RiskSelection{{TaskID: "t1", SelectedRiskTier: "high"}}}

	if _, err := p.BuildDecision(req, "approved", risk); err == nil {
		t.Fatal("approved 分支非空 RiskSelections 應拒絕")
	}
	if _, err := p.BuildDecision(req, "rejected", risk); err == nil {
		t.Fatal("rejected 分支非空 RiskSelections 應拒絕")
	}
}

func TestGate3BuildDecisionRejectedSkipsReverify(t *testing.T) {
	called := false
	p := NewGate3Policy(Gate3Deps{VerifyTaskRun: func(id, dg string) error { called = true; return nil }})
	req := gate.GateRequest{Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Bindings: gate3Bindings()}
	if _, err := p.BuildDecision(req, "rejected", gate.DecisionInput{}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("rejected 分支跳過重驗（B5 §5.3(6)）")
	}
}

func TestGate3BuildDecisionMismatchSentinelPropagation(t *testing.T) {
	req := gate.GateRequest{Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Bindings: gate3Bindings()}

	t.Run("wrapped_mismatch_is_recognizable", func(t *testing.T) {
		p := NewGate3Policy(Gate3Deps{
			VerifyTaskRun:    func(id, dg string) error { return fmt.Errorf("taskrun stale: %w", ErrGate3Mismatch) },
			VerifyForge:      func(h, b, d string) error { return nil },
			VerifyProvenance: func(id, d, h string) error { return nil },
		})
		_, err := p.BuildDecision(req, "approved", gate.DecisionInput{})
		if !errors.Is(err, ErrGate3Mismatch) {
			t.Fatalf("%%w 包裝的 ErrGate3Mismatch 必須可用 errors.Is 辨識：%v", err)
		}
	})

	t.Run("unwrapped_error_not_misjudged_as_mismatch", func(t *testing.T) {
		p := NewGate3Policy(Gate3Deps{
			VerifyTaskRun:    func(id, dg string) error { return errors.New("forge 讀取逾時") }, // transient，未包裝
			VerifyForge:      func(h, b, d string) error { return nil },
			VerifyProvenance: func(id, d, h string) error { return nil },
		})
		_, err := p.BuildDecision(req, "approved", gate.DecisionInput{})
		if errors.Is(err, ErrGate3Mismatch) {
			t.Fatal("未包裝的 transient error 不得被誤判為 ErrGate3Mismatch")
		}
	})
}

func TestGate3ReconcileBindingsPendingPseudoRecordEmpty(t *testing.T) {
	// rev7（plan gate 第六輪 P1）：pending pseudo-record 必須回空——
	// PrepareDecision 的前置檢查會把 cause 轉成未包裝一般錯誤並跳過
	// BuildDecision（service.go:107），pending 會繞過 expired 永久滯留。
	// Task 6 只證明這一點——完整的 PrepareDecision → ErrGate3Mismatch →
	// ExpirePending 鏈仍屬 Task 6b，不在此提前拉入。
	p := NewGate3Policy(Gate3Deps{})
	causes, err := p.ReconcileBindings(gate.ApprovalRecord{
		Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Decision: "", // pending pseudo-record 形狀
	})
	if err != nil || len(causes) != 0 {
		t.Fatalf("pending pseudo-record 必須回 (nil, nil)：causes=%v err=%v", causes, err)
	}
}

func TestGate3NilDepsFailClosed(t *testing.T) {
	// 鏡射實際 registry 註冊形狀（app.go ensureGate：
	// gatepolicy.NewGate3Policy(gatepolicy.Gate3Deps{})，全 deps 為零值）——
	// 註冊存在但 deps 未接線時不放行。
	p := NewGate3Policy(Gate3Deps{})
	req := gate.GateRequest{Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Bindings: gate3Bindings()}
	if _, err := p.BuildDecision(req, "approved", gate.DecisionInput{}); err == nil ||
		!strings.Contains(err.Error(), "not wired") {
		t.Fatal("deps 未接線應 fail closed 且錯誤具名")
	}
}
