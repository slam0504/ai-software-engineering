package domainspec

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// replaceGroup 以群組 key 為界整段替換：decode 成 map[string]json.RawMessage →
// 覆寫該 key → 依固定 key 序重組，比 regexp 可靠且 deterministic（plan rev3 建議）。
func replaceGroup(snapshotJSON, key, replacement string) string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(snapshotJSON), &raw); err != nil {
		panic(fmt.Sprintf("replaceGroup: invalid json: %v", err))
	}
	raw[key] = json.RawMessage(replacement)
	order := []string{
		"schema_version", "evaluation_phase",
		"decision", "reason", "approver", "entry", "request",
		"current", "base_commit_state", "plan", "risk_policy",
		"risk_selections", "escalations",
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		v, ok := raw[k]
		if !ok {
			continue
		}
		parts = append(parts, `"`+k+`":`+string(v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// wrapper 形式的合法 decide/approved snapshot（各 task 測試共用底稿）。
// plan rev3：帶齊五種 required binding（R8）且 current 四值與 bound digest 相符
// （R11）——否則 Task 5 正式 bundle 下不可能得到 truth=true 的 clean 基準。
func validSnapshotJSON() string {
	z64 := strings.Repeat("0", 64)
	a40 := strings.Repeat("a", 40)
	return `{
      "schema_version": 1, "evaluation_phase": "decide",
      "decision": {"presence":"known","value":"approved"},
      "reason": {"presence":"known","value":""},
      "approver": {"presence":"known","value":{"name":"u","email":"u@x"}},
      "entry": {"presence":"known","value":{"exists":true,"has_request":true,"has_record":false}},
      "request": {"presence":"known","value":{"gate":"gate2","subject":"plan:P1","bindings":[
        {"kind":"spec_manifest","role":"","ref":"","digest":"sha256:` + z64 + `"},
        {"kind":"plan","role":"","ref":"","digest":"sha256:` + z64 + `"},
        {"kind":"base_commit","role":"","ref":"HEAD","digest":"git:sha1:` + a40 + `"},
        {"kind":"risk_policy","role":"","ref":"","digest":"sha256:` + z64 + `"},
        {"kind":"permission_manifest","role":"","ref":"","digest":"sha256:` + z64 + `"}]}},
      "current": {"presence":"known","value":{"spec_manifest":"sha256:` + z64 + `","plan_manifest":"sha256:` + z64 + `","risk_policy":"sha256:` + z64 + `","permission_manifest":"sha256:` + z64 + `"}},
      "base_commit_state": {"presence":"known","value":"ok"},
      "plan": {"presence":"known","value":{"tasks":[{"id":"T1","source_index":0,"minimum_risk_tier":"low","planner_risk_tier":"low","impact":{"contexts":["gate"],"modules":[]}}]}},
      "risk_policy": {"presence":"known","value":{"default_tier":"low","rules":[{"match":{"contexts":["gate"],"modules":[]},"tier":"low"}]}},
      "risk_selections": {"presence":"known","value":[{"task_id":"T1","selected_risk_tier":"low","override_reason":""}]},
      "escalations": {"presence":"known","value":[]}
    }`
}

// 合法 submit snapshot（plan rev3——matrix 測試不得從 decide 底稿多重替換拼裝，
// 否則可能因其他欄位違規而誤通過）：decision 群組四項 not_applicable、request known。
func validSubmitSnapshotJSON() string {
	z64 := strings.Repeat("0", 64)
	a40 := strings.Repeat("a", 40)
	na := `{"presence":"not_applicable","value":null}`
	return `{
      "schema_version": 1, "evaluation_phase": "submit",
      "decision": ` + na + `, "reason": ` + na + `, "approver": ` + na + `, "entry": ` + na + `,
      "request": {"presence":"known","value":{"gate":"gate2","subject":"plan:P1","bindings":[
        {"kind":"spec_manifest","role":"","ref":"","digest":"sha256:` + z64 + `"},
        {"kind":"plan","role":"","ref":"","digest":"sha256:` + z64 + `"},
        {"kind":"base_commit","role":"","ref":"HEAD","digest":"git:sha1:` + a40 + `"},
        {"kind":"risk_policy","role":"","ref":"","digest":"sha256:` + z64 + `"},
        {"kind":"permission_manifest","role":"","ref":"","digest":"sha256:` + z64 + `"}]}},
      "current": ` + na + `, "base_commit_state": ` + na + `,
      "plan": ` + na + `, "risk_policy": ` + na + `,
      "risk_selections": ` + na + `, "escalations": ` + na + `
    }`
}

// 合法 decide/rejected snapshot（plan rev3）：decision="rejected"、reason 非空、
// current/base_commit_state/plan/risk_policy 四項 not_applicable，其餘 known。
func validRejectedSnapshotJSON() string {
	j := validSnapshotJSON()
	j = strings.Replace(j, `"value":"approved"`, `"value":"rejected"`, 1)
	j = strings.Replace(j, `"reason": {"presence":"known","value":""}`,
		`"reason": {"presence":"known","value":"not good enough"}`, 1)
	na := `{"presence":"not_applicable","value":null}`
	for _, k := range []string{"current", "base_commit_state", "plan", "risk_policy"} {
		j = replaceGroup(j, k, na) // helper：以群組 key 為界整段替換（實作為 regexp `"<k>": \{.*?\}\}?` 或以 json round-trip 改欄位後重排）
	}
	// replaceGroup 重組整份 JSON 時一律輸出 `"key":value`（無空白，見 replaceGroup
	// 實作），故這裡改回 rejected 的空 risk_selections 也要用同一種無空白格式
	// 比對，否則 strings.Replace 靜默無 match、risk_selections 留著非空值——
	// 一個「合法 rejected snapshot」意外帶著會觸發 R21 的 risk_selections（只有
	// 真的跑 Evaluate 才會現形，先前消費端都沒有觸發）。
	j = replaceGroup(j, "risk_selections", `{"presence":"known","value":[]}`)
	return j
}

func mustSnapshot(t *testing.T) *FactsSnapshot {
	t.Helper()
	s, err := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
	if err != nil {
		t.Fatalf("decode valid: %v", err)
	}
	return s
}

func TestDecodeFactsSnapshotValid(t *testing.T) {
	s := mustSnapshot(t)
	if s.EvaluationPhase != "decide" || s.Plan.Value == nil || s.Plan.Value.Tasks[0].SourceIndex != 0 {
		t.Fatalf("unexpected snapshot: %+v", s)
	}
}

func TestDecodeFactsSnapshotRejectsUnknownField(t *testing.T) {
	for _, inject := range []struct{ old, new string }{
		{`"schema_version": 1`, `"schema_version": 1, "bogus": true`},                            // 頂層
		{`"presence":"known","value":"approved"`, `"presence":"known","value":"approved","x":1`}, // wrapper 內層
	} {
		j := strings.Replace(validSnapshotJSON(), inject.old, inject.new, 1)
		if _, err := DecodeFactsSnapshot([]byte(j)); err == nil {
			t.Fatalf("unknown field must be rejected: %s", inject.new)
		}
	}
}

func TestDecodeFactsSnapshotWrapperInvariant(t *testing.T) {
	// presence=known 但 value=null → 拒；presence=not_applicable 但 value 非 null → 拒
	for _, bad := range []struct{ old, new string }{
		{`"plan": {"presence":"known","value":{"tasks":[{"id":"T1","source_index":0,"minimum_risk_tier":"low","planner_risk_tier":"low","impact":{"contexts":["gate"],"modules":[]}}]}}`,
			`"plan": {"presence":"known","value":null}`},
		{`"escalations": {"presence":"known","value":[]}`,
			`"escalations": {"presence":"not_applicable","value":[]}`},
	} {
		j := strings.Replace(validSnapshotJSON(), bad.old, bad.new, 1)
		if _, err := DecodeFactsSnapshot([]byte(j)); err == nil {
			t.Fatalf("wrapper invariant must reject: %s", bad.new)
		}
	}
}

func TestDecodeFactsSnapshotPresenceMatrix(t *testing.T) {
	// 各案例都從「該欄位形狀完全合法」的底稿出發、只翻一個欄位（plan rev3——
	// 多重替換拼裝可能因其他違規誤通過）。
	// decide/rejected 合法底稿 → 只把 plan 翻回 known → 必拒
	j := replaceGroup(validRejectedSnapshotJSON(), "plan",
		`{"presence":"known","value":{"tasks":[]}}`)
	if _, err := DecodeFactsSnapshot([]byte(j)); err == nil {
		t.Fatal("rejected with plan=known must violate presence matrix")
	}
	// submit 合法底稿 → 只把 request 翻 not_applicable → 必拒
	j2 := replaceGroup(validSubmitSnapshotJSON(), "request",
		`{"presence":"not_applicable","value":null}`)
	if _, err := DecodeFactsSnapshot([]byte(j2)); err == nil {
		t.Fatal("submit with request=not_applicable must be rejected（R5.submit–R9 需要 request facts）")
	}
	// decide/approved：plan=missing 合法（本應 known 而缺 → unknown 路徑）
	j3 := replaceGroup(validSnapshotJSON(), "plan", `{"presence":"missing","value":null}`)
	if _, err := DecodeFactsSnapshot([]byte(j3)); err != nil {
		t.Fatalf("missing in a known-column must be legal: %v", err)
	}
}

func TestDecodeFactsSnapshotInvalidDecisionColumn(t *testing.T) {
	// rev3：decide/invalid 欄——R1 隔離案例的合法輸入形狀
	j := validSnapshotJSON()
	j = strings.Replace(j, `"value":"approved"`, `"value":"weird"`, 1)
	na := `{"presence":"not_applicable","value":null}`
	for _, k := range []string{"current", "base_commit_state", "plan", "risk_policy"} {
		j = replaceGroup(j, k, na)
	}
	if _, err := DecodeFactsSnapshot([]byte(j)); err != nil {
		t.Fatalf("invalid-decision snapshot must be decodable（R1 隔離輸入）: %v", err)
	}
}

func TestDecodeFactsSnapshotR4RequestException(t *testing.T) {
	// rev3：entry 非 pending → request 允許 not_applicable（service.go:98-101 先敗）
	j := replaceGroup(validSnapshotJSON(), "entry",
		`{"presence":"known","value":{"exists":false,"has_request":false,"has_record":false}}`)
	j = replaceGroup(j, "request", `{"presence":"not_applicable","value":null}`)
	na := `{"presence":"not_applicable","value":null}`
	for _, k := range []string{"current", "base_commit_state", "plan", "risk_policy"} {
		j = replaceGroup(j, k, na)
	}
	if _, err := DecodeFactsSnapshot([]byte(j)); err != nil {
		t.Fatalf("entry-absent with request=not_applicable must be legal（R4 路徑）: %v", err)
	}
}

func TestDecodeFactsSnapshotRejectsBadPhaseAndVersion(t *testing.T) {
	for _, bad := range []struct{ old, new string }{
		{`"evaluation_phase": "decide"`, `"evaluation_phase": "runtime"`},
		{`"schema_version": 1`, `"schema_version": 2`},
	} {
		j := strings.Replace(validSnapshotJSON(), bad.old, bad.new, 1)
		if _, err := DecodeFactsSnapshot([]byte(j)); err == nil {
			t.Fatalf("must reject %s", bad.new)
		}
	}
}

func TestDecodeFactsSnapshotRejectsBadSourceIndex(t *testing.T) {
	task := func(id string, idx int) string {
		return `{"id":"` + id + `","source_index":` + strconv.Itoa(idx) +
			`,"minimum_risk_tier":"low","planner_risk_tier":"low","impact":{"contexts":[],"modules":[]}}`
	}
	for name, tasks := range map[string]string{
		"swapped":        "[" + task("T1", 1) + "," + task("T2", 0) + "]",
		"duplicate":      "[" + task("T1", 0) + "," + task("T2", 0) + "]",
		"non-contiguous": "[" + task("T1", 0) + "," + task("T2", 2) + "]",
	} {
		j := replaceGroup(validSnapshotJSON(), "plan",
			`{"presence":"known","value":{"tasks":`+tasks+`}}`)
		if _, err := DecodeFactsSnapshot([]byte(j)); err == nil {
			t.Fatalf("%s source_index must be rejected（tasks[i].source_index == i）", name)
		}
	}
}
