package gate

import (
	"encoding/json"
	"fmt"
	"regexp"
)

var (
	reSHA256 = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reGitOID = regexp.MustCompile(`^git:(sha1:[0-9a-f]{40}|sha256:[0-9a-f]{64})$`)
)

type BindingReq struct {
	Kind     string
	Role     string
	DigestRe *regexp.Regexp
}

// normalizeRequest 將 v1 形狀（無 schema_version、有 spec_manifest_digest／
// base_commit legacy 欄位）正規化為 v2 形狀（schema_version=2、
// subject="workspace"、bindings=[spec_manifest, base_commit]）。純函式，僅
// 影響 in-memory projection，不回寫 journal。已是 v2 的請求原樣回傳。
func normalizeRequest(r GateRequest) GateRequest {
	if r.SchemaVersion == 0 && r.SpecManifestDigest != "" {
		r.SchemaVersion = 2
		r.Subject = "workspace"
		r.Bindings = []Binding{
			{Kind: "spec_manifest", Digest: r.SpecManifestDigest},
			{Kind: "base_commit", Digest: r.BaseCommit},
		}
	}
	return r
}

func Project(ops []GateOp) ([]GateEntry, error) {
	order := []string{}
	idx := map[string]*GateEntry{}
	get := func(id string) *GateEntry {
		if e, ok := idx[id]; ok {
			return e
		}
		e := &GateEntry{ApprovalID: id, State: Pending}
		idx[id] = e
		order = append(order, id)
		return e
	}
	for _, op := range ops {
		for _, raw := range op.Records {
			var probe struct {
				Type string `json:"_type"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				return nil, fmt.Errorf("gate_op record: %w", err)
			}
			switch probe.Type {
			case "gate_request":
				var r GateRequest
				_ = json.Unmarshal(raw, &r)
				r = normalizeRequest(r)
				e := get(r.ApprovalID)
				e.Request = &r // 仍 pending
			case "approval_record":
				var r ApprovalRecord
				_ = json.Unmarshal(raw, &r)
				e := get(r.ApprovalID)
				e.Record = &r
				if e.State == Pending && r.Decision == "approved" {
					e.State = Active
				}
				if e.State == Pending && r.Decision == "rejected" {
					e.State = Rejected
				}
			case "transition":
				var tr Transition
				_ = json.Unmarshal(raw, &tr)
				e := get(tr.ApprovalID)
				// changed 只在本次 transition 實際改變 e.State 時為
				// true——只有這時才更新 TerminalCause（Task 6b）。Rejected
				// 不出現在這個 switch：它只由上面 "approval_record" 分支的
				// r.Decision == "rejected" 設定（且該分支本身已限定
				// e.State == Pending 才生效），原因留在 Record.Reason，此
				// 處永不補寫 TerminalCause。Rejected／Expired 都只能從
				// Pending 轉入、且 ops 依序套用，先成立的終態會讓 State
				// 離開 Pending，另一條終態路徑的入口條件自然不再成立——
				// 「先成立的終態保持不變」不需要額外互斥判斷。
				changed := false
				switch tr.To {
				case "stale":
					// 不得覆寫 Superseded／Rejected／Expired（同級終態，
					// 先成立者不變）；已是 Stale 不算改變（避免被同狀態
					// 的後續 transition 覆寫 cause）。
					if e.State != Superseded && e.State != Rejected &&
						e.State != Expired && e.State != Stale {
						e.State = Stale
						changed = true
					}
				case "superseded":
					// 允許 Stale→Superseded（既有修復路徑，
					// project_test.go:109 固定）；不得覆寫 Rejected／
					// Expired；已是 Superseded 不算改變。
					if e.State != Rejected && e.State != Expired &&
						e.State != Superseded {
						e.State = Superseded
						changed = true
					}
				case "expired":
					// B5 §4.3：expired 僅由 Pending 轉入——其餘狀態（含
					// 重複 expire）一律忽略，不得覆寫既有終態。
					if e.State == Pending {
						e.State = Expired
						changed = true
					}
				}
				if changed {
					e.TerminalCause = tr.Cause
				}
			default:
				return nil, fmt.Errorf("unknown record _type %q", probe.Type)
			}
		}
	}
	out := make([]GateEntry, 0, len(order))
	for _, id := range order {
		out = append(out, *idx[id])
	}
	return out, nil
}

func validateBindingSet(bs []Binding, required []BindingReq) error {
	// 檢查 (kind, role) 唯一性
	seen := map[[2]string]bool{}
	for _, b := range bs {
		key := [2]string{b.Kind, b.Role}
		if seen[key] {
			return fmt.Errorf("duplicate binding (kind, role): (%q, %q)", b.Kind, b.Role)
		}
		seen[key] = true
	}

	// 檢查所有 required binding 都存在且 digest 符合
	for _, req := range required {
		found := false
		for _, b := range bs {
			if b.Kind == req.Kind && b.Role == req.Role {
				found = true
				if !req.DigestRe.MatchString(b.Digest) {
					return fmt.Errorf("binding (%q, %q) digest %q does not match expected pattern", req.Kind, req.Role, b.Digest)
				}
				break
			}
		}
		if !found {
			return fmt.Errorf("missing required binding (kind, role): (%q, %q)", req.Kind, req.Role)
		}
	}

	return nil
}

func ValidateGate1Bindings(bs []Binding) error {
	gate1Reqs := []BindingReq{
		{Kind: "spec_manifest", Role: "", DigestRe: reSHA256},
		{Kind: "base_commit", Role: "", DigestRe: reGitOID},
	}
	return validateBindingSet(bs, gate1Reqs)
}
