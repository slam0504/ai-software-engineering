package escalation

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/slam0504/sdlc-workbench/internal/journal"
)

// ErrJournalDegraded mirrors gate.ErrJournalDegraded / evidence.ErrJournalDegraded:
// once the underlying journal.Journal has failed a write, further Append*
// calls are refused rather than risk producing a silent gap.
var ErrJournalDegraded = errors.New("escalation: journal degraded")

// Journal is a thin Item/ItemTransition-typed wrapper around the generic
// append-only internal/journal.Journal (mirrors internal/gate.Journal and
// internal/evidence.Journal's shape). It owns marshaling Item/ItemTransition
// <-> raw JSON lines; crash-safe tail repair and write/sync semantics live
// in internal/journal. Projection (open/acknowledged/resolved state,
// blocking, condition-key dedup) is deliberately not owned here — it lives
// in project.go's Project, which consumes Lines() so callers/tests can
// project without going through an open Journal.
type Journal struct {
	mu sync.Mutex
	j  *journal.Journal
}

// OpenJournal opens the underlying raw journal at path. Line-level
// unmarshal/schema validation happens in Project, not here — the generic
// journal layer already guarantees every loaded line is syntactically valid
// JSON with crash-safe tail repair.
func OpenJournal(path string) (*Journal, error) {
	j, err := journal.Open(path)
	if err != nil {
		return nil, err
	}
	return &Journal{j: j}, nil
}

// AppendItem durably appends it as an "escalation_item" record.
func (j *Journal) AppendItem(it Item) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.j.Degraded() {
		return ErrJournalDegraded
	}
	it.Type = "escalation_item"
	line, err := json.Marshal(it)
	if err != nil {
		return err
	}
	return j.j.Append(line) // raw write/sync error; underlying journal is now degraded
}

// AppendTransition durably appends tr as an "escalation_transition" record.
func (j *Journal) AppendTransition(tr ItemTransition) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.j.Degraded() {
		return ErrJournalDegraded
	}
	tr.Type = "escalation_transition"
	line, err := json.Marshal(tr)
	if err != nil {
		return err
	}
	return j.j.Append(line)
}

// Lines returns every record loaded/appended so far, for Project to replay.
func (j *Journal) Lines() [][]byte { return j.j.Lines() }

// Degraded reports whether a prior Append* failed, meaning further writes
// are refused.
func (j *Journal) Degraded() bool { return j.j.Degraded() }

// Close closes the underlying file handle.
func (j *Journal) Close() error { return j.j.Close() }
