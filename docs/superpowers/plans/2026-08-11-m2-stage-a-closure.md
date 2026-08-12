# M2 Stage A 閉環 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 workbench app 內做到規格編輯 → app 內 scoped commit → 送核 → Gate 1 核可（綁 spec_manifest ＋ base_commit）→ 規格變更觸發 STALE 的可稽核閉環。

**Architecture:** 純確定性 Go domain（`internal/spec` manifest／`internal/gate` reducer）＋ 帶 I/O 的 `gate.Service`（journal 交易、`ReconcileGate1`）＋ additive contract（workspace event lane）＋ app.go 綁定 ＋ 隔離 one-shot SpecAssist ＋ 前端規格工作區／表示圖／Gate 主控台。STALE 走「讀取權威重算 ＋ watcher 通知」雙層。

**Tech Stack:** Go 1.x（既有）、Wails v2、Vue 3 + Pinia、CodeMirror 6（新增）、mermaid（既有）、fsnotify（既有 v1.10.1）、go-git 或 `git cat-file`（committed snapshot，Task 9 定）。

**Spec:** `docs/superpowers/specs/2026-08-11-m2-stage-a-closure-design.md`（rev5，五輪 closure review PASS）。每個 task 的驗收對照該 spec 的凍結契約。

## Global Constraints

- **契約 additive-only**：`contract.Envelope` 只加欄位、不改既有欄位語意（BAT 教訓）。
- **event_id 檔案級單調**：所有事件（含 workspace）經 `Manager` 單一 mutex 出口，`event_id` 跨 provider 嚴格遞增。
- **稽核只追加**：`gate_op` journal append-only；目前狀態一律由 `gate.Project()` 重算的 projection，UI 只顯示 projection。
- **fail loud**：跳過的步驟／降級一律揭露；journal 中段 malformed 拒載、degraded mode 停用核可。
- **納管範圍固定**（scope_version=1）：`spec/features/**`、`spec/nfr/**`、`spec/glossary.md`、`spec/context-map/**`。
- **digest 格式**：`spec_manifest.digest = sha256:<64 hex>`；`base_commit.digest = git:<algorithm>:<完整 object id>`（禁短 SHA）。
- **SpecAssist 安全不變量**：provider-enforced zero workspace mutation（Claude one-shot `--tools ""`；Codex ephemeral thread `sandboxPolicy={type:"readOnly",networkAccess:false}` ＋ `approvalPolicy="never"`）；escalation/approval 一律 fail closed。
- **驗證基線**：`.workbench/` 為 app state（gitignored）；每次收尾 gate 含既有 Go `-race` 全套、前端 44 vitest、frontend/Wails build，不得只驗 M2 新測試。

---

## File Structure

**新增 Go：**
- `internal/spec/manifest.go` — canonical manifest 純計算 ＋ scope 宣告。
- `internal/spec/snapshot.go` — `BuildCommittedSnapshot`（HEAD tree）、`BuildCurrentManifest`（worktree 雙建）、dirty 偵測。
- `internal/spec/commit.go` — 兩階段 `PreviewSpecCommit`/`ConfirmSpecCommit`。
- `internal/gate/types.go` — `ApprovalRecord`、`Transition`、`GateRequest`、`GateOp`、`Binding`。
- `internal/gate/project.go` — `Project()` 純 reducer ＋ binding 驗證。
- `internal/gate/journal.go` — append-only JSONL store（單交易、Sync、tail 修復、degraded）。
- `internal/gate/service.go` — `Service.ReconcileGate1`、`SubmitForApproval`、`Decide`、`List`。

**修改 Go：**
- `internal/contract/event.go` — 新增 `KindGateRequest`、`KindBindingStale` Kind。
- `internal/contract/envelope.go` — additive 欄位 `Scope`、`Bindings`、`Payload`、`CorrelationID`、`Purpose`。
- `internal/appcore/manager.go` — 新增 `EmitWorkspace(env)`。
- `app.go` — 新增 spec/gate/SpecAssist 綁定；watcher 生命週期。

**新增前端：**
- `frontend/src/stores/gate.ts` — gate projection store（不進 session reducer）。
- `frontend/src/stores/assist.ts` — SpecAssist 草稿區 store。
- `frontend/src/components/GateConsole.vue`、`SpecWorkspace.vue`、`DiagramPane.vue`。
- `frontend/src/lib/gateRouting.ts` — scope/purpose 分流純函式。

**修改前端：**
- `frontend/src/types.ts` — Envelope additive 欄位 ＋ gate 型別。
- `frontend/src/App.vue` — event 依 scope/purpose 分流。
- `frontend/package.json` — 新增 CodeMirror 6。

---

## Phase 1 — 純確定性 domain

### Task 1: canonical manifest（純計算 ＋ scope）

**Files:**
- Create: `internal/spec/manifest.go`
- Test: `internal/spec/manifest_test.go`

**Interfaces:**
- Produces:
  - `const ScopeVersion = 1`
  - `var ScopePatterns = []string{"spec/features/**","spec/nfr/**","spec/glossary.md","spec/context-map/**"}`
  - `type FileEntry struct { Path string; SHA256 string }` （Path 為 repo-relative `/`；SHA256 為 raw bytes 十六進位）
  - `func ManifestDigest(entries []FileEntry) (string, error)` → `sha256:<64hex>`；內部序列化 canonical JSON `{"scope_version":1,"patterns":[...],"files":[{"path","sha256"}...]}`，files 依 path byte-order 排序。
  - `func InScope(relPath string) bool`

- [ ] **Step 1: Write the failing test**

```go
package spec

import "testing"

func TestManifestDigestDeterministicAndOrdered(t *testing.T) {
	a := []FileEntry{{Path: "spec/nfr/perf.md", SHA256: "bb"}, {Path: "spec/features/a.feature", SHA256: "aa"}}
	b := []FileEntry{{Path: "spec/features/a.feature", SHA256: "aa"}, {Path: "spec/nfr/perf.md", SHA256: "bb"}}
	da, err := ManifestDigest(a)
	if err != nil {
		t.Fatal(err)
	}
	db, _ := ManifestDigest(b)
	if da != db {
		t.Fatalf("order must not affect digest: %s vs %s", da, db)
	}
	if len(da) != len("sha256:")+64 {
		t.Fatalf("bad digest shape: %s", da)
	}
}

func TestScopeVersionInCanonical(t *testing.T) {
	// 改 scope_version 必須改 digest（否則改 scope 可排除檔案而不觸發 STALE）
	e := []FileEntry{{Path: "spec/glossary.md", SHA256: "aa"}}
	d1, _ := ManifestDigest(e)
	orig := ScopeVersion
	t.Cleanup(func() { setScopeVersionForTest(orig) })
	setScopeVersionForTest(orig + 1)
	d2, _ := ManifestDigest(e)
	if d1 == d2 {
		t.Fatal("scope_version must be part of canonical content")
	}
}

func TestInScope(t *testing.T) {
	for _, p := range []string{"spec/features/x.feature", "spec/nfr/a.md", "spec/glossary.md", "spec/context-map/c4.mmd"} {
		if !InScope(p) {
			t.Errorf("want in-scope: %s", p)
		}
	}
	for _, p := range []string{"spec/other.md", "app.go", "spec/glossary.md.bak"} {
		if InScope(p) {
			t.Errorf("want out-of-scope: %s", p)
		}
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/spec/ -run TestManifest -v`
Expected: FAIL（未定義）。

- [ ] **Step 3: Implement**

```go
package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

var scopeVersion = 1

const ScopeVersion = 1 // 對外常數；內部以 scopeVersion 供測試覆寫

var ScopePatterns = []string{"spec/features/**", "spec/nfr/**", "spec/glossary.md", "spec/context-map/**"}

func setScopeVersionForTest(v int) { scopeVersion = v }

type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type canonical struct {
	ScopeVersion int         `json:"scope_version"`
	Patterns     []string    `json:"patterns"`
	Files        []FileEntry `json:"files"`
}

func ManifestDigest(entries []FileEntry) (string, error) {
	files := append([]FileEntry(nil), entries...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	c := canonical{ScopeVersion: scopeVersion, Patterns: ScopePatterns, Files: files}
	b, err := json.Marshal(c) // struct 欄位序固定 → canonical；無時間欄位
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// InScope：`**` 語意＝該目錄下任意深度。純前綴比對 ＋ glossary.md 精確比對即可，
// 不需 glob 函式庫；`..` 已在 app 層 resolveInWorkspace 擋掉。
func InScope(rel string) bool {
	rel = strings.TrimPrefix(rel, "./")
	if rel == "spec/glossary.md" {
		return true
	}
	for _, dir := range []string{"spec/features/", "spec/nfr/", "spec/context-map/"} {
		if strings.HasPrefix(rel, dir) {
			return true
		}
	}
	return false
}
```

> 註：import 移除未用的 `path`／`fmt`（僅保留 `crypto/sha256`、`encoding/hex`、`encoding/json`、`sort`、`strings`）。

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/spec/ -run 'TestManifest|TestScope|TestInScope' -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/spec/manifest.go internal/spec/manifest_test.go
git commit -m "feat(spec): canonical manifest digest with scope_version"
```

---

### Task 2: gate 型別 ＋ Project reducer ＋ binding 驗證（純）

**Files:**
- Create: `internal/gate/types.go`, `internal/gate/project.go`
- Test: `internal/gate/project_test.go`

**Interfaces:**
- Produces:
  - `type Binding struct { Kind, Ref, Digest string }`
  - `type ApprovalRecord struct { ApprovalID, Gate, Decision string; Approver Approver; Reason string; Bindings []Binding; CreatedAt string }`
  - `type Approver struct { ID, Method string }`
  - `type Transition struct { ApprovalID, To, At, Cause, EvidenceRef string }`
  - `type GateRequest struct { ApprovalID, Gate, SpecManifestDigest, BaseCommit, CreatedAt string }`
  - `type GateOp struct { OpID, At string; Records []json.RawMessage }` （union 三型；每筆帶 `"_type"` 判別）
  - `type State string`（`"pending" | "active" | "stale" | "superseded"`）
  - `type GateEntry struct { ApprovalID string; State State; Record *ApprovalRecord; Request *GateRequest }`
  - `func Project(ops []GateOp) ([]GateEntry, error)`
  - `func ValidateGate1Bindings(bs []Binding) error`（必填 spec_manifest+base_commit、各至多一筆、digest 格式）

- [ ] **Step 1: Write the failing test**

```go
package gate

import "testing"

func TestProjectPendingThenActiveThenStale(t *testing.T) {
	ops := []GateOp{
		opWith(t, GateRequest{ApprovalID: "A", Gate: "gate1", SpecManifestDigest: "sha256:x", BaseCommit: "git:sha1:c1"}),
		opWith(t, ApprovalRecord{ApprovalID: "A", Gate: "gate1", Decision: "approved",
			Bindings: []Binding{{"spec_manifest", "spec/", "sha256:x"}, {"base_commit", "HEAD", "git:sha1:c1"}}}),
	}
	e := entryByID(mustProject(t, ops), "A")
	if e.State != Active {
		t.Fatalf("want active, got %s", e.State)
	}
	ops = append(ops, opWith(t, Transition{ApprovalID: "A", To: "stale", Cause: "changed"}))
	e = entryByID(mustProject(t, ops), "A")
	if e.State != Stale {
		t.Fatalf("want stale, got %s", e.State)
	}
	// stale 不復活：再加同 digest 也不變回 active
	ops = append(ops, opWith(t, Transition{ApprovalID: "A", To: "stale", Cause: "noop"}))
	if entryByID(mustProject(t, ops), "A").State != Stale {
		t.Fatal("stale must not revive")
	}
}

func TestProjectSupersede(t *testing.T) {
	ops := []GateOp{
		opWith(t, ApprovalRecord{ApprovalID: "A", Gate: "gate1", Decision: "approved",
			Bindings: gate1B("sha256:x", "git:sha1:c1")},
			Transition{ApprovalID: "A", To: "superseded", Cause: "new approval"}),
		opWith(t, ApprovalRecord{ApprovalID: "B", Gate: "gate1", Decision: "approved",
			Bindings: gate1B("sha256:y", "git:sha1:c2")}),
	}
	es := mustProject(t, ops)
	if entryByID(es, "A").State != Superseded || entryByID(es, "B").State != Active {
		t.Fatal("want A superseded, B active — at most one active")
	}
}

func TestValidateGate1Bindings(t *testing.T) {
	if err := ValidateGate1Bindings(gate1B("sha256:"+hex64(), "git:sha1:"+hex40())); err != nil {
		t.Fatalf("valid should pass: %v", err)
	}
	// 重複 kind 拒絕
	dup := []Binding{{"spec_manifest", "spec/", "sha256:" + hex64()}, {"spec_manifest", "spec/", "sha256:" + hex64()}, {"base_commit", "HEAD", "git:sha1:" + hex40()}}
	if ValidateGate1Bindings(dup) == nil {
		t.Fatal("duplicate kind must be rejected")
	}
	// 缺 base_commit 拒絕
	if ValidateGate1Bindings([]Binding{{"spec_manifest", "spec/", "sha256:" + hex64()}}) == nil {
		t.Fatal("missing base_commit must be rejected")
	}
	// 短 SHA 拒絕
	if ValidateGate1Bindings(gate1B("sha256:"+hex64(), "git:sha1:abc123")) == nil {
		t.Fatal("short SHA must be rejected")
	}
}
```

> 測試 helper（放同檔）：`opWith(t, recs...)` 把 record marshal 進 `GateOp{OpID:"op-"+n, Records:[...]}`（每筆加 `_type`）；`mustProject`、`entryByID`、`gate1B(mDigest,bDigest)`、`hex64()`/`hex40()` 回固定長度 hex。State 常數 `Pending/Active/Stale/Superseded`。

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/gate/ -run 'TestProject|TestValidate' -v`
Expected: FAIL。

- [ ] **Step 3: Implement `types.go` ＋ `project.go`**

```go
// types.go
package gate

import "encoding/json"

type State string

const (
	Pending    State = "pending"
	Active     State = "active"
	Stale      State = "stale"
	Superseded State = "superseded"
)

type Binding struct {
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}
type Approver struct {
	ID     string `json:"id"`
	Method string `json:"method"`
}
type ApprovalRecord struct {
	Type       string    `json:"_type"` // "approval_record"
	ApprovalID string    `json:"approval_id"`
	Gate       string    `json:"gate"`
	Decision   string    `json:"decision"`
	Approver   Approver  `json:"approver"`
	Reason     string    `json:"reason"`
	Bindings   []Binding `json:"bindings"`
	CreatedAt  string    `json:"created_at"`
}
type Transition struct {
	Type        string `json:"_type"` // "transition"
	ApprovalID  string `json:"approval_id"`
	To          string `json:"to"`
	At          string `json:"at"`
	Cause       string `json:"cause"`
	EvidenceRef string `json:"evidence_ref"`
}
type GateRequest struct {
	Type               string `json:"_type"` // "gate_request"
	ApprovalID         string `json:"approval_id"`
	Gate               string `json:"gate"`
	SpecManifestDigest string `json:"spec_manifest_digest"`
	BaseCommit         string `json:"base_commit"`
	CreatedAt          string `json:"created_at"`
}
type GateOp struct {
	OpID    string            `json:"op_id"`
	At      string            `json:"at"`
	Records []json.RawMessage `json:"records"`
}
type GateEntry struct {
	ApprovalID string
	State      State
	Record     *ApprovalRecord
	Request    *GateRequest
}
```

```go
// project.go
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
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/gate/ -run 'TestProject|TestValidate' -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/gate/types.go internal/gate/project.go internal/gate/project_test.go
git commit -m "feat(gate): records, Project reducer, gate1 binding validation"
```

---

## Phase 2 — journal 交易 ＋ reconcile service

### Task 3: gate_op journal（單交易 append、Sync、tail 修復、degraded）

**Files:**
- Create: `internal/gate/journal.go`
- Test: `internal/gate/journal_test.go`

**Interfaces:**
- Produces:
  - `type Journal struct { ... }`
  - `func OpenJournal(path string) (*Journal, error)`（載入既有、驗證；中段 malformed → error；final malformed → quarantine+truncate 修復）
  - `func (j *Journal) Append(op GateOp) error`（單 lock／一行／`Sync()`；degraded 時回錯）
  - `func (j *Journal) Ops() []GateOp`（快照）
  - `func (j *Journal) Degraded() bool`
  - `var ErrJournalDegraded = errors.New("gate journal degraded")`

- [ ] **Step 1: Write the failing test**

```go
package gate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJournalAppendRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gate.jsonl")
	j, err := OpenJournal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Append(mustOp(t, GateRequest{Type: "gate_request", ApprovalID: "A", Gate: "gate1"})); err != nil {
		t.Fatal(err)
	}
	j2, _ := OpenJournal(p) // 重啟載入
	if len(j2.Ops()) != 1 {
		t.Fatalf("want 1 op after reload, got %d", len(j2.Ops()))
	}
}

func TestJournalMidfileMalformedRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gate.jsonl")
	os.WriteFile(p, []byte("{bad}\n{\"op_id\":\"x\",\"records\":[]}\n"), 0o644)
	if _, err := OpenJournal(p); err == nil {
		t.Fatal("mid-file malformed must be rejected (fail loud)")
	}
}

func TestJournalFinalMalformedRepairThenAppend(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gate.jsonl")
	good := `{"op_id":"o1","at":"t","records":[]}` + "\n"
	os.WriteFile(p, []byte(good+`{"op_id":"o2` /* 截斷 */), 0o644)
	j, err := OpenJournal(p) // final malformed → quarantine + truncate 修復
	if err != nil {
		t.Fatalf("final malformed should repair, got %v", err)
	}
	if j.Degraded() {
		t.Fatal("successful repair must not be degraded")
	}
	if err := j.Append(mustOp(t, GateRequest{Type: "gate_request", ApprovalID: "B"})); err != nil {
		t.Fatal(err)
	}
	j2, err := OpenJournal(p) // 再重啟仍完整（壞 tail 已修）
	if err != nil {
		t.Fatalf("reload after repair failed: %v", err)
	}
	if len(j2.Ops()) != 2 {
		t.Fatalf("want 2 ops (o1 + appended), got %d", len(j2.Ops()))
	}
	if _, err := os.Stat(p + ".quarantine"); err != nil {
		t.Fatal("bad tail must be quarantined as evidence")
	}
}
```

> helper `mustOp(t, recs...)` marshal 每筆記錄成 `json.RawMessage`、包成 `GateOp{OpID:"op",At:"t",Records:[...]}`。

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/gate/ -run TestJournal -v`
Expected: FAIL。

- [ ] **Step 3: Implement**

```go
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
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/gate/ -run TestJournal -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/gate/journal.go internal/gate/journal_test.go
git commit -m "feat(gate): append-only journal with single-op txn and tail repair"
```

---

### Task 4: worktree manifest 雙建 ＋ dirty 偵測

**Files:**
- Create: `internal/spec/snapshot.go`
- Test: `internal/spec/snapshot_test.go`

**Interfaces:**
- Consumes: Task 1 `FileEntry`/`ManifestDigest`/`HashBytes`/`InScope`。
- Produces:
  - `type Repo interface { HeadCommit() (string, error); ScopedClean() (bool, error); ReadScopedWorktree() ([]FileEntry, error); ReadScopedHeadTree(head string) ([]FileEntry, error) }`
  - `var ErrConcurrentModification = errors.New("spec: concurrent modification during scan")`
  - `func BuildCurrentManifest(r Repo) (string, error)`（雙建、一致才回、bounded retry、否則 `ErrConcurrentModification`）
  - `func BuildCommittedSnapshot(r Repo) (manifestDigest string, baseCommit string, err error)`（讀 HEAD tree；dirty 拒；HEAD₁/HEAD₂ 一致）

- [ ] **Step 1: Write the failing test**

```go
package spec

import (
	"errors"
	"testing"
)

type fakeRepo struct {
	head       string
	clean      bool
	worktree   [][]FileEntry // 逐次呼叫回不同集合（模擬掃描期間變動）
	headTree   []FileEntry
	wtCall     int
}

func (f *fakeRepo) HeadCommit() (string, error) { return f.head, nil }
func (f *fakeRepo) ScopedClean() (bool, error)  { return f.clean, nil }
func (f *fakeRepo) ReadScopedHeadTree(string) ([]FileEntry, error) { return f.headTree, nil }
func (f *fakeRepo) ReadScopedWorktree() ([]FileEntry, error) {
	e := f.worktree[min(f.wtCall, len(f.worktree)-1)]
	f.wtCall++
	return e, nil
}

func TestBuildCurrentManifestStableTwice(t *testing.T) {
	same := []FileEntry{{Path: "spec/glossary.md", SHA256: "aa"}}
	r := &fakeRepo{worktree: [][]FileEntry{same, same}}
	if _, err := BuildCurrentManifest(r); err != nil {
		t.Fatalf("stable double-build should pass: %v", err)
	}
}

func TestBuildCurrentManifestConcurrentModification(t *testing.T) {
	r := &fakeRepo{worktree: [][]FileEntry{
		{{Path: "spec/glossary.md", SHA256: "aa"}},
		{{Path: "spec/glossary.md", SHA256: "bb"}}, // 內容替換（mtime/size 可能不變）
		{{Path: "spec/glossary.md", SHA256: "cc"}},
	}}
	if _, err := BuildCurrentManifest(r); !errors.Is(err, ErrConcurrentModification) {
		t.Fatalf("want ErrConcurrentModification, got %v", err)
	}
}

func TestBuildCommittedSnapshotRejectsDirty(t *testing.T) {
	r := &fakeRepo{head: "c1", clean: false, headTree: []FileEntry{{Path: "spec/glossary.md", SHA256: "aa"}}}
	if _, _, err := BuildCommittedSnapshot(r); err == nil {
		t.Fatal("dirty scoped tree must reject 送核")
	}
}
```

- [ ] **Step 2: Run to verify fail** — `go test ./internal/spec/ -run TestBuild -v` → FAIL。

- [ ] **Step 3: Implement**

```go
package spec

import "errors"

var ErrConcurrentModification = errors.New("spec: concurrent modification during scan")

type Repo interface {
	HeadCommit() (string, error)
	ScopedClean() (bool, error)
	ReadScopedWorktree() ([]FileEntry, error)
	ReadScopedHeadTree(head string) ([]FileEntry, error)
}

const buildRetries = 3

func BuildCurrentManifest(r Repo) (string, error) {
	var last string
	for i := 0; i < buildRetries; i++ {
		a, err := r.ReadScopedWorktree()
		if err != nil {
			return "", err
		}
		da, err := ManifestDigest(a)
		if err != nil {
			return "", err
		}
		b, err := r.ReadScopedWorktree()
		if err != nil {
			return "", err
		}
		db, err := ManifestDigest(b)
		if err != nil {
			return "", err
		}
		if da == db {
			return da, nil
		}
		last = da
	}
	_ = last
	return "", ErrConcurrentModification
}

func BuildCommittedSnapshot(r Repo) (string, string, error) {
	head1, err := r.HeadCommit()
	if err != nil {
		return "", "", err
	}
	clean, err := r.ScopedClean()
	if err != nil {
		return "", "", err
	}
	if !clean {
		return "", "", errors.New("spec: scoped tree dirty — commit before 送核")
	}
	entries, err := r.ReadScopedHeadTree(head1)
	if err != nil {
		return "", "", err
	}
	digest, err := ManifestDigest(entries)
	if err != nil {
		return "", "", err
	}
	head2, err := r.HeadCommit()
	if err != nil {
		return "", "", err
	}
	if head1 != head2 {
		return "", "", errors.New("spec: HEAD moved during snapshot — retry")
	}
	return digest, "git:sha1:" + head1, nil
}
```

> `Repo` 的真實實作（go-git 或 `git cat-file`）於 Task 9 綁定；本 task 只驗純邏輯。`min` 於 Go 1.21+ 為 builtin。

- [ ] **Step 4: Run** — `go test ./internal/spec/ -run TestBuild -v` → PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/spec/snapshot.go internal/spec/snapshot_test.go
git commit -m "feat(spec): committed snapshot + double-build current manifest"
```

---

### Task 5: gate.Service — ReconcileGate1 ＋ Submit ＋ Decide ＋ List

**Files:**
- Create: `internal/gate/service.go`
- Test: `internal/gate/service_test.go`

**Interfaces:**
- Consumes: Task 2 型別/Project/Validate、Task 3 Journal、Task 4 `BuildCurrentManifest`/`BuildCommittedSnapshot`。
- Produces:
  - `type ManifestFn func() (string, error)`（注入 `BuildCurrentManifest` 綁定）
  - `type Emitter interface { EmitGateEvent(kind string, bindings []Binding, payload any) }`
  - `type Service struct { ... }` ＋ `func NewService(j *Journal, current ManifestFn, ulid func() string, now func() string, em Emitter) *Service`
  - `func (s *Service) Submit(manifestDigest, baseCommit string, bindings []Binding) (approvalID string, err error)`
  - `func (s *Service) Decide(approvalID, decision, reason string, approver Approver, bindings []Binding) error`
  - `func (s *Service) List() ([]GateEntry, error)`（呼叫 `ReconcileGate1` 後回 projection）
  - `func (s *Service) ReconcileGate1() error`
  - `var ErrNotPending, ErrRejectNeedsReason error`

- [ ] **Step 1: Write the failing test（含併發 barrier）**

```go
package gate

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestDecideOnlyOnceUnderConcurrency(t *testing.T) {
	s, _ := newTestService(t)
	id, _ := s.Submit("sha256:"+hex64(), "git:sha1:"+hex40(), gate1B("sha256:"+hex64(), "git:sha1:"+hex40()))
	var ok int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Decide(id, "approved", "", Approver{ID: "u", Method: "app-local"},
				gate1B("sha256:"+hex64(), "git:sha1:"+hex40())); err == nil {
				atomic.AddInt32(&ok, 1)
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("exactly one Decide must succeed, got %d", ok)
	}
}

func TestReconcileAppendsSingleStaleThenNoRevive(t *testing.T) {
	digest := "sha256:" + hex64()
	changed := "sha256:" + hex64b()
	cur := digest
	s, _ := newTestServiceWithCurrent(t, func() (string, error) { return cur, nil })
	id, _ := s.Submit(digest, "git:sha1:"+hex40(), gate1BWith(digest))
	_ = s.Decide(id, "approved", "", Approver{ID: "u", Method: "app-local"}, gate1BWith(digest))
	cur = changed // 規格變更
	if err := s.ReconcileGate1(); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileGate1(); err != nil { // 併發/重入只一筆 transition
		t.Fatal(err)
	}
	e := entryByID(mustProjectOps(t, s), id)
	if e.State != Stale {
		t.Fatalf("want stale, got %s", e.State)
	}
	cur = digest // 改回原值不復活
	_ = s.ReconcileGate1()
	if entryByID(mustProjectOps(t, s), id).State != Stale {
		t.Fatal("stale must not revive")
	}
}

func TestRejectNeedsReason(t *testing.T) {
	s, _ := newTestService(t)
	id, _ := s.Submit("sha256:"+hex64(), "git:sha1:"+hex40(), gate1BWith("sha256:"+hex64()))
	if err := s.Decide(id, "rejected", "", Approver{ID: "u", Method: "app-local"}, nil); err == nil {
		t.Fatal("rejected must require reason")
	}
}
```

- [ ] **Step 2: Run to verify fail** — `go test ./internal/gate/ -run 'TestDecide|TestReconcile|TestReject' -race -v` → FAIL。

- [ ] **Step 3: Implement**

```go
package gate

import (
	"errors"
	"sync"
)

var (
	ErrNotPending        = errors.New("gate: no pending request for approval id")
	ErrRejectNeedsReason = errors.New("gate: rejected decision requires reason")
)

type ManifestFn func() (string, error)
type Emitter interface {
	EmitGateEvent(kind string, bindings []Binding, payload any)
}

type Service struct {
	mu      sync.Mutex
	j       *Journal
	current ManifestFn
	ulid    func() string
	now     func() string
	em      Emitter
}

func NewService(j *Journal, current ManifestFn, ulid func() string, now func() string, em Emitter) *Service {
	return &Service{j: j, current: current, ulid: ulid, now: now, em: em}
}

func (s *Service) Submit(manifestDigest, baseCommit string, bindings []Binding) (string, error) {
	if err := ValidateGate1Bindings(bindings); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.ulid()
	req := GateRequest{Type: "gate_request", ApprovalID: id, Gate: "gate1",
		SpecManifestDigest: manifestDigest, BaseCommit: baseCommit, CreatedAt: s.now()}
	if err := s.appendOp(req); err != nil {
		return "", err
	}
	s.em.EmitGateEvent("gate_request", bindings, map[string]string{"approval_id": id, "gate": "gate1"})
	return id, nil
}

func (s *Service) Decide(id, decision, reason string, approver Approver, bindings []Binding) error {
	if decision == "rejected" && reason == "" {
		return ErrRejectNeedsReason
	}
	if decision == "approved" {
		if err := ValidateGate1Bindings(bindings); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := Project(s.j.Ops())
	if err != nil {
		return err
	}
	e := findEntry(entries, id)
	if e == nil || e.Record != nil || e.Request == nil { // 必須 pending（有 request、無 record）
		return ErrNotPending
	}
	recs := []any{ApprovalRecord{Type: "approval_record", ApprovalID: id, Gate: "gate1",
		Decision: decision, Approver: approver, Reason: reason, Bindings: bindings, CreatedAt: s.now()}}
	if decision == "approved" { // 同交易 supersede 先前 active
		for _, prev := range entries {
			if prev.State == Active {
				recs = append(recs, Transition{Type: "transition", ApprovalID: prev.ApprovalID,
					To: "superseded", At: s.now(), Cause: "new approved gate1 " + id})
			}
		}
	}
	if err := s.appendOp(recs...); err != nil {
		return err
	}
	s.em.EmitGateEvent("approval_decision", bindings, map[string]any{
		"approval_id": id, "gate": "gate1", "decision": decision,
		"approver": approver, "reason": reason})
	return nil
}

func (s *Service) List() ([]GateEntry, error) {
	if err := s.ReconcileGate1(); err != nil {
		return nil, err
	}
	return Project(s.j.Ops())
}

func (s *Service) ReconcileGate1() error {
	cur, err := s.current() // read-error（含 ErrConcurrentModification）→ 回錯，不 append stale
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := Project(s.j.Ops())
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.State != Active || e.Record == nil {
			continue
		}
		bound := ""
		for _, b := range e.Record.Bindings {
			if b.Kind == "spec_manifest" {
				bound = b.Digest
			}
		}
		if bound != "" && bound != cur { // 只允許一次 stale（active check 在 lock 內）
			tr := Transition{Type: "transition", ApprovalID: e.ApprovalID, To: "stale",
				At: s.now(), Cause: "spec_manifest changed", EvidenceRef: cur}
			if err := s.appendOp(tr); err != nil {
				return err
			}
			// durable 後才發布通知；EmitGateEvent 失敗不回滾（journal 權威）
			s.em.EmitGateEvent("binding_stale", nil, map[string]string{
				"approval_id": e.ApprovalID, "to": "stale",
				"cause": "spec_manifest changed", "evidence_ref": cur})
		}
	}
	return nil
}

func (s *Service) appendOp(recs ...any) error {
	raws, err := marshalRecords(recs...)
	if err != nil {
		return err
	}
	return s.j.Append(GateOp{OpID: s.ulid(), At: s.now(), Records: raws})
}
```

> helper `marshalRecords`、`findEntry` 放同檔；測試 helper `newTestService`/`newTestServiceWithCurrent`（用 temp journal ＋ counter ulid ＋ fake Emitter）放 `service_test.go`。

- [ ] **Step 4: Run to verify pass** — `go test ./internal/gate/ -race -v` → PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/gate/service.go internal/gate/service_test.go
git commit -m "feat(gate): Service — submit/decide/list/reconcile with once-decide + single-stale"
```

---

## Phase 3 — committed snapshot 實作 ＋ 兩階段 commit

### Task 6: git Repo 實作（committed snapshot 讀 object DB）

**Files:**
- Create: `internal/spec/gitrepo.go`
- Test: `internal/spec/gitrepo_test.go`（用 temp git repo）

**Interfaces:**
- Consumes: Task 4 `Repo` 介面。
- Produces: `func NewGitRepo(root string) *GitRepo`，實作 `Repo`：`ReadScopedHeadTree` 一律經 `git cat-file`／go-git 讀 HEAD tree（**不讀 worktree**）；`ScopedClean` 以 `git status --porcelain` 過濾納管 pattern；`ReadScopedWorktree` 讀工作區 raw bytes、拒 symlink/submodule/非 regular file。

- [ ] **Step 1: Write the failing test**

```go
package spec

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "a@b"}, {"config", "user.name", "a"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	return dir
}

func TestGitRepoCommittedSnapshotIgnoresWorktreeEdit(t *testing.T) {
	dir := initRepo(t)
	os.MkdirAll(filepath.Join(dir, "spec"), 0o755)
	os.WriteFile(filepath.Join(dir, "spec", "glossary.md"), []byte("v1"), 0o644)
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-m", "c1")
	r := NewGitRepo(dir)
	d1, _, err := BuildCommittedSnapshot(r)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "spec", "glossary.md"), []byte("v2-uncommitted"), 0o644)
	// committed snapshot 應仍等於 v1（讀 HEAD tree，非 worktree）— 但 dirty 應拒送
	if _, _, err := BuildCommittedSnapshot(r); err == nil {
		t.Fatal("dirty scoped tree must reject")
	}
	entries, _ := r.ReadScopedHeadTree("HEAD")
	d2, _ := ManifestDigest(entries)
	if d1 != d2 {
		t.Fatal("HEAD-tree manifest must be stable regardless of worktree edit")
	}
}

func TestGitRepoRejectsSymlinkInScope(t *testing.T) {
	dir := initRepo(t)
	os.MkdirAll(filepath.Join(dir, "spec", "features"), 0o755)
	os.Symlink("/etc/passwd", filepath.Join(dir, "spec", "features", "evil.feature"))
	r := NewGitRepo(dir)
	if _, err := r.ReadScopedWorktree(); err == nil {
		t.Fatal("symlink in scope must be rejected")
	}
}
```

> helper `run`/`min`；實作可先用 `os/exec` 呼叫 `git`（repo 已 pin git 依賴於環境），或改 go-git。writing 執行者二選一並在 commit message 註明。

- [ ] **Step 2-4:** 實作 `GitRepo`（`ReadScopedHeadTree` 用 `git ls-tree -r HEAD` + `git cat-file blob`；`ScopedClean` 用 `git status --porcelain -- spec/`；`ReadScopedWorktree` walk 納管 pattern、`os.Lstat` 拒 symlink、跳過 `.gitmodules`／submodule、raw bytes SHA-256）。Run `go test ./internal/spec/ -run TestGitRepo -v` → PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/spec/gitrepo.go internal/spec/gitrepo_test.go
git commit -m "feat(spec): git Repo — HEAD-tree snapshot, scoped clean, symlink/submodule reject"
```

---

### Task 7: 兩階段 SpecCommit（commit_token 綁 canonical tree digest）

**Files:**
- Create: `internal/spec/commit.go`
- Test: `internal/spec/commit_test.go`

**Interfaces:**
- Produces:
  - `type CommitToken struct { HeadOID string; TreeDigest string }`（TreeDigest = 候選提交 canonical tree digest，涵蓋 add/delete/rename/untracked/mode）
  - `func (r *GitRepo) PreviewSpecCommit() (CommitToken, string /*diff*/, error)`
  - `func (r *GitRepo) ConfirmSpecCommit(tok CommitToken, message string) error`（重驗 token；HeadOID 或 TreeDigest 改變即拒；scope 外 index/worktree 不變，無法保證則 staged 存在時 fail closed）
  - `var ErrCommitStale, ErrStagedChangesPresent error`

- [ ] **Step 1: Write the failing test**

```go
func TestConfirmRejectsWhenContentChangedAfterPreview(t *testing.T) {
	dir := initRepo(t)
	os.MkdirAll(filepath.Join(dir, "spec"), 0o755)
	os.WriteFile(filepath.Join(dir, "spec", "glossary.md"), []byte("draft"), 0o644)
	r := NewGitRepo(dir)
	tok, _, err := r.PreviewSpecCommit()
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "spec", "glossary.md"), []byte("changed-after-preview"), 0o644)
	if err := r.ConfirmSpecCommit(tok, "commit spec"); !errors.Is(err, ErrCommitStale) {
		t.Fatalf("content change after preview must reject: %v", err)
	}
}

func TestConfirmFailsClosedWithOutOfScopeStaged(t *testing.T) {
	dir := initRepo(t)
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("package x"), 0o644)
	run(t, dir, "add", "app.go") // scope 外 staged
	os.MkdirAll(filepath.Join(dir, "spec"), 0o755)
	os.WriteFile(filepath.Join(dir, "spec", "glossary.md"), []byte("draft"), 0o644)
	r := NewGitRepo(dir)
	tok, _, _ := r.PreviewSpecCommit()
	if err := r.ConfirmSpecCommit(tok, "m"); !errors.Is(err, ErrStagedChangesPresent) {
		t.Fatalf("out-of-scope staged change must fail closed: %v", err)
	}
}
```

- [ ] **Step 2-4:** 實作（Preview：讀 HEAD OID ＋ 從**納管 worktree 候選內容**算 canonical tree digest；Confirm：重讀 HEAD OID＋重算 tree digest 比對 token，任一不符回 `ErrCommitStale`；偵測 scope 外 staged（`git diff --cached --name-only` 有納管外路徑）→ `ErrStagedChangesPresent`；commit 僅 `git add -- <scoped patterns>` + `git commit -m`）。Run `go test ./internal/spec/ -run 'TestConfirm' -v` → PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/spec/commit.go internal/spec/commit_test.go
git commit -m "feat(spec): two-phase SpecCommit with commit_token and staged fail-closed"
```

---

## Phase 4 — contract additive ＋ EmitWorkspace

### Task 8: Envelope additive 欄位 ＋ workspace kinds ＋ Manager.EmitWorkspace

**Files:**
- Modify: `internal/contract/event.go`, `internal/contract/envelope.go`, `internal/appcore/manager.go`
- Test: `internal/appcore/manager_workspace_test.go`, `internal/contract/envelope_test.go`

**Interfaces:**
- Produces:
  - `contract.KindGateRequest = "gate_request"`、`contract.KindBindingStale = "binding_stale"`
  - Envelope additive：`Scope string json:"scope,omitempty"`、`Bindings []Binding json:"bindings,omitempty"`、`Payload json.RawMessage json:"payload,omitempty"`、`CorrelationID string json:"correlation_id,omitempty"`、`Purpose string json:"purpose,omitempty"`
  - `type Binding struct { Kind, Ref, Digest string }`（contract 版；json tag kind/ref/digest）
  - `func (m *Manager) EmitWorkspace(kind string, bindings []Binding, payload any)`（scope=workspace、無 provider/session_id、走 `writeAndEmitLocked`、不碰 slot reducer）

- [ ] **Step 1: Write the failing test**

```go
package appcore

import (
	"encoding/json"
	"testing"

	"…/internal/contract"
)

func TestEmitWorkspaceScopedAndNoSlot(t *testing.T) {
	var got []contract.Envelope
	m := New(Config{Emit: func(e contract.Envelope) { got = append(got, e) },
		Sink: nopSink{}})
	m.EmitWorkspace("gate_request",
		[]contract.Binding{{Kind: "spec_manifest", Ref: "spec/", Digest: "sha256:x"}},
		map[string]string{"approval_id": "A"})
	if len(got) != 1 {
		t.Fatalf("want 1 workspace envelope, got %d", len(got))
	}
	e := got[0]
	if e.Scope != "workspace" || e.Provider != "" || e.SessionID != "" {
		t.Fatalf("workspace event must omit provider/session_id, got %+v", e)
	}
	if e.Kind != "gate_request" || len(e.Bindings) != 1 {
		t.Fatal("kind/bindings must be top-level")
	}
	var p map[string]string
	_ = json.Unmarshal(e.Payload, &p)
	if p["approval_id"] != "A" {
		t.Fatal("payload must carry approval_id, not Text")
	}
	if e.Text != "" {
		t.Fatal("must not stuff data into Text")
	}
}
```

- [ ] **Step 2: Run to verify fail** → FAIL。

- [ ] **Step 3: Implement**（envelope 加欄位；event.go 加兩 Kind；manager 加）：

```go
// manager.go
func (m *Manager) EmitWorkspace(kind string, bindings []contract.Binding, payload any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		m.emitClosedDroppedLocked(kind, "")
		return
	}
	raw, _ := json.Marshal(payload)
	env := contract.Envelope{
		EventID: contract.NewULID(time.Now()),
		TS:      time.Now().UTC().Format(time.RFC3339Nano),
		Scope:   "workspace",
		Kind:    kind,
		Bindings: bindings,
		Payload: raw,
	}
	m.writeAndEmitLocked(env) // 檔案級 event_id 遞增；不碰 slot reducer
}
```

- [ ] **Step 4: Run** → PASS。加 `go test ./internal/contract/ ./internal/appcore/ -race` 綠。

- [ ] **Step 5: Commit**

```bash
git add internal/contract/ internal/appcore/manager.go internal/appcore/manager_workspace_test.go
git commit -m "feat(contract): additive scope/bindings/payload + Manager.EmitWorkspace lane"
```

---

## Phase 5 — app.go 綁定

### Task 9: SpecList / SpecRead / SpecWrite（atomic rename ＋ expected_digest）

**Files:**
- Modify: `app.go`
- Test: `app_spec_test.go`

**Interfaces:**
- Produces（Wails 綁定）：
  - `func (a *App) SpecList() ([]FileNode, error)`（列納管樹）
  - `func (a *App) SpecRead(rel string) (content string, digest string, err error)`（digest = raw bytes sha256 hex）
  - `func (a *App) SpecWrite(rel, content, expectedDigest string) (newDigest string, err error)`（新檔 expectedDigest=""；既有檔 mismatch → `ErrSpecWriteConflict`；驗 canonical parent、atomic rename）
  - `var ErrSpecWriteConflict error`

- [ ] **Step 1: Write the failing test**

```go
func TestSpecWriteNewFileAtomic(t *testing.T) {
	a := newTestApp(t) // workspaceDir = temp
	d, err := a.SpecWrite("spec/features/x.feature", "Feature: X\n", "")
	if err != nil {
		t.Fatal(err)
	}
	_, got, _ := a.SpecRead("spec/features/x.feature")
	if got != d {
		t.Fatal("read digest must match write digest")
	}
}

func TestSpecWriteConflictOnStaleExpectedDigest(t *testing.T) {
	a := newTestApp(t)
	a.SpecWrite("spec/glossary.md", "v1", "")
	if _, err := a.SpecWrite("spec/glossary.md", "v2", "sha256:wrong"); !errors.Is(err, ErrSpecWriteConflict) {
		t.Fatalf("stale expected_digest must conflict: %v", err)
	}
}

func TestSpecWriteRejectsOutOfScope(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.SpecWrite("app.go", "x", ""); err == nil {
		t.Fatal("out-of-scope write must reject")
	}
}
```

- [ ] **Step 2-4:** 實作（`InScope` 檢查；parent 以 `filepath.EvalSymlinks(parentDir)` 驗 canonical、不對目標檔 EvalSymlinks；temp 檔 + `os.Rename` atomic；既有檔先讀算 digest 比 expectedDigest）。Run `go test . -run TestSpecWrite -v` → PASS。

- [ ] **Step 5: Commit**

```bash
git add app.go app_spec_test.go
git commit -m "feat(app): SpecList/Read/Write with atomic rename and optimistic concurrency"
```

---

### Task 10: SubmitForApproval / GateList / GateDecide 綁定

**Files:**
- Modify: `app.go`（wire `gate.Service`＋`spec.NewGitRepo`；Emitter adapter 呼叫 `Manager.EmitWorkspace`）
- Test: `app_gate_test.go`

**Interfaces:**
- Consumes: Task 5 `gate.Service`、Task 6 `GitRepo`、Task 8 `EmitWorkspace`。
- Produces:
  - Emitter adapter：`EmitGateEvent(kind, []gate.Binding, payload)` → 轉 `[]contract.Binding` → `a.manager.EmitWorkspace`。
  - `func (a *App) SubmitForApproval() (approvalID string, err error)`（`BuildCommittedSnapshot` → `Service.Submit`；git identity 缺 → 拒）
  - `func (a *App) GateList() ([]GateEntryDTO, error)`（reconcile ＋ projection ＋ degraded 標示）
  - `func (a *App) GateDecide(approvalID, decision, reason string) error`（approver 取 git identity；缺 → 拒、提示設定）

- [ ] **Step 1: Write the failing test（live-ish，用 temp git workspace）**

```go
func TestGateLiveLoopSubmitApproveThenStale(t *testing.T) {
	a := newTestAppGit(t) // temp git repo workspace + wired gate.Service
	a.SpecWrite("spec/glossary.md", "term v1", "")
	commitAll(t, a) // 用 SpecCommit 或 helper commit 納管
	id, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(id, "approved", "ok"); err != nil {
		t.Fatal(err)
	}
	list, _ := a.GateList()
	if stateOf(list, id) != "active" {
		t.Fatalf("want active after approve")
	}
	a.SpecWrite("spec/glossary.md", "term v2", digestOf(t, a, "spec/glossary.md"))
	commitAll(t, a)
	list, _ = a.GateList() // reconcile 觸發 STALE
	if stateOf(list, id) != "stale" {
		t.Fatalf("want stale after spec change, got %s", stateOf(list, id))
	}
}

func TestGateDecideRejectsWithoutGitIdentity(t *testing.T) {
	a := newTestAppGitNoIdentity(t)
	a.SpecWrite("spec/glossary.md", "x", "")
	commitAll(t, a)
	id, _ := a.SubmitForApproval()
	if err := a.GateDecide(id, "approved", ""); err == nil {
		t.Fatal("missing git identity must reject approval")
	}
}
```

- [ ] **Step 2-4:** 實作 wiring（`GateList` 的 `current` = `func() (string,error){ return spec.BuildCurrentManifest(repo) }`；`SubmitForApproval` 用 `BuildCommittedSnapshot` 回的 digest/baseCommit 組 bindings）。Run `go test . -run TestGate -race -v` → PASS。

- [ ] **Step 5: Commit**

```bash
git add app.go app_gate_test.go
git commit -m "feat(app): Gate 1 bindings — submit/list/decide with STALE reconcile"
```

---

### Task 11: SpecAssist 隔離 one-shot ＋ lifecycle

**Files:**
- Create: `internal/assist/oneshot.go`（provider-agnostic 介面 ＋ claude/codex enforcement）
- Modify: `app.go`（`SpecAssist` 綁定 ＋ lifecycle、txn gate、shutdown reclaim）
- Test: `app_assist_test.go`, `internal/assist/oneshot_test.go`

**Interfaces:**
- Produces:
  - `type Runner interface { Run(ctx, prompt string, sink func(contract.Envelope)) error }`
  - `func NewClaudeAssist(bin, cwd string, env []string) Runner`（argv 含 `--tools ""`）
  - `func NewCodexAssist(...) Runner`（turn 帶 `sandboxPolicy={type:"readOnly",networkAccess:false}`＋`approvalPolicy:"never"`；escalation/approval → fail closed error）
  - `func (a *App) SpecAssist(provider, purpose, prompt string) error`
  - `var ErrAssistActive = errors.New("assist already active for provider")`

- [ ] **Step 1: Write the failing tests（三條 barrier）**

```go
func TestSpecAssistExclusivePerProvider(t *testing.T) {
	a := newTestAppAssist(t, blockingRunner()) // 第一個 run 卡住
	go a.SpecAssist("claude", "spec_assist", "draft")
	waitAssistActive(t, a, "claude")
	if err := a.SpecAssist("claude", "spec_assist", "draft2"); !errors.Is(err, ErrAssistActive) {
		t.Fatalf("second concurrent assist must be rejected: %v", err)
	}
}

func TestShutdownWaitsForAndReclaimsSpecAssist(t *testing.T) {
	done := make(chan struct{})
	a := newTestAppAssist(t, runnerSignaling(done))
	go a.SpecAssist("claude", "spec_assist", "draft")
	waitAssistActive(t, a, "claude")
	a.shutdown(context.Background()) // 必須 cancel + 等 bounded completion + 稽核收尾 → 才 Close
	select {
	case <-done:
	default:
		t.Fatal("shutdown must reclaim (cancel/terminate) the one-shot")
	}
	if a.manager != nil && !a.managerClosed() {
		t.Fatal("Manager.Close must happen after assist reclaimed")
	}
}

func TestCodexAssistCannotEscalateOrMutateSessionView(t *testing.T) {
	fake := fakeCodexEscalatingRunner(t) // 嘗試 approval/escalation + tool write
	a := newTestAppAssist(t, fake)
	var providerEvents int
	a.emitUI = func(name string, data any) { /* 記錄進 provider view 的事件 */ if isProviderView(data) { providerEvents++ } }
	err := a.SpecAssist("codex", "spec_assist", "draft")
	if err == nil {
		t.Fatal("escalation/approval must fail closed")
	}
	if providerEvents != 0 {
		t.Fatal("assist events must not enter provider session view")
	}
	assertWorkspaceUnchanged(t, a) // zero workspace mutation
}
```

- [ ] **Step 2-4:** 實作：
  - `SpecAssist`：`beginAppTxn()`（shutdown 後回錯）；CAS 設 `a.assistActive[provider]`（已存在→`ErrAssistActive`）；配 `correlationID = NewULID`；規格內容注入 prompt；起獨立 one-shot（**不寫 `a.claudeSess`／`a.runner`／`a.codexConn`**）；事件包成 `scope=session, provider, correlation_id, purpose="spec_assist"` 經 Manager 出口，但**前端**依 purpose 分流（Task 13）。
  - once/token 收尾：`sync.Once` per generation；`result`/`abort`/`timeout`/`shutdown` 任一觸發清 `assistActive`＋`endAppTxn`；晚到舊 generation 事件（correlation_id 不符）丟棄並發 `stream_error`（fail loud）。
  - shutdown：snapshot assist cancel funcs → cancel → 等 bounded（如 5s）completion → 才既有 teardown/Close。
  - Codex enforcement：turn params 帶 readOnly+networkAccess:false+approvalPolicy never；收到 approval/escalation request → 立即 fail closed error、terminate thread。
  Run `go test . -run 'TestSpecAssist|TestShutdownWaits|TestCodexAssist' -race -v` ＋ `go test ./internal/assist/ -v` → PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/assist/ app.go app_assist_test.go
git commit -m "feat(app): SpecAssist isolated one-shot with zero-mutation enforcement + lifecycle"
```

---

### Task 12: spec/ 遞迴 watcher（通知層）

**Files:**
- Modify: `app.go`（新增 `watchSpecTree`、shutdown close）
- Test: `app_watch_test.go`

**Interfaces:**
- Consumes: Task 10 `GateList`/reconcile。
- Produces: `func (a *App) watchSpecTree()`（fsnotify 遞迴監看納管樹；debounce；rename/remove 處理；新目錄 re-add；變更 → `Service.ReconcileGate1()` → UI 徽章；close lifecycle；錯誤 fail-loud UI，不影響權威）。

- [ ] **Step 1: Write the failing test**

```go
func TestWatcherTriggersReconcileOnSpecChange(t *testing.T) {
	a := newTestAppGit(t)
	a.SpecWrite("spec/glossary.md", "v1", "")
	commitAll(t, a)
	id, _ := a.SubmitForApproval()
	a.GateDecide(id, "approved", "ok")
	a.watchSpecTree()
	a.SpecWrite("spec/glossary.md", "v2", digestOf(t, a, "spec/glossary.md"))
	commitAll(t, a)
	waitFor(t, 2*time.Second, func() bool {
		l, _ := a.GateList()
		return stateOf(l, id) == "stale"
	})
}
```

- [ ] **Step 2-4:** 實作（遞迴 `filepath.WalkDir` 加 watch；`fsnotify.Create` 為目錄則 re-add；debounce 200ms 後 `ReconcileGate1`；shutdown 時 `watcher.Close()`）。Run `go test . -run TestWatcher -v` → PASS。

- [ ] **Step 5: Commit**

```bash
git add app.go app_watch_test.go
git commit -m "feat(app): recursive spec watcher as STALE notification layer"
```

---

## Phase 6 — 前端

### Task 13: types additive ＋ scope/purpose 分流 ＋ gate/assist store

**Files:**
- Modify: `frontend/src/types.ts`, `frontend/src/App.vue`
- Create: `frontend/src/lib/gateRouting.ts`, `frontend/src/stores/gate.ts`, `frontend/src/stores/assist.ts`
- Test: `frontend/src/lib/gateRouting.test.ts`, `frontend/src/stores/gate.test.ts`

**Interfaces:**
- Produces:
  - types.ts：Envelope 加 `scope?`, `bindings?: ApprovalBinding[]`, `payload?: unknown`, `correlation_id?`, `purpose?`；`ApprovalBinding {kind,ref,digest}`；`GateEntry {approval_id,state,record?,request?}`。
  - `gateRouting.ts`：`function routeEnvelope(env): 'session'|'gate'|'assist'`（workspace→gate；session+purpose=spec_assist→assist；else session）。
  - `stores/gate.ts`：`applyGateEvent(env)`（依 kind 更新 pending/decision/stale projection）。
  - `stores/assist.ts`：`applyAssistEvent(env)`（依 correlation_id 累積草稿）。

- [ ] **Step 1: Write the failing test**

```ts
import { describe, it, expect } from 'vitest'
import { routeEnvelope } from '../lib/gateRouting'

describe('routeEnvelope', () => {
  it('workspace → gate', () => {
    expect(routeEnvelope({ scope: 'workspace', kind: 'gate_request' } as any)).toBe('gate')
  })
  it('session + spec_assist → assist', () => {
    expect(routeEnvelope({ scope: 'session', provider: 'claude', purpose: 'spec_assist' } as any)).toBe('assist')
  })
  it('normal session → session', () => {
    expect(routeEnvelope({ scope: 'session', provider: 'claude', kind: 'message' } as any)).toBe('session')
  })
  it('legacy no-scope → session', () => {
    expect(routeEnvelope({ provider: 'codex', kind: 'message' } as any)).toBe('session')
  })
})
```

- [ ] **Step 2-4:** 實作 `routeEnvelope`；`App.vue` 的 `EventsOn('workbench:event')` 改先 `routeEnvelope` 分流到 `gate.applyGateEvent`／`assist.applyAssistEvent`／`session.apply`。確認 workspace/assist 事件**不**進 `session.apply`（不動 totals/unread/Chat）。Run `npx vitest run gateRouting gate` → PASS。

- [ ] **Step 5: Commit**

```bash
git add frontend/src/types.ts frontend/src/App.vue frontend/src/lib/gateRouting.ts frontend/src/stores/
git commit -m "feat(ui): scope/purpose envelope routing + gate/assist stores"
```

---

### Task 14: Gate 1 主控台 元件

**Files:**
- Create: `frontend/src/components/GateConsole.vue`
- Modify: `frontend/src/App.vue`（掛入右下面板）
- Test: `frontend/src/components/GateConsole.test.ts`

**Interfaces:**
- Consumes: `stores/gate.ts`、Wails `GateList`/`SubmitForApproval`/`GateDecide`。
- Produces: 待辦清單（gate 種類、bindings、狀態徽章 active/stale/pending/superseded）、核可／退回＋理由欄、degraded 停用核可。

- [ ] **Step 1: Write the failing test**

```ts
import { mount } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import GateConsole from './GateConsole.vue'

describe('GateConsole', () => {
  it('reject requires reason', async () => {
    const decide = vi.fn()
    const w = mount(GateConsole, { props: { entries: [{ approval_id: 'A', state: 'pending' }], decide } })
    await w.find('[data-test=reject]').trigger('click')
    expect(decide).not.toHaveBeenCalled() // 無理由不送
    await w.find('[data-test=reason]').setValue('bad')
    await w.find('[data-test=reject]').trigger('click')
    expect(decide).toHaveBeenCalledWith('A', 'rejected', 'bad')
  })
  it('shows stale badge', () => {
    const w = mount(GateConsole, { props: { entries: [{ approval_id: 'A', state: 'stale' }], decide: vi.fn() } })
    expect(w.find('[data-test=badge-A]').text()).toContain('STALE')
  })
})
```

- [ ] **Step 2-4:** 實作元件。Run `npx vitest run GateConsole` → PASS。

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/GateConsole.vue frontend/src/App.vue frontend/src/components/GateConsole.test.ts
git commit -m "feat(ui): Gate 1 console with bindings, badges, reason-gated reject"
```

---

### Task 15: 規格工作區（CodeMirror 6）＋ AI 輔助草稿區

**Files:**
- Modify: `frontend/package.json`（加 `codemirror`, `@codemirror/lang-*` 或自訂 Gherkin），`frontend/src/App.vue`
- Create: `frontend/src/components/SpecWorkspace.vue`
- Test: `frontend/src/components/SpecWorkspace.test.ts`

**Interfaces:**
- Consumes: `SpecList/SpecRead/SpecWrite/PreviewSpecCommit/ConfirmSpecCommit/SpecAssist`、`stores/assist.ts`。
- Produces: 編輯器（CodeMirror 6）、三個 AI 輔助按鈕（呼叫 `SpecAssist(provider,'spec_assist',prompt)`）、草稿區（顯示 assist store、accept → `SpecWrite`）、送核按鈕（`SubmitForApproval`）、SpecCommit 兩階段 UI（preview diff → confirm）。

- [ ] **Step 1: Write the failing test（純邏輯層，避開 CM6 DOM）**

```ts
import { mount } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import SpecWorkspace from './SpecWorkspace.vue'

describe('SpecWorkspace draft accept', () => {
  it('accept writes draft via SpecWrite, not before', async () => {
    const write = vi.fn().mockResolvedValue('sha256:x')
    const w = mount(SpecWorkspace, { props: {
      path: 'spec/glossary.md', draft: 'AI draft content', write,
    }})
    expect(write).not.toHaveBeenCalled() // 草稿不自動寫檔
    await w.find('[data-test=accept-draft]').trigger('click')
    expect(write).toHaveBeenCalledWith('spec/glossary.md', 'AI draft content', expect.any(String))
  })
})
```

- [ ] **Step 2-4:** 實作元件（CM6 以 `onMounted` 動態 import，測試走 props 邏輯層）。Run `npx vitest run SpecWorkspace` → PASS。`npm run build`（frontend）綠。

- [ ] **Step 5: Commit**

```bash
git add frontend/package.json frontend/package-lock.json frontend/src/components/SpecWorkspace.vue frontend/src/App.vue frontend/src/components/SpecWorkspace.test.ts
git commit -m "feat(ui): spec workspace (CodeMirror 6) with AI-assist draft area"
```

---

### Task 16: 表示圖層（重用 Mermaid strict）

**Files:**
- Create: `frontend/src/components/DiagramPane.vue`
- Modify: `frontend/src/App.vue`
- Test: `frontend/src/components/DiagramPane.test.ts`

**Interfaces:**
- Consumes: `SpecList/SpecRead`（`spec/context-map/*.mmd`）、既有 `PreviewPane` 的 mermaid strict 設定。
- Produces: 監看 `context-map/`（`diagram:changed` 事件或 `SpecList` 輪詢）自動重渲染；只瀏覽／監看／重渲染。

- [ ] **Step 1: Write the failing test**

```ts
import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
vi.mock('mermaid', () => ({ default: { initialize: vi.fn(), render: vi.fn().mockResolvedValue({ svg: '<svg/>' }) } }))
import DiagramPane from './DiagramPane.vue'

describe('DiagramPane', () => {
  it('renders context-map mmd on load', async () => {
    const read = vi.fn().mockResolvedValue(['graph TD; A-->B', 'sha256:x'])
    const w = mount(DiagramPane, { props: { path: 'spec/context-map/c4.mmd', read } })
    await flushPromises()
    expect(w.html()).toContain('svg')
  })
})
```

- [ ] **Step 2-4:** 實作（複用 `PreviewPane` 的 `mermaid.initialize({securityLevel:'strict'})` 與 render 流程）。Run `npx vitest run DiagramPane` → PASS。

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/DiagramPane.vue frontend/src/App.vue frontend/src/components/DiagramPane.test.ts
git commit -m "feat(ui): context-map diagram pane reusing strict mermaid"
```

---

## Phase 7 — 收尾 gate（全套回歸 ＋ live 閉環）

### Task 17: 全套回歸 ＋ live 閉環驗收

**Files:** 無新 code；驗證與文件。

- [ ] **Step 1: Go 全套 `-race`**

Run: `go vet ./... && go test -race ./... -count=1`
Expected: 全 package ok（含既有 8 package ＋ 新 spec/gate/assist）。

- [ ] **Step 2: 前端全套 ＋ build**

Run: `cd frontend && npx vitest run && npm run build`
Expected: 既有 44 ＋ 新測試全綠；build 成功。

- [ ] **Step 3: Wails 封裝 build**

Run: `wails build`（或 repo 既有 build script）
Expected: 成功產出 app。

- [ ] **Step 4: live 閉環（owner-run，對照 spec §7）**

手動：app 內編輯 `spec/glossary.md` → SpecCommit（preview diff → confirm）→ SubmitForApproval → Gate 主控台核可 → 改 `spec/context-map/*.mmd` → 觀察 STALE 徽章亮 ＋ `.workbench/` journal 有對應 `gate_op`（gate_request/approval_record/transition）＋ `event_id` 單調。記錄進 `docs/spikes/m2-results.md`。

- [ ] **Step 5: Commit 結果**

```bash
git add docs/spikes/m2-results.md
git commit -m "docs(m2): acceptance results — Stage A live loop + full regression"
```

---

## Self-Review（對照 spec）

- **§1.2 納管範圍** → Task 1（scope patterns/version 進 canonical）。
- **§3.1 記錄型別 / §3.5 durable ownership** → Task 2, 5（gate_request pending 重建、supersede 同交易）。
- **§3.2 gate_op 單交易 ＋ tail 修復** → Task 3, 5。
- **§3.3 STALE 重算持久化 / read-error fail closed / ErrConcurrentModification / EmitWorkspace 失敗不回滾** → Task 4, 5。
- **§3.4a committed snapshot 只讀 HEAD tree / 兩階段 SpecCommit** → Task 6, 7。
- **§3.4b manifest 演算法 / symlink 拒絕** → Task 1, 6。
- **§3.4c workspace event JSON / scope / bindings 頂層 / digest 格式 / 前端分流** → Task 8, 13。
- **§5.1 SpecAssist 隔離 one-shot ＋ enforcement ＋ lifecycle** → Task 11。
- **§5.1 SpecWrite atomic/expected_digest** → Task 9。
- **§5.2 表示圖層** → Task 16。
- **§5.3 Gate 主控台 / degraded 停用** → Task 14。
- **§5.4 git identity fail closed** → Task 10。
- **§4 watcher 通知層** → Task 12。
- **§7 驗證（barrier/live/全套）** → Task 5, 11, 17，＋各 task 對應命名測試。

**Placeholder scan**：無 TBD；UI 任務的 CM6 DOM 以 props 邏輯層測試繞開（明示）。`Repo` 真實實作與 go-git/cat-file 選擇於 Task 6 由執行者定並記 commit。

**Type consistency**：`gate.Binding`（domain）與 `contract.Binding`（envelope）為兩型，Task 10 Emitter adapter 明確轉換；`ManifestDigest`/`BuildCurrentManifest`/`BuildCommittedSnapshot`/`ReconcileGate1`/`EmitWorkspace`/`routeEnvelope` 跨 task 命名一致。
