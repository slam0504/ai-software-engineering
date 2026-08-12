// Package journal implements a generic append-only JSON-lines (JSONL) log
// with crash-safe tail repair. It operates on raw JSON line bytes and does
// not know about any particular record schema — callers own marshaling and
// unmarshaling their own payloads.
//
// Crash semantics:
//   - A malformed line in the middle of the file is unrecoverable corruption
//     and causes Open to fail loud (returns an error).
//   - A malformed line at the very end of the file is treated as a torn
//     write from a crash mid-append: it is quarantined to "<path>.quarantine"
//     and the journal file is truncated to the last good line.
//   - A syntactically valid final line that is NOT terminated by '\n' is
//     also treated as a torn write, because Append always writes
//     `line + '\n'` followed by Sync in a single step — a missing trailing
//     newline means that write was never durably committed, even though the
//     JSON itself happens to parse.
package journal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"sync"
)

// ErrDegraded is returned by Append once a previous write has failed. A
// degraded journal refuses further writes rather than risk producing gaps.
var ErrDegraded = errors.New("journal degraded")

// Journal is a crash-safe append-only JSONL log.
type Journal struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	lines    [][]byte
	degraded bool
}

// Open opens (creating if necessary) the journal at path, repairing a torn
// tail write if one is found. Mid-file corruption is reported as an error
// rather than silently repaired.
func Open(path string) (*Journal, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lines, validLen, badTail := parseLines(data)
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
	return &Journal{path: path, f: f, lines: lines}, nil
}

type parseErr struct {
	err     error
	isFinal bool
}

// parseLines scans data into raw JSON lines, validating each is syntactically
// valid JSON. It returns the valid lines, the byte length covered by them,
// and a non-nil bad tail descriptor if the final line is corrupt or torn.
func parseLines(data []byte) (lines [][]byte, validLen int, bad *parseErr) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	offset := 0
	rawLines := [][]byte{}
	for sc.Scan() {
		rawLines = append(rawLines, append([]byte(nil), sc.Bytes()...))
	}
	// 逐行檢查是否為合法 JSON；最後一行壞 = final（截斷），中段壞 = fail loud
	for i, ln := range rawLines {
		if !json.Valid(ln) {
			isFinal := i == len(rawLines)-1 && !bytes.HasSuffix(data, []byte("\n"))
			return lines, offset, &parseErr{err: errors.New("malformed journal line"), isFinal: isFinal}
		}
		lines = append(lines, ln)
		offset += len(ln) + 1
	}
	// Every line parsed as valid JSON. Append always writes `line + '\n'` in a
	// single Sync'd call, so a file that does NOT end in '\n' means the last
	// line was never durably committed — a crash can tear off exactly the
	// trailing newline while leaving otherwise-valid JSON bytes behind. Treat
	// that last line as a torn final tail even though it parses, so it goes
	// through the same quarantine+truncate repair path.
	if len(data) > 0 && len(rawLines) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		lastLen := len(rawLines[len(rawLines)-1])
		validLen := offset - lastLen - 1
		return lines[:len(lines)-1], validLen, &parseErr{
			err:     errors.New("torn final line without trailing newline"),
			isFinal: true,
		}
	}
	return lines, offset, nil
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

// Append writes line followed by '\n' and fsyncs it. On any write or sync
// error the journal is marked degraded and all subsequent Appends fail with
// ErrDegraded.
func (j *Journal) Append(line []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.degraded {
		return ErrDegraded
	}
	buf := append(append([]byte(nil), line...), '\n')
	if _, err := j.f.Write(buf); err != nil {
		j.degraded = true
		return err
	}
	if err := j.f.Sync(); err != nil {
		j.degraded = true
		return err
	}
	j.lines = append(j.lines, append([]byte(nil), line...))
	return nil
}

// Lines returns a copy of all lines successfully loaded/appended so far.
func (j *Journal) Lines() [][]byte {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([][]byte, len(j.lines))
	for i, ln := range j.lines {
		out[i] = append([]byte(nil), ln...)
	}
	return out
}

// Degraded reports whether a prior Append failed, meaning further writes are
// refused.
func (j *Journal) Degraded() bool { j.mu.Lock(); defer j.mu.Unlock(); return j.degraded }

// Close closes the underlying file handle.
func (j *Journal) Close() error { return j.f.Close() }
