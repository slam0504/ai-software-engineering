package escalation

import (
	"errors"
	"fmt"
	"sync"
)

// Service is the escalation application layer: it ties Journal (append log)
// and Project (pure reducer) together for create/ack/resolve with §3.8's
// invariants — system-item condition-key dedup, occurrence counting, hard
// items resolvable only by the system, and resolved-field validation at the
// write side (Project re-validates on replay, but a bad transition must never
// reach the journal in the first place).
//
// 重入規約（task-24 凍結）：Service 本身「絕不」取 app 層的 workflowMu——
// 跨 journal 的互斥完全由 app 層持有；這裡的小 mu 只保護單一 escalation
// journal 的 check-then-append 原子性（同 gate.Service 之於 gate journal）。
type Service struct {
	mu   sync.Mutex
	j    *Journal
	ulid func() string
	now  func() string
}

func NewService(j *Journal, ulid func() string, now func() string) *Service {
	return &Service{j: j, ulid: ulid, now: now}
}

// Entries returns the current projection. A Project failure fails loud —
// callers must treat the inbox as unavailable, never as empty (§3.8).
func (s *Service) Entries() ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Project(s.j.Lines())
}

// CreateSystem appends a system-sourced item under conditionKey. An existing
// not-yet-resolved item with the same key wins (dedup: its id is returned,
// nothing is appended); otherwise occurrence = historical count of the key
// + 1. Before appending, the minted escalation_id is checked against every
// existing item — a duplicate id would make Project's replay silently revive
// a resolved entry (the later Item record resets state to open).
func (s *Service) CreateSystem(conditionKey, blockScope string, hard bool, summary, sourceRef string) (string, error) {
	if conditionKey == "" {
		return "", errors.New("escalation: system item requires a non-empty condition_key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := Project(s.j.Lines())
	if err != nil {
		return "", err
	}
	if e := OpenKeyed(entries, conditionKey); e != nil {
		return e.Item.EscalationID, nil // 同 key 未 resolved 項已存在：去重回既有 id
	}
	occurrence := 1
	for _, e := range entries {
		if e.Item.ConditionKey == conditionKey {
			occurrence++
		}
	}
	return s.appendItemLocked(Item{
		ConditionKey: conditionKey, Occurrence: occurrence, Source: "system",
		SourceRef: sourceRef, BlockScope: blockScope, Hard: hard, Summary: summary,
	}, entries)
}

// CreateManual appends a manual item. sourceRef is mandatory (§3.8: manual
// items have no condition key, so the source reference is their only anchor).
func (s *Service) CreateManual(sourceRef, blockScope, summary string) (string, error) {
	if sourceRef == "" {
		return "", errors.New("escalation: manual item requires a non-empty source_ref")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := Project(s.j.Lines())
	if err != nil {
		return "", err
	}
	return s.appendItemLocked(Item{
		Occurrence: 1, Source: "manual", SourceRef: sourceRef,
		BlockScope: blockScope, Summary: summary,
	}, entries)
}

// appendItemLocked mints the id (with the duplicate-id sanity check against
// entries) and durably appends it. Caller holds s.mu.
func (s *Service) appendItemLocked(it Item, entries []Entry) (string, error) {
	id := s.ulid()
	for _, e := range entries {
		if e.Item.EscalationID == id {
			return "", fmt.Errorf("escalation: minted duplicate escalation_id %q — refusing append (would revive a prior entry on replay)", id)
		}
	}
	it.EscalationID = id
	it.CreatedAt = s.now()
	if err := s.j.AppendItem(it); err != nil {
		return "", err
	}
	return id, nil
}

// Ack marks id acknowledged. Acknowledged does NOT release any block (§3.8).
func (s *Service) Ack(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.findLocked(id)
	if err != nil {
		return err
	}
	if e.State == StateResolved {
		return fmt.Errorf("escalation: item %q is already resolved", id)
	}
	return s.j.AppendTransition(ItemTransition{EscalationID: id, To: StateAcknowledged, At: s.now()})
}

// Resolve marks id resolved. Hard items only accept actor == "system"
// (§3.8: UI 不可手動 resolve). Resolution/reason/actor are validated here at
// the write side — Project would fail loud on replay otherwise.
func (s *Service) Resolve(id, resolution, reason, actor string) error {
	if resolution == "" || reason == "" || actor == "" {
		return errors.New("escalation: resolve requires resolution, reason and actor")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.findLocked(id)
	if err != nil {
		return err
	}
	if e.State == StateResolved {
		return fmt.Errorf("escalation: item %q is already resolved", id)
	}
	if e.Item.Hard && actor != "system" {
		return fmt.Errorf("escalation: item %q is hard — only the system may resolve it (§3.8)", id)
	}
	return s.j.AppendTransition(ItemTransition{EscalationID: id, To: StateResolved,
		Resolution: resolution, Reason: reason, Actor: actor, At: s.now()})
}

// ResolveByKey system-resolves the not-yet-resolved item under conditionKey,
// recording evidenceRef as the repair evidence. No open item under the key is
// a no-op (nil) — most repairs run unconditionally on their authoritative
// path (§3.8 修復條件) and usually have nothing to release.
func (s *Service) ResolveByKey(conditionKey, evidenceRef string) error {
	if evidenceRef == "" {
		evidenceRef = "system-repair"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := Project(s.j.Lines())
	if err != nil {
		return err
	}
	e := OpenKeyed(entries, conditionKey)
	if e == nil {
		return nil
	}
	return s.j.AppendTransition(ItemTransition{EscalationID: e.Item.EscalationID, To: StateResolved,
		Resolution: "system_repaired", Reason: evidenceRef, Actor: "system", At: s.now()})
}

// findLocked projects and returns the entry for id. Caller holds s.mu.
func (s *Service) findLocked(id string) (*Entry, error) {
	entries, err := Project(s.j.Lines())
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].Item.EscalationID == id {
			return &entries[i], nil
		}
	}
	return nil, fmt.Errorf("escalation: item %q not found", id)
}
