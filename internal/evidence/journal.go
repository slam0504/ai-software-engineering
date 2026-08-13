// Journal is a thin EvidenceRun/Mutation-typed wrapper around the generic
// append-only internal/journal.Journal (mirrors internal/gate.Journal's
// shape). It owns marshaling EvidenceRun/Mutation <-> raw JSON lines and the
// "恰一次 finalize" invariant (§4-5): a duplicate evidence_id/mutation_id
// append is rejected rather than silently accepted, so a run can be
// finalized into the journal at most once. Crash-safe tail repair and
// write/sync semantics live in internal/journal; uniqueness and replay
// reconstruction live here.
package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/slam0504/sdlc-workbench/internal/journal"
)

// ErrJournalDegraded mirrors gate.ErrJournalDegraded: once the underlying
// journal.Journal has failed a write, further Append* calls are refused
// rather than risk producing a silent gap.
var ErrJournalDegraded = errors.New("evidence: journal degraded")

// ErrDuplicateEvidenceID / ErrDuplicateMutationID guard the exactly-once
// finalize invariant (§4-5): re-appending a record for an evidence_id or
// mutation_id already present in the journal is rejected, never silently
// overwritten or accepted as a second write.
var (
	ErrDuplicateEvidenceID = errors.New("evidence: duplicate evidence_id")
	ErrDuplicateMutationID = errors.New("evidence: duplicate mutation_id")
)

// ErrNotFound is returned by Get/GetMutation when no record for the given id
// has been appended (or replayed) into the journal.
var ErrNotFound = errors.New("evidence: not found")

// evidenceRunRecord／mutationRecord discriminate journal lines via "_type"
// (mirrors internal/gate's GateOp record shapes: gate/types.go's
// ApprovalRecord/Transition each carry their own Type field tagged
// json:"_type"). EvidenceRun and Mutation are frozen types from Task 17/19
// with no such field, so the discriminator is added here via composition
// instead — an anonymous embed flattens EvidenceRun/Mutation's JSON fields
// onto the same line as "_type" without touching either type.
type evidenceRunRecord struct {
	Type string `json:"_type"`
	EvidenceRun
}

type mutationRecord struct {
	Type string `json:"_type"`
	Mutation
}

// Journal is the evidence package's append-only log of EvidenceRun and
// Mutation records. mu guards both the in-memory index (runs/muts) and every
// call into the underlying journal.Journal, so a concurrent duplicate-id
// Append and a concurrent Get always observe a consistent snapshot.
type Journal struct {
	mu   sync.Mutex
	j    *journal.Journal
	runs map[string]EvidenceRun
	muts map[string]Mutation
}

// OpenJournal opens the underlying raw journal at path and replays every
// loaded line into the in-memory index, reconstructing full state after a
// crash/restart. A line whose "_type" is neither "evidence_run" nor
// "mutation" is corruption at this package's level (the generic journal
// layer already accepted it as syntactically valid JSON) and causes
// OpenJournal to fail loud, matching internal/gate.OpenJournal's precedent.
func OpenJournal(path string) (*Journal, error) {
	j, err := journal.Open(path)
	if err != nil {
		return nil, err
	}
	jr := &Journal{j: j, runs: map[string]EvidenceRun{}, muts: map[string]Mutation{}}
	for _, ln := range j.Lines() {
		var probe struct {
			Type string `json:"_type"`
		}
		if err := json.Unmarshal(ln, &probe); err != nil {
			return nil, fmt.Errorf("evidence: journal: %w", err)
		}
		switch probe.Type {
		case "evidence_run":
			var rec evidenceRunRecord
			if err := json.Unmarshal(ln, &rec); err != nil {
				return nil, fmt.Errorf("evidence: journal: %w", err)
			}
			jr.runs[rec.EvidenceID] = rec.EvidenceRun
		case "mutation":
			var rec mutationRecord
			if err := json.Unmarshal(ln, &rec); err != nil {
				return nil, fmt.Errorf("evidence: journal: %w", err)
			}
			jr.muts[rec.MutationID] = rec.Mutation
		default:
			return nil, fmt.Errorf("evidence: journal: unknown record _type %q", probe.Type)
		}
	}
	return jr, nil
}

// AppendEvidenceRun durably appends run as an "evidence_run" record.
// run.EvidenceID must not already exist in the journal — the caller
// (app.go's RunEvidence) is expected to have already persisted every CAS
// artifact the run references (stdout/stderr/mutation patch) before calling
// this, per §3.7's durability order; AppendEvidenceRun itself only owns the
// journal side of that order.
func (j *Journal) AppendEvidenceRun(run EvidenceRun) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.j.Degraded() {
		return ErrJournalDegraded
	}
	if _, exists := j.runs[run.EvidenceID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateEvidenceID, run.EvidenceID)
	}
	line, err := json.Marshal(evidenceRunRecord{Type: "evidence_run", EvidenceRun: run})
	if err != nil {
		return err
	}
	if err := j.j.Append(line); err != nil {
		return err // raw write/sync error; underlying journal is now degraded
	}
	j.runs[run.EvidenceID] = run
	return nil
}

// AppendMutation durably appends m as a "mutation" record. m.MutationID must
// not already exist in the journal, same reasoning as AppendEvidenceRun.
func (j *Journal) AppendMutation(m Mutation) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.j.Degraded() {
		return ErrJournalDegraded
	}
	if _, exists := j.muts[m.MutationID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateMutationID, m.MutationID)
	}
	line, err := json.Marshal(mutationRecord{Type: "mutation", Mutation: m})
	if err != nil {
		return err
	}
	if err := j.j.Append(line); err != nil {
		return err
	}
	j.muts[m.MutationID] = m
	return nil
}

// Get returns the EvidenceRun previously appended (or replayed) under
// evidenceID.
func (j *Journal) Get(evidenceID string) (EvidenceRun, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	run, ok := j.runs[evidenceID]
	if !ok {
		return EvidenceRun{}, fmt.Errorf("%w: evidence_id %s", ErrNotFound, evidenceID)
	}
	return run, nil
}

// GetMutation returns the Mutation previously appended (or replayed) under
// mutationID.
func (j *Journal) GetMutation(mutationID string) (Mutation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	m, ok := j.muts[mutationID]
	if !ok {
		return Mutation{}, fmt.Errorf("%w: mutation_id %s", ErrNotFound, mutationID)
	}
	return m, nil
}

// Degraded reports whether a prior Append* failed, meaning further writes
// are refused.
func (j *Journal) Degraded() bool { return j.j.Degraded() }

// Close closes the underlying file handle.
func (j *Journal) Close() error { return j.j.Close() }
