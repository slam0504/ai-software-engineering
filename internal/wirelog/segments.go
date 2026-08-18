package wirelog

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/slam0504/sdlc-workbench/internal/journal"
)

// ErrSegmentSetDegraded mirrors internal/evidence and internal/gate's
// ErrJournalDegraded: once a durable SegmentSet's underlying journal has
// failed a write, further Append calls are refused rather than risk
// producing a gap in the session's only durable wire-log evidence.
var ErrSegmentSetDegraded = errors.New("wirelog: segment set journal degraded")

// segmentRecord is the JSONL envelope for one durable SegmentSet.Append call:
// {wsid, wire_log_id, start_frame, end_frame}. Composition (anonymous embed)
// mirrors internal/evidence's evidenceRunRecord/mutationRecord — it flattens
// SegmentRef's JSON fields onto the same line as wsid without adding a WSID
// field to SegmentRef itself (SegmentRef describes "one frame range in one
// wire log"; ownership by a WSID is SegmentSet's concern, not SegmentRef's).
type segmentRecord struct {
	WSID string `json:"wsid"`
	SegmentRef
}

// SegmentSet is session-level wire-log evidence (§3.4.4): an ordered
// []SegmentRef per WSID, letting one WSID span multiple app-server
// generations (B1 probe, server restart, app restart) even though the
// connection-wide wire log is one physical file per generation.
//
// Relationship to the frame index's per-frame WSID (§3.4.3, WSIDResolver) —
// the two answer different questions and neither replaces the other:
//
//   - SegmentSet answers "which generations / frame ranges does this WSID span",
//     which is the only thing that survives without reading the wire logs at all
//     and the only thing that is ordered across generations.
//   - The frame index's per-frame WSID answers "inside an overlapping range,
//     who owns *this* frame". Concurrent sessions share one codex.Conn, so
//     their ranges necessarily contain each other's frames (see App's
//     closeWireSegment `exclusive` qualifier) — set-level evidence cannot
//     answer it.
//
// Both are durable: SegmentSet through this journal, the per-frame WSID through
// the wsid column of each wire-log row (rebuildable via RebuildFrameIndex).
// This is not a dual source of truth — they have disjoint scopes, and the
// per-frame column is the *only* writer of frame-level ownership.
//
// Ordering: For(wsid) returns segments in Append call order. SegmentSet does
// not reorder by any other key — frame numbers are only comparable within
// the same wire_log_id/generation, not across generations, so time order
// must come from call order. Callers (Task 12/13) are responsible for
// calling Append in chronological order.
type SegmentSet struct {
	mu   sync.Mutex
	j    *journal.Journal // nil: in-memory only (NewSegmentSet)
	byID map[string][]SegmentRef
}

// NewSegmentSet returns an in-memory-only SegmentSet with no crash-safe
// persistence: segments Append'd to it are lost on process exit. This is for
// tests and any non-production use; production callers must use
// OpenSegmentSet so that a crash does not permanently lose a session's wire
// log attribution (see SegmentSet's doc).
func NewSegmentSet() *SegmentSet {
	return &SegmentSet{byID: map[string][]SegmentRef{}}
}

// OpenSegmentSet opens (creating if necessary) a durable, crash-safe
// SegmentSet backed by an append-only JSONL journal at path, replaying any
// previously appended segments into memory so For reflects pre-crash state
// immediately after Open.
//
// Append-only journal, not a snapshot store (contrast internal/wsregistry's
// temp-file+atomic-rename Store): a SegmentRef is a fact about the past
// ("this WSID owned this frame range in this generation") that is only ever
// added to, never edited or superseded — the same shape as
// internal/evidence's and internal/gate's journals, unlike wsregistry's
// mutable current-state entries.
//
// Crash-tail handling is inherited from internal/journal.Open, unchanged:
// a torn final line (process died mid-Append) is quarantined and truncated
// away, tolerated; a malformed line followed by valid lines is mid-file
// corruption and fails loud. This mirrors Task 10's
// FrameIndex/RebuildFrameIndex grading (§3.5.6-style) — wire log frame
// indices and their session-level attribution have the same failure shape
// (append-only log torn by an unclean process exit), so there is no reason
// for a different policy here. A record whose JSON is syntactically valid
// (journal.Open already accepts it) but is missing a wsid, or decodes into
// something other than segmentRecord, is corruption at this package's level
// and also fails loud, matching internal/evidence.OpenJournal's precedent
// for unknown record shapes.
func OpenSegmentSet(path string) (*SegmentSet, error) {
	j, err := journal.Open(path)
	if err != nil {
		return nil, err
	}
	s := &SegmentSet{j: j, byID: map[string][]SegmentRef{}}
	for _, ln := range j.Lines() {
		var rec segmentRecord
		if err := json.Unmarshal(ln, &rec); err != nil {
			return nil, fmt.Errorf("wirelog: segment set journal %s: %w", path, err)
		}
		if rec.WSID == "" {
			return nil, fmt.Errorf("wirelog: segment set journal %s: record missing wsid", path)
		}
		s.byID[rec.WSID] = append(s.byID[rec.WSID], rec.SegmentRef)
	}
	return s, nil
}

// Append records that wsid owns ref (a frame range within one generation),
// ordered after any segments already recorded for the same wsid.
//
// If s is backed by a durable journal (OpenSegmentSet), ref is durably
// persisted before it becomes visible to For; a persist failure is returned
// and in-memory state is left unchanged (fail loud, no silent degrade —
// this task's Global Constraint). Once a persist has failed, s is latched
// degraded and every subsequent Append is refused with
// ErrSegmentSetDegraded, matching internal/evidence and internal/gate's
// journal-backed types.
func (s *SegmentSet) Append(wsid string, ref SegmentRef) error {
	if wsid == "" {
		return fmt.Errorf("wirelog: segment set: empty wsid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.j != nil {
		if s.j.Degraded() {
			return ErrSegmentSetDegraded
		}
		line, err := json.Marshal(segmentRecord{WSID: wsid, SegmentRef: ref})
		if err != nil {
			return err
		}
		if err := s.j.Append(line); err != nil {
			return err // raw write/sync error; underlying journal is now degraded
		}
	}
	s.byID[wsid] = append(s.byID[wsid], ref)
	return nil
}

// For returns a copy of the ordered segments recorded for wsid (nil if none
// have been recorded). The returned slice and its elements are independent
// of SegmentSet's internal state — callers mutating the result must not be
// able to corrupt it, the same class of bug Task 7 hit via a returned
// pointer copy.
func (s *SegmentSet) For(wsid string) []SegmentRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.byID[wsid]
	if v == nil {
		return nil
	}
	out := make([]SegmentRef, len(v))
	copy(out, v)
	return out
}

// Degraded reports whether a prior durable Append failed. Always false for
// an in-memory-only SegmentSet (NewSegmentSet).
func (s *SegmentSet) Degraded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.j == nil {
		return false
	}
	return s.j.Degraded()
}

// Close closes the underlying journal file handle, if any (no-op for an
// in-memory-only SegmentSet).
func (s *SegmentSet) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.j == nil {
		return nil
	}
	return s.j.Close()
}
