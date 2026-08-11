package gate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"sync"
)

var ErrJournalDegraded = errors.New("gate journal degraded")

type Journal struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	ops      []GateOp
	degraded bool
}

func OpenJournal(path string) (*Journal, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ops, validLen, badTail := parseOps(data)
	if badTail != nil && !badTail.isFinal {
		return nil, badTail.err // 中段 malformed：fail loud，不修
	}
	if badTail != nil { // final malformed：quarantine + truncate
		if werr := os.WriteFile(path+".quarantine", data[validLen:], 0o644); werr != nil {
			return nil, werr
		}
		if terr := truncateAndSync(path, data[:validLen]); terr != nil {
			return nil, terr
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Journal{path: path, f: f, ops: ops}, nil
}

type parseErr struct {
	err     error
	isFinal bool
}

func parseOps(data []byte) (ops []GateOp, validLen int, bad *parseErr) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	offset := 0
	lines := [][]byte{}
	for sc.Scan() {
		lines = append(lines, append([]byte(nil), sc.Bytes()...))
	}
	// 逐行 unmarshal；最後一行壞 = final（截斷），中段壞 = fail loud
	for i, ln := range lines {
		var op GateOp
		if err := json.Unmarshal(ln, &op); err != nil {
			isFinal := i == len(lines)-1 && !bytes.HasSuffix(data, []byte("\n"))
			return ops, offset, &parseErr{err: err, isFinal: isFinal}
		}
		ops = append(ops, op)
		offset += len(ln) + 1
	}
	return ops, offset, nil
}

func truncateAndSync(path string, keep []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(keep); err != nil {
		return err
	}
	return f.Sync()
}

func (j *Journal) Append(op GateOp) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.degraded {
		return ErrJournalDegraded
	}
	line, err := json.Marshal(op)
	if err != nil {
		return err
	}
	if _, err := j.f.Write(append(line, '\n')); err != nil {
		j.degraded = true
		return err
	}
	if err := j.f.Sync(); err != nil {
		j.degraded = true
		return err
	}
	j.ops = append(j.ops, op)
	return nil
}

func (j *Journal) Ops() []GateOp {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]GateOp(nil), j.ops...)
}

func (j *Journal) Degraded() bool { j.mu.Lock(); defer j.mu.Unlock(); return j.degraded }

func (j *Journal) Close() error { return j.f.Close() }
