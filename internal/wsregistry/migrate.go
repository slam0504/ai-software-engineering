package wsregistry

import (
	"fmt"
	"time"
)

// LegacyEntry：M3a 時代 restore.json 的 per-provider 一筆記錄，轉換後餵給
// Migrate。轉換（讀 restore.json → map[string]LegacyEntry）由呼叫端負責
// （Task 6），本檔不碰檔案。
type LegacyEntry struct {
	ViewStartEventID string
	ResumeSessionID  string
	TaskID           string
}

// providerMigrationOrder：決定性走訪順序（spec §3.2.5）——map 迭代順序不
// 穩定，若不固定順序，同樣的輸入在不同次呼叫會把 WSID 指派給不同 provider。
var providerMigrationOrder = []string{"claude", "codex"}

// Migrate：把 M3a 時代的 per-provider legacy entry 一次性轉成 M3b workspace
// session，寫回 s 並回傳新建的 entries（spec §3.2.5-6）。
//
// 冪等靠 s.Migrated()：已遷移過就直接回 nil, nil，不再走訪 legacy 建立第二
// 枚 WSID——這也是「不存在『registry 已有 entries 卻呼叫 MarkMigrated』路
// 徑」的保證所在：MarkMigrated 只會在本函式唯一一處呼叫點被呼叫到，而該
// 呼叫點前面就是這個 !s.Migrated() early return，所以只要 s 尚未標記
// migrated，s.file.Entries 理論上必然還是「舊格式遷移前」的狀態（Open 剛
// 讀完、或全新檔案）。
//
// 第二層防禦：上一段的「理論上」不是唯一防線
// ——workspace-sessions.json 是使用者可手動編輯的檔案，若有人手動把
// migrated 改回 false（或載入順序被破壞、在 Migrate 之前先呼叫了
// Put／Remove），s.file.Entries 就會在 Migrated()==false 時已經有內容。
// 這種情況下 MarkMigrated 的整批取代語意會把既有 entries 無聲蒸發、且回傳
// nil error，呼叫端會誤判成功。所以在建立任何新 entry 之前，先檢查
// entryCount() 是否非零——刻意用「registry 有任何已持久化的 entry」而非
// 「有 live entry」：只看 Live() 會漏掉「只剩 tombstone、沒有 live
// entry」的情況（例如手動編輯前使用者已經移除過某個 session），此時
// Live() 回空、guard 不會觸發，遷移照跑，tombstone 就被整批取代抹掉——而
// tombstone 存在正是為了讓 replay index 重建時不要把已移除的 session
// 復活（store.go Remove 的說明），丟掉它等於把 §3.6.1 要防的洞打開。
// marker 未設代表這個 registry「應該」是全新的，那它就該是空的（entry
// 數為零，不分 live／tombstone）；任何內容都是異常，一律拒絕。
//
// 只遷移有內容的 entry：ResumeSessionID／TaskID／ViewStartEventID 三者皆空
// 的 provider 不建立、不佔名額。沿用舊 entry 的 ViewStartEventID，只遷 view
// window 之後的歷史，不把整個 provider 的歷史丟進 legacy session。
//
// persist 失敗（MarkMigrated 回錯）一律 fail loud、不標記 migrated，呼叫端
// 應據此不啟動 provider。
func Migrate(s *Store, legacy map[string]LegacyEntry, newWSID func() string) ([]Entry, error) {
	if s.Migrated() {
		return nil, nil
	}
	if n := s.entryCount(); n > 0 {
		return nil, fmt.Errorf("wsregistry: refusing to migrate: registry has %d persisted entries but migration marker is unset (manual edit or load-order bug)", n)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]Entry, 0, len(providerMigrationOrder))
	for _, provider := range providerMigrationOrder {
		le, ok := legacy[provider]
		if !ok {
			continue
		}
		if le.ResumeSessionID == "" && le.TaskID == "" && le.ViewStartEventID == "" {
			continue
		}
		out = append(out, Entry{
			WSID:             newWSID(),
			Provider:         provider,
			ResumeSessionID:  le.ResumeSessionID,
			TaskLabel:        le.TaskID,
			ViewStartEventID: le.ViewStartEventID,
			CreatedAt:        now,
		})
	}

	if err := s.MarkMigrated(out); err != nil {
		return nil, err
	}
	return out, nil
}
