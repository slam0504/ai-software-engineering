package gate

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/slam0504/sdlc-workbench/internal/journal"
)

var ErrJournalDegraded = errors.New("gate journal degraded")

// Journal is a thin GateOp-typed wrapper around the generic append-only
// internal/journal.Journal. It owns marshaling GateOp <-> raw JSON lines;
// crash-safe tail repair and write/sync semantics live in internal/journal.
type Journal struct {
	mu  sync.Mutex
	j   *journal.Journal
	ops []GateOp
}

// OpenJournal opens the underlying raw journal at path and unmarshals every
// loaded line into a GateOp. A line that fails to unmarshal as a GateOp is
// mid-file corruption at the gate level (the generic layer already accepted
// it as syntactically valid JSON) and causes OpenJournal to fail loud,
// matching the pre-refactor behavior.
func OpenJournal(path string) (*Journal, error) {
	j, err := journal.Open(path)
	if err != nil {
		return nil, err
	}
	lines := j.Lines()
	ops := make([]GateOp, 0, len(lines))
	for _, ln := range lines {
		var op GateOp
		if err := json.Unmarshal(ln, &op); err != nil {
			j.Close()
			return nil, err
		}
		ops = append(ops, op)
	}
	return &Journal{j: j, ops: ops}, nil
}

func (j *Journal) Append(op GateOp) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.j.Degraded() {
		return ErrJournalDegraded
	}
	line, err := json.Marshal(op)
	if err != nil {
		return err
	}
	if err := j.j.Append(line); err != nil {
		return err // raw write/sync error; underlying journal is now degraded
	}
	j.ops = append(j.ops, op)
	return nil
}

func (j *Journal) Ops() []GateOp {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]GateOp(nil), j.ops...)
}

func (j *Journal) Degraded() bool { return j.j.Degraded() }

func (j *Journal) Close() error { return j.j.Close() }
