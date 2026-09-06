package proc

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ---- B2c-6：process-group oracle 有界輪詢（B2c-4 裁定記錄 D4：只有 ESRCH
// 算群組消失；EPERM／其他 errno 記錄但繼續輪詢，不偽裝成 gone） ----

// pollGroupGone 每輪先 probe：ESRCH 立即回報 gone；nil／EPERM／其他 errno 記錄
// 進 seq 並計數 EPERM，再判斷 deadline；還有時間才 sleep(min(backoff, remaining))，
// backoff 序列 1/2/4/8/16/32/64 ms 之後固定 64 ms。
//
//nolint:staticcheck // ST1008: 回傳順序（gone, epermCount, last, seq）是 B2c-6 design gate 核准的固定形狀，四套件須逐字一致
func pollGroupGone(probe func() error, deadline time.Time, now func() time.Time, sleep func(time.Duration)) (gone bool, epermCount int, last error, seq []string) {
	const maxBackoff = 64 * time.Millisecond
	backoff := time.Millisecond
	for {
		err := probe()
		last = err
		seq = append(seq, errnoToken(err))
		if errors.Is(err, syscall.ESRCH) {
			return true, epermCount, last, seq
		}
		if errors.Is(err, syscall.EPERM) {
			epermCount++
		}
		if !now().Before(deadline) {
			return false, epermCount, last, seq
		}
		d := backoff
		if remaining := deadline.Sub(now()); d > remaining {
			d = remaining
		}
		sleep(d)
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// errnoToken 把 probe 的錯誤正規化成人類可讀 token，供 requireGroupGone 的失敗
// 訊息與 table test 斷言使用。
func errnoToken(err error) string {
	switch {
	case err == nil:
		return "nil"
	case errors.Is(err, syscall.ESRCH):
		return "ESRCH"
	case errors.Is(err, syscall.EPERM):
		return "EPERM"
	case errors.Is(err, syscall.EINVAL):
		return "EINVAL"
	default:
		return err.Error()
	}
}

// groupOracleSnapshot 直接執行 ps（不經 shell），依第二欄 PGID 在 Go 內篩選——
// `ps -g` 在 Linux procps 對純數字選的是 session、非 process group，不可用。
func groupOracleSnapshot(pgid int) string {
	out, err := exec.Command("ps", "-Ao", "pid=,pgid=,stat=,command=").CombinedOutput()
	if err != nil {
		return fmt.Sprintf("<ps failed: %v>\n%s", err, out)
	}
	want := strconv.Itoa(pgid)
	var rows []string
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != want {
			continue
		}
		rows = append(rows, line)
	}
	if len(rows) == 0 {
		return fmt.Sprintf("<no rows for pgid %d>", pgid)
	}
	return strings.Join(rows, "\n")
}

// requireGroupGone 是 production 呼叫點用的外層：2 s deadline、真實時鐘，失敗
// 附 errno 序列與 ps 快照（B2c-6 D2）。
func requireGroupGone(t *testing.T, pgid int, context string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	probe := func() error { return syscall.Kill(-pgid, 0) }
	gone, epermCount, last, seq := pollGroupGone(probe, deadline, time.Now, time.Sleep)
	if !gone {
		t.Fatalf("%s: process group %d not gone within 2s: errno sequence=%v (EPERM=%d, last=%v); ps rows for pgid:\n%s",
			context, pgid, seq, epermCount, last, groupOracleSnapshot(pgid))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPollGroupGoneTableCases 五案（design gate rev2 D1）：fake clock 只由 fake
// sleep 推進，不真的睡、不忙迴圈。
func TestPollGroupGoneTableCases(t *testing.T) {
	t.Run("eperm_eperm_esrch_gone", func(t *testing.T) {
		errs := []error{syscall.EPERM, syscall.EPERM, syscall.ESRCH}
		i := 0
		probe := func() error {
			e := errs[i]
			i++
			return e
		}
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		var sleeps []time.Duration
		sleepFn := func(d time.Duration) {
			sleeps = append(sleeps, d)
			clock = clock.Add(d)
		}
		deadline := clock.Add(2 * time.Second)

		gone, epermCount, last, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if !gone {
			t.Fatal("want gone=true")
		}
		if epermCount != 2 {
			t.Fatalf("epermCount = %d, want 2", epermCount)
		}
		if !errors.Is(last, syscall.ESRCH) {
			t.Fatalf("last = %v, want ESRCH", last)
		}
		if want := []string{"EPERM", "EPERM", "ESRCH"}; !equalStrings(tokens, want) {
			t.Fatalf("seq = %v, want %v", tokens, want)
		}
		if want := []time.Duration{time.Millisecond, 2 * time.Millisecond}; !equalDurations(sleeps, want) {
			t.Fatalf("sleeps = %v, want %v", sleeps, want)
		}
	})

	t.Run("nil_expires_at_deadline_not_gone", func(t *testing.T) {
		probeCount := 0
		probe := func() error {
			probeCount++
			return nil
		}
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		var sleeps []time.Duration
		sleepFn := func(d time.Duration) {
			sleeps = append(sleeps, d)
			clock = clock.Add(d)
		}
		deadline := clock.Add(2 * time.Second)

		gone, epermCount, last, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if gone {
			t.Fatal("want gone=false")
		}
		if epermCount != 0 {
			t.Fatalf("epermCount = %d, want 0", epermCount)
		}
		if last != nil {
			t.Fatalf("last = %v, want nil", last)
		}
		wantSleeps := []time.Duration{}
		for _, ms := range []int{1, 2, 4, 8, 16, 32} {
			wantSleeps = append(wantSleeps, time.Duration(ms)*time.Millisecond)
		}
		for range 30 {
			wantSleeps = append(wantSleeps, 64*time.Millisecond)
		}
		wantSleeps = append(wantSleeps, 17*time.Millisecond) // 2000 - (63 + 64*30) = 17 的截短值
		if !equalDurations(sleeps, wantSleeps) {
			t.Fatalf("sleeps = %v (len=%d), want %v (len=%d)", sleeps, len(sleeps), wantSleeps, len(wantSleeps))
		}
		if probeCount != len(tokens) {
			t.Fatalf("probeCount = %d, len(seq) = %d, want equal", probeCount, len(tokens))
		}
		if len(tokens) != 38 {
			t.Fatalf("len(seq) = %d, want 38 (37 sleeps + 1 final probe after deadline)", len(tokens))
		}
		for _, tok := range tokens {
			if tok != "nil" {
				t.Fatalf("seq contains non-nil token %q: %v", tok, tokens)
			}
		}
	})

	t.Run("eperm_expires_at_deadline_not_gone", func(t *testing.T) {
		probeCount := 0
		probe := func() error {
			probeCount++
			return syscall.EPERM
		}
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		sleepFn := func(d time.Duration) { clock = clock.Add(d) }
		deadline := clock.Add(2 * time.Second)

		gone, epermCount, last, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if gone {
			t.Fatal("want gone=false")
		}
		if !errors.Is(last, syscall.EPERM) {
			t.Fatalf("last = %v, want EPERM", last)
		}
		if epermCount != probeCount {
			t.Fatalf("epermCount = %d, probeCount = %d, want equal", epermCount, probeCount)
		}
		if len(tokens) != probeCount {
			t.Fatalf("len(seq) = %d, probeCount = %d, want equal", len(tokens), probeCount)
		}
	})

	t.Run("immediate_esrch_zero_sleeps", func(t *testing.T) {
		probe := func() error { return syscall.ESRCH }
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		sleepCalls := 0
		sleepFn := func(time.Duration) { sleepCalls++ }
		deadline := clock.Add(2 * time.Second)

		gone, epermCount, last, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if !gone {
			t.Fatal("want gone=true")
		}
		if sleepCalls != 0 {
			t.Fatalf("sleepCalls = %d, want 0", sleepCalls)
		}
		if epermCount != 0 {
			t.Fatalf("epermCount = %d, want 0", epermCount)
		}
		if !errors.Is(last, syscall.ESRCH) {
			t.Fatalf("last = %v, want ESRCH", last)
		}
		if want := []string{"ESRCH"}; !equalStrings(tokens, want) {
			t.Fatalf("seq = %v, want %v", tokens, want)
		}
	})

	t.Run("mixed_errno_sequence_normalizes_tokens", func(t *testing.T) {
		errs := []error{nil, syscall.EPERM, syscall.EINVAL, syscall.ESRCH}
		i := 0
		probe := func() error {
			e := errs[i]
			i++
			return e
		}
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		sleepFn := func(d time.Duration) { clock = clock.Add(d) }
		deadline := clock.Add(2 * time.Second)

		gone, _, _, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if !gone {
			t.Fatal("want gone=true")
		}
		if want := []string{"nil", "EPERM", "EINVAL", "ESRCH"}; !equalStrings(tokens, want) {
			t.Fatalf("seq = %v, want %v", tokens, want)
		}
	})
}
