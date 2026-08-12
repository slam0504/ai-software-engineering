package evidence

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/plan"
)

func testEvidenceRun(id string) EvidenceRun {
	return EvidenceRun{
		EvidenceID:          id,
		Kind:                "expected_red",
		Source:              "local_app",
		BaseCommit:          "base",
		TestCommit:          "test",
		OracleSurfaceDigest: "sha256:" + id,
		Command:             plan.Command{Executable: "sh", Argv: []string{"run_test.sh"}},
		CWD:                 "worktree:" + id,
		StartedAt:           "2026-08-12T00:00:00Z",
		FinishedAt:          "2026-08-12T00:00:01Z",
		ExitCode:            1,
		ExpectedFailure:     plan.ExpectedFailure{TestIDs: []string{"TestX"}, Matcher: "FAIL"},
		ObservedFailure:     "FAIL: TestX",
		StdoutDigest:        "sha256:out",
		StderrDigest:        "sha256:err",
		RecordingRef:        "/tmp/cas",
		RunnerVersion:       "m3a-1",
		Result:              "passed",
	}
}

// ---- Step 1: 同一 evidence_id append 兩次第二次拒 ----

func TestAppendEvidenceRunRejectsDuplicateEvidenceID(t *testing.T) {
	j, err := OpenJournal(filepath.Join(t.TempDir(), "evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })

	run := testEvidenceRun("ev1")
	if err := j.AppendEvidenceRun(run); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := j.AppendEvidenceRun(run); !errors.Is(err, ErrDuplicateEvidenceID) {
		t.Fatalf("second append of the same evidence_id must reject with ErrDuplicateEvidenceID, got %v", err)
	}
}

func TestAppendMutationRejectsDuplicateMutationID(t *testing.T) {
	j, err := OpenJournal(filepath.Join(t.TempDir(), "evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })

	m := Mutation{MutationID: "m1", TaskRef: "P1/T1", Digest: "sha256:abc", CreatedAt: "2026-08-12T00:00:00Z"}
	if err := j.AppendMutation(m); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := j.AppendMutation(m); !errors.Is(err, ErrDuplicateMutationID) {
		t.Fatalf("second append of the same mutation_id must reject with ErrDuplicateMutationID, got %v", err)
	}
}

// ---- Step 1: replay 重建後 Get／GetMutation 回傳完整 record ----

func TestOpenJournalReplaysAndReconstructsGetAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	j1, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	run := testEvidenceRun("ev1")
	if err := j1.AppendEvidenceRun(run); err != nil {
		t.Fatal(err)
	}
	m := Mutation{MutationID: "m1", TaskRef: "P1/T1", Digest: "sha256:abc", CreatedAt: "2026-08-12T00:00:00Z"}
	if err := j1.AppendMutation(m); err != nil {
		t.Fatal(err)
	}
	if err := j1.Close(); err != nil {
		t.Fatal(err)
	}

	// 模擬重啟：重新 OpenJournal 同一路徑，不帶任何 in-process 狀態。
	j2, err := OpenJournal(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = j2.Close() })

	gotRun, err := j2.Get("ev1")
	if err != nil {
		t.Fatalf("Get after replay: %v", err)
	}
	if !reflect.DeepEqual(gotRun, run) {
		t.Fatalf("replayed EvidenceRun mismatch:\nwant %+v\ngot  %+v", run, gotRun)
	}

	gotMut, err := j2.GetMutation("m1")
	if err != nil {
		t.Fatalf("GetMutation after replay: %v", err)
	}
	if gotMut != m {
		t.Fatalf("replayed Mutation mismatch:\nwant %+v\ngot  %+v", m, gotMut)
	}

	// replay 後的 index 仍受恰一次保護（不是只在同一個 Journal 物件生命週期內）。
	if err := j2.AppendEvidenceRun(run); !errors.Is(err, ErrDuplicateEvidenceID) {
		t.Fatalf("post-replay duplicate append must still reject, got %v", err)
	}
}

func TestGetUnknownIDReturnsErrNotFound(t *testing.T) {
	j, err := OpenJournal(filepath.Join(t.TempDir(), "evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })

	if _, err := j.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of unknown evidence_id must return ErrNotFound, got %v", err)
	}
	if _, err := j.GetMutation("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMutation of unknown mutation_id must return ErrNotFound, got %v", err)
	}
}

func TestOpenJournalRejectsUnknownRecordType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	// 繞過 AppendEvidenceRun／AppendMutation 直接寫入未知 _type 的原始行，
	// 模擬未來 schema 演進或損毀寫入——OpenJournal 必須 fail loud，不得默默跳過。
	if err := j.j.Append([]byte(`{"_type":"bogus","evidence_id":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenJournal(path); err == nil {
		t.Fatal("OpenJournal must reject an unknown record _type, got nil error")
	}
}
