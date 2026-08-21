package wirelog

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/recorder"
)

// buildGen：一份寫了兩個 session 的 frame、已 finalize 的 generation。
func buildGen(t *testing.T, dir, id string) map[string][]int {
	t.Helper()
	owners := []string{"w-a", "", "w-b", "w-a"}
	i := 0
	g, err := NewGeneration(dir, id, func(Direction, []byte) string {
		w := owners[i]
		i++
		return w
	})
	if err != nil {
		t.Fatal(err)
	}
	for range owners {
		if lerr := g.Line(DirServerToClient, []byte(`{"method":"agent/message/delta"}`)); lerr != nil {
			t.Fatal(lerr)
		}
	}
	want := g.FrameIndex().AttributionByWSID()
	if err := g.Finalize(recorder.Meta{Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	return want
}

// TestFinalizeWritesSidecar：契約第 4 條前半——收尾當下直接產生 compact index，
// 不必事後把整份錄流重讀一遍。
func TestFinalizeWritesSidecar(t *testing.T) {
	dir := t.TempDir()
	want := buildGen(t, dir, "g1")
	b, err := os.ReadFile(AttributionPath(dir, "g1"))
	if err != nil {
		t.Fatalf("finalize 必須直接寫出 sidecar：%v", err)
	}
	if len(b) == 0 {
		t.Fatal("sidecar 不得是空檔")
	}
	res, err := LoadOrBuildAttribution(dir, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.FromSidecar {
		t.Fatal("sidecar 存在時必須走快路徑，不得重讀錄流")
	}
	if !reflect.DeepEqual(res.ByWSID, want) {
		t.Fatalf("sidecar 內容必須與記憶體索引一致：%+v（want %+v）", res.ByWSID, want)
	}
}

// TestUnreadableSidecarFallsBackAndIsRepaired：契約第 4 條後半的**壞檔**那一半。
// sidecar 是可再生的衍生檔、不是證據，所以解不開時不得 fail loud——必須回頭讀錄流
// （唯一權威），並把壞掉的那份覆蓋掉，下次才不會每次都重讀。
//
// G5（App 層）走的是「sidecar 不存在」，這條走「存在但解不開」，兩者是不同分支。
func TestUnreadableSidecarFallsBackAndIsRepaired(t *testing.T) {
	dir := t.TempDir()
	want := buildGen(t, dir, "g1")
	if err := os.WriteFile(AttributionPath(dir, "g1"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := LoadOrBuildAttribution(dir, "g1")
	if err != nil {
		t.Fatalf("sidecar 壞掉不得致命（錄流才是權威）：%v", err)
	}
	if res.FromSidecar {
		t.Fatal("解不開的 sidecar 不得被當成命中")
	}
	if !reflect.DeepEqual(res.ByWSID, want) {
		t.Fatalf("fallback 的結果必須與原索引一致：%+v（want %+v）", res.ByWSID, want)
	}
	again, err := LoadOrBuildAttribution(dir, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if !again.FromSidecar {
		t.Fatal("壞掉的 sidecar 必須被補建覆蓋，否則每一次都要重讀整份錄流")
	}
	if !reflect.DeepEqual(again.ByWSID, want) {
		t.Fatalf("補建後的 sidecar 內容不符：%+v", again.ByWSID)
	}
}

// TestMissingSidecarIsBackfilled：sidecar 不存在（舊資料／被刪）→ 重讀錄流並補建。
func TestMissingSidecarIsBackfilled(t *testing.T) {
	dir := t.TempDir()
	want := buildGen(t, dir, "g1")
	if err := os.Remove(AttributionPath(dir, "g1")); err != nil {
		t.Fatal(err)
	}
	res, err := LoadOrBuildAttribution(dir, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if res.FromSidecar || !reflect.DeepEqual(res.ByWSID, want) {
		t.Fatalf("缺 sidecar 必須 fallback 重建：%+v", res)
	}
	if _, serr := os.Stat(AttributionPath(dir, "g1")); serr != nil {
		t.Fatalf("重建之後必須補建 sidecar：%v", serr)
	}
}

// TestTruncatedTailSurvivesIntoResult：由**截斷**的錄流建出來的歸屬是不完整的答案，
// 而 sidecar 會把它變成後續每一次 app run 的快路徑。截斷量必須一路傳到結果裡（呼叫端
// 才有東西可以送進稽核），而且**經過 sidecar 之後仍然帶著**——否則第二次以後就查無此事。
func TestTruncatedTailSurvivesIntoResult(t *testing.T) {
	dir := t.TempDir()
	buildGen(t, dir, "g1")
	if err := os.Remove(AttributionPath(dir, "g1")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "g1.jsonl")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(b, []byte(`{"frame":9,"dir":"s2c"`)...), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := LoadOrBuildAttribution(dir, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if res.TruncatedTailBytes == 0 {
		t.Fatal("重建時的檔尾截斷必須傳到結果裡，否則呼叫端沒有東西可以送進稽核")
	}
	again, err := LoadOrBuildAttribution(dir, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if !again.FromSidecar {
		t.Fatal("precondition：第二次必須走 sidecar")
	}
	if again.TruncatedTailBytes != res.TruncatedTailBytes {
		t.Fatalf("截斷量必須存活過 sidecar：%d（want %d）", again.TruncatedTailBytes, res.TruncatedTailBytes)
	}
}

// TestSidecarHitDoesNotReadWireLog：契約 4 的**效益**那一半——sidecar 存在時必須真的
// 省掉整份錄流的讀取，不只是「回報說省掉了」。
//
// **oracle 不是 FromSidecar**（失效形狀 H）：那是受測對象的自陳。這裡數的是
// RebuildFrameIndex 實際被呼叫幾次——review 實測過一個「照樣重讀一遍、但仍回
// FromSidecar:true」的 mutation 可以讓 App 層的稽核式斷言全綠。
func TestSidecarHitDoesNotReadWireLog(t *testing.T) {
	dir := t.TempDir()
	buildGen(t, dir, "g1")

	var reads []string
	hookRebuild = func(p string) { reads = append(reads, p) }
	t.Cleanup(func() { hookRebuild = nil })

	if _, err := LoadOrBuildAttribution(dir, "g1"); err != nil {
		t.Fatal(err)
	}
	if len(reads) != 0 {
		t.Fatalf("sidecar 命中時不得讀整份錄流（契約 4 的效益就是這個）：%v", reads)
	}

	// 對照組：sidecar 拿掉就必須真的讀——否則上面那個 0 是因為根本沒接上探針。
	if err := os.Remove(AttributionPath(dir, "g1")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrBuildAttribution(dir, "g1"); err != nil {
		t.Fatal(err)
	}
	if len(reads) != 1 {
		t.Fatalf("precondition：缺 sidecar 必須重讀一次，探針才算真的接上：%v", reads)
	}
}
