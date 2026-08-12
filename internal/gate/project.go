package gate

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

var (
	reSHA256 = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reGitOID = regexp.MustCompile(`^git:(sha1:[0-9a-f]{40}|sha256:[0-9a-f]{64})$`)
)

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

func ValidateGate1Bindings(bs []Binding) error {
	seen := map[string]Binding{}
	for _, b := range bs {
		if _, dup := seen[b.Kind]; dup {
			return fmt.Errorf("duplicate binding kind %q", b.Kind)
		}
		seen[b.Kind] = b
	}
	sm, ok := seen["spec_manifest"]
	if !ok {
		return errors.New("missing spec_manifest binding")
	}
	if !reSHA256.MatchString(sm.Digest) {
		return fmt.Errorf("spec_manifest digest must be sha256:<64hex>: %q", sm.Digest)
	}
	bc, ok := seen["base_commit"]
	if !ok {
		return errors.New("missing base_commit binding")
	}
	if !reGitOID.MatchString(bc.Digest) {
		return fmt.Errorf("base_commit digest must be git:<algo>:<full oid>: %q", bc.Digest)
	}
	return nil
}
