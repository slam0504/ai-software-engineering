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
	Kind    string
	Role    string
	DigestRe *regexp.Regexp
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
			case "transition":
				var tr Transition
				_ = json.Unmarshal(raw, &tr)
				e := get(tr.ApprovalID)
				switch tr.To { // stale/superseded 皆終態，不復活
				case "stale":
					if e.State != Superseded {
						e.State = Stale
					}
				case "superseded":
					e.State = Superseded
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
