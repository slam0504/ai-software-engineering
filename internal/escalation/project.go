package escalation

import (
	"encoding/json"
	"fmt"
)

// Project replays raw "escalation_item"/"escalation_transition" journal
// lines into one Entry per escalation_id, applying the open → acknowledged
// → resolved state machine (§3.8). resolved is a terminal state: once an
// entry reaches it, further transitions targeting the same escalation_id
// are ignored rather than reviving it. An unknown "_type", an unmarshal
// failure, an unknown transition target, or a "resolved" transition missing
// Resolution/Reason/Actor all fail loud — malformed *line* content is
// already screened out by internal/journal, so anything wrong here is a
// schema/invariant violation at this package's level.
func Project(lines [][]byte) ([]Entry, error) {
	order := []string{}
	idx := map[string]*Entry{}
	get := func(id string) *Entry {
		if e, ok := idx[id]; ok {
			return e
		}
		e := &Entry{State: StateOpen}
		idx[id] = e
		order = append(order, id)
		return e
	}
	for _, raw := range lines {
		var probe struct {
			Type string `json:"_type"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, fmt.Errorf("escalation: journal record: %w", err)
		}
		switch probe.Type {
		case "escalation_item":
			var it Item
			if err := json.Unmarshal(raw, &it); err != nil {
				return nil, fmt.Errorf("escalation: journal record: %w", err)
			}
			e := get(it.EscalationID)
			e.Item = it
			e.State = StateOpen
		case "escalation_transition":
			var tr ItemTransition
			if err := json.Unmarshal(raw, &tr); err != nil {
				return nil, fmt.Errorf("escalation: journal record: %w", err)
			}
			e := get(tr.EscalationID)
			if e.State == StateResolved {
				continue // resolved 終態不復活
			}
			switch tr.To {
			case StateAcknowledged:
				e.State = StateAcknowledged
			case StateResolved:
				if tr.Resolution == "" || tr.Reason == "" || tr.Actor == "" {
					return nil, fmt.Errorf("escalation: resolved transition for %q missing resolution/reason/actor", tr.EscalationID)
				}
				e.State = StateResolved
			default:
				return nil, fmt.Errorf("escalation: unknown transition target %q", tr.To)
			}
		default:
			return nil, fmt.Errorf("escalation: unknown record _type %q", probe.Type)
		}
	}
	out := make([]Entry, 0, len(order))
	for _, id := range order {
		out = append(out, *idx[id])
	}
	return out, nil
}

// BlockingFor returns every not-yet-resolved entry (open or acknowledged —
// acknowledged does NOT release the block, §3.8) whose BlockScope covers
// scope. An entry with BlockScope == "workspace" overrides everything and
// blocks any scope; otherwise BlockScope must match scope exactly. An entry
// with BlockScope == "" is never blocking (manual/informational items).
func BlockingFor(entries []Entry, scope string) []Entry {
	out := []Entry{}
	for _, e := range entries {
		if e.State == StateResolved {
			continue
		}
		bs := e.Item.BlockScope
		if bs == "" {
			continue
		}
		if bs == "workspace" || bs == scope {
			out = append(out, e)
		}
	}
	return out
}

// OpenKeyed returns the first not-yet-resolved (open or acknowledged) entry
// whose ConditionKey equals conditionKey, for system-source dedup (§3.8).
// A resolved entry under the same key does not block creating a new
// occurrence — it is skipped rather than returned.
func OpenKeyed(entries []Entry, conditionKey string) *Entry {
	for i := range entries {
		e := &entries[i]
		if e.State == StateResolved {
			continue
		}
		if e.Item.ConditionKey == conditionKey {
			return e
		}
	}
	return nil
}
