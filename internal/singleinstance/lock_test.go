package singleinstance

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 注意：這個檔案守的是 Acquire 的**本機**契約（fail closed、殘留 lock file
// 可重用、釋放後可再取得）。跨 process 那一維——「兩個真的 OS process 同時
// 啟動只有一個進得去」「SIGKILL 後 kernel 自然釋放」——量不出來，只有真的
// spawn 出第二個 process 才驗得到，見 repo 根目錄的
// main_singleinstance_test.go。

func TestAcquireThenReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatalf("第一次 Acquire 必須成功：%v", err)
	}
	if want := filepath.Join(dir, LockFileName); l.Path() != want {
		t.Fatalf("鎖檔路徑必須是 <stateDir>/%s：want %s got %s", LockFileName, want, l.Path())
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release：%v", err)
	}
	l2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("釋放後必須能再取得：%v", err)
	}
	_ = l2.Release()
}

// TestAcquireRejectsWhileHeld：flock 的持有者是 open file description，不是
// process，所以同一個 process 內第二次 Acquire 也必須被拒——這條只證明「鎖真
// 的是獨佔的」，不是跨 process 守門（見檔頭）。
func TestAcquireRejectsWhileHeld(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()

	l2, err := Acquire(dir)
	if err == nil {
		_ = l2.Release()
		t.Fatal("鎖已被持有時 Acquire 必須失敗")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("被佔用必須回 ErrAlreadyRunning（呼叫端據此分辨拒絕 UX）：%v", err)
	}
}

// TestHeldIsCapabilityEvidence：Held/StateDir 是呼叫端拿來當 capability 判準
// 的東西（見 app.go 的 stateLease），所以三種「不是真鎖」的值都必須是 false。
func TestHeldIsCapabilityEvidence(t *testing.T) {
	dir := t.TempDir()
	var nilLock *Lock
	if nilLock.Held() || nilLock.StateDir() != "" {
		t.Fatal("nil 不得被當成持有中的鎖")
	}
	forged := &Lock{}
	if forged.Held() || forged.StateDir() != "" {
		t.Fatal("零值（別的 package 造得出來）不得被當成持有中的鎖")
	}
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Held() || l.StateDir() != dir {
		t.Fatalf("Acquire 取得的鎖必須 Held 且記得 state directory：held=%v dir=%q", l.Held(), l.StateDir())
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if l.Held() {
		t.Fatal("釋放之後 Held 必須為 false——已釋放的鎖不能再授權任何寫入")
	}
	if err := l.Release(); err != nil {
		t.Fatalf("重複 Release 必須安全（shutdown 可能跑兩次）：%v", err)
	}
}

// TestReleaseKeepsLockFile：crash 之後磁碟上會留著這個檔案，Release 也刻意
// 留著——unlink 會讓下一個 process 在新 inode 上拿到第二把鎖。
func TestReleaseKeepsLockFile(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, LockFileName)); err != nil {
		t.Fatalf("Release 不得刪除 lock file：%v", err)
	}
}

// TestAcquireFailsClosedOnMissingDir：state directory 不存在 → 開檔失敗。
// 必須回錯誤而不是「沒人持鎖，放行」。
func TestAcquireFailsClosedOnMissingDir(t *testing.T) {
	l, err := Acquire(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		_ = l.Release()
		t.Fatal("開檔失敗必須 fail closed（回錯誤），不得放行")
	}
	if errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("開檔失敗不是「已在執行中」，不能共用同一個拒絕 UX：%v", err)
	}
}

// TestAcquireFailsClosedOnUnwritableDir：目錄不可寫（權限）→ 同樣 fail closed。
func TestAcquireFailsClosedOnUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 會繞過目錄權限檢查，這條量不出來")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	l, err := Acquire(dir)
	if err == nil {
		_ = l.Release()
		t.Fatal("權限不足必須 fail closed（回錯誤），不得當成「目前沒人持鎖」")
	}
	if errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("權限錯誤不是「已在執行中」：%v", err)
	}
}
