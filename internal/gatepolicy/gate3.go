// gate3.go——B5 spec §5 的 Gate 3 policy 骨架（B6）：binding 形狀驗證與
// 決議時重驗「編排」在此凍結；重驗的三組實體檢查（TaskRun currentness、
// forge 現時狀態、provenance 重建）以 Gate3Deps 注入，C1a/C1b/C1c 接線。
// deps 未接線時 fail closed——註冊存在但不可能誤放行。
package gatepolicy

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/slam0504/sdlc-workbench/internal/gate"
)

var reTaskRunRef = regexp.MustCompile(`^taskrun:[0-9A-HJKMNP-TV-Z]{26}$`)

// ErrGate3Mismatch：重驗「已確認不符」的 sentinel（rev2——B5 §4.3 的
// mismatch／transient 區分）。deps 實作以 fmt.Errorf("…: %w", ErrGate3Mismatch)
// 標記**確認不符**（重驗完成且結果為不符→pending request 應轉終態）；
// 未包裝的 error＝transient（forge 讀取失敗等——無法決議、維持 pending，
// 不得轉終態）。BuildDecision 原樣傳遞（%w），呼叫端以 errors.Is 判斷。
var ErrGate3Mismatch = errors.New("gate3: 重驗確認不符")

type Gate3Deps struct {
	VerifyTaskRun    func(taskRunID, snapshotDigest string) error                    // §5.3(1)
	VerifyForge      func(promotionHead, mainBase, requiredCheckDigest string) error // §5.3(2)(3)(4)
	VerifyProvenance func(taskRunID, provenanceDigest, promotionHead string) error   // §5.3(5)
}

type gate3BindingReq struct {
	kind     string
	digestRe *regexp.Regexp
	refRe    *regexp.Regexp
}

var gate3BindingReqs = []gate3BindingReq{
	{kind: "task_run", digestRe: reSHA256, refRe: reTaskRunRef},
	{kind: "promotion_head", digestRe: reGitOID},
	{kind: "main_base", digestRe: reGitOID},
	{kind: "oracle_surface", digestRe: reSHA256},
	{kind: "required_check_manifest", digestRe: reSHA256},
	{kind: "review_evidence_provenance", digestRe: reSHA256},
}

type Gate3Policy struct{ deps Gate3Deps }

var _ gate.GatePolicy = (*Gate3Policy)(nil)

func NewGate3Policy(deps Gate3Deps) gate.GatePolicy { return &Gate3Policy{deps: deps} }

func (p *Gate3Policy) ValidateRequest(req gate.GateRequest) error {
	if !reTaskRunRef.MatchString(req.Subject) {
		return fmt.Errorf("gate3: subject 形狀必須為 taskrun:<ULID>，得 %q", req.Subject)
	}
	found := map[string]gate.Binding{}
	// rev2（plan gate P1）：subject 必須與 task_run binding 完全相等——否則
	// subject A 可綁 TaskRun B：重驗 B、supersession／completed 卻依 A 處理。
	// （相等檢查在 bindings 迴圈後、以 found["task_run"] 執行，見下。）
	for _, b := range req.Bindings {
		if _, dup := found[b.Kind]; dup {
			return fmt.Errorf("gate3: binding %q 重複", b.Kind)
		}
		found[b.Kind] = b
	}
	for _, r := range gate3BindingReqs {
		b, ok := found[r.kind]
		if !ok {
			return fmt.Errorf("gate3: 缺 binding %q", r.kind)
		}
		if r.digestRe != nil && !r.digestRe.MatchString(b.Digest) {
			return fmt.Errorf("gate3: binding %q digest 形狀不符：%q", r.kind, b.Digest)
		}
		if r.refRe != nil && !r.refRe.MatchString(b.Ref) {
			return fmt.Errorf("gate3: binding %q ref 形狀不符：%q", r.kind, b.Ref)
		}
	}
	if len(req.Bindings) != len(gate3BindingReqs) {
		return fmt.Errorf("gate3: binding 數 %d ≠ %d（不得有未知 binding）", len(req.Bindings), len(gate3BindingReqs))
	}
	if tr := found["task_run"]; tr.Ref != req.Subject {
		return fmt.Errorf("gate3: subject %q 與 task_run binding %q 不一致", req.Subject, tr.Ref)
	}
	return nil
}

// BuildDecision：Gate 3 為無 risk policy（對齊 gate1／TCA 與 DecisionInput
// 契約，§3.1）——approved／rejected 兩條路徑皆拒絕非空 RiskSelections。
// approved 分支另執行 §5.3 決議時重驗（fail closed）；rejected 分支僅需
// reason（由 Service 層既有驗證），跳過重驗。
func (p *Gate3Policy) BuildDecision(req gate.GateRequest, decision string, input gate.DecisionInput) (*gate.Metadata, error) {
	if len(input.RiskSelections) > 0 {
		return nil, fmt.Errorf("gate3: risk selections not accepted")
	}
	if decision != "approved" {
		return nil, nil
	}
	b := map[string]gate.Binding{}
	for _, x := range req.Bindings {
		b[x.Kind] = x
	}
	if p.deps.VerifyTaskRun == nil || p.deps.VerifyForge == nil || p.deps.VerifyProvenance == nil {
		return nil, fmt.Errorf("gate3: dependency not wired（C1 接線前不可決議）")
	}
	taskRunID := b["task_run"].Ref[len("taskrun:"):]
	if err := p.deps.VerifyTaskRun(taskRunID, b["task_run"].Digest); err != nil {
		return nil, fmt.Errorf("gate3 重驗(1) taskrun: %w", err)
	}
	if err := p.deps.VerifyForge(b["promotion_head"].Digest, b["main_base"].Digest, b["required_check_manifest"].Digest); err != nil {
		return nil, fmt.Errorf("gate3 重驗(2-4) forge: %w", err)
	}
	if err := p.deps.VerifyProvenance(taskRunID, b["review_evidence_provenance"].Digest, b["promotion_head"].Digest); err != nil {
		return nil, fmt.Errorf("gate3 重驗(5) provenance: %w", err)
	}
	return nil, nil
}

func (p *Gate3Policy) SupersessionKey(gateName, subject string) string {
	return gateName + "|" + subject
}

// ReconcileBindings——**契約凍結（rev7，plan gate 第六輪 P1）**：
//
// production PrepareDecision 會先以 pending request 建 pseudo-record 呼叫
// 本方法做 staleness 前置檢查；一旦回傳 cause，會被轉成**未包裝**一般
// 錯誤並直接返回，BuildDecision 不執行（service.go:107）。若 pending 的
// TaskRun STALE 判定接在這裡，gateDecide 的 errors.Is(ErrGate3Mismatch)
// 分支永遠不會觸發——pending 永久滯留，違反 B5 §4.3。因此凍結：
//   - pending pseudo-record（rec.Decision == ""）**一律回空**——pending
//     Gate 3 的失效只走決議時重驗（BuildDecision → mismatch sentinel →
//     gateDecide ExpirePending；owner 裁決 6c 決議時重驗、不輪詢）。
//   - 已核可 record（rec.Decision == "approved"）才回 stale cause——C1a
//     接 TaskRun reader 後補「TaskRun STALE → gate3 record stale」；骨架期回空。
func (p *Gate3Policy) ReconcileBindings(rec gate.ApprovalRecord) ([]gate.StaleCause, error) {
	if rec.Decision == "" {
		return nil, nil // pending：決議時重驗承載，不得在此回 cause
	}
	return nil, nil // approved record：C1a 接線；骨架期回空
}
