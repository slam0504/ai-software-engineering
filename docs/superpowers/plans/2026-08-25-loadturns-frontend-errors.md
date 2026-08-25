# LoadTurnsBefore 前端錯誤處理 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `pin()`／`loadOlder()` 的載入錯誤在 store 層攔截並顯示（不動 busy）、同一 session 可實際重試（含真實 UI 呼叫端）、`ErrRegistryUncertain` 帶穩定判別片語回前端。

**Tech Stack:** Go（rebuild_orchestrator.go）＋Vue 3／Pinia／TypeScript（session.ts、SessionList.vue、SettingsBar.vue）＋vitest。

**Spec:** `docs/superpowers/specs/2026-08-25-loadturns-frontend-errors-design.md`——最終 snapshot **commit 3245450**（668cf51 rev1→3cd1f26 rev2→b9d5122 rev3→3245450 rev4，owner APPROVED）

## Global Constraints

- §2：清旗標 uncertain 的 cerr wrap 成 `fmt.Errorf("%w（load turns wsid=%s：清 legacy 旗標時發現：%v）", errRegistryUncertain, wsid, cerr)`；非 uncertain 原樣傳播；**不**在 loadTurnsBefore 入口加 `registryUncertain()` 早退（讀路徑照常服務——spec 裁定）。
- §3 proxy 陷阱（gate 實測凍結）：`createdView` 必須是「寫入後從 state 讀回」的 proxy（`if (isNew) this.views[wsid] = newView(); const createdView = this.views[wsid]`）——比對與 `applyToView` 都用它；原始 `newView()` 回傳值比對恆 false（7 條既有測試紅的教訓）。
- §3 呼叫端：SessionList（基準 `persistentPins`）／SettingsBar（基準 `pins`）各自維持；已釘選分支改 `if (!s.views[wsid]) { void s.pin(at, wsid) } else { s.setFocus(at) }`——`idx` 用已釘選格 `at`、重新 pin 不另呼叫 `setFocus`。
- §3：pin 失敗 `pushNotice`（不動 busy、不用 pushError）、pins／persistentPins 不回退、pin 不向外 reject；loadOlder 失敗保留既有內容。
- session.ts:419-423「不可達」註解更新（A→B→A 不需 unpin UI 即可達——§5 裁定）。
- i18n：`store.turnsLoadFailed`／`store.olderTurnsLoadFailed`（zh-TW＋en 同補；keyRefs／locales.parity 測試會守）。
- gofmt／vue-tsc 乾淨；台灣用語書面中文 doc／commit。

---

### Task 1: Backend——清旗標 uncertain 片語 wrap

**Files:**
- Modify: `rebuild_orchestrator.go`（合併分支 `cerr` 非哨兵路徑）
- Test: `app_legacy_transcript_test.go`（stubRegistry 驅動兩條新測試）

- [ ] **Step 1: Write the failing test**（手法比照 app_registry_uncertain_test.go:328 的 stubRegistry 驅動；fixture 三前提：stub entry `LegacyTranscript=true`＋`ViewStartEventID` 非空＋window 空——同 C3 時建立的 legacy_flag_clear 覆蓋列）

```go
// spec §2：清旗標撞 uncertain latch 時，錯誤必須帶前端判別片語（同
// TestErrRegistryUncertainKeepsUIMarker 字面）、保留 cerr 診斷文字、哨兵鏈不斷。
// 注入帶探針文字（owner review P2：裸哨兵抓不到拿掉 %v 的 mutation）。
func TestLoadTurnsBeforeClearUncertainCarriesUIMarker(t *testing.T) {
	// stub fixture 依既有 legacy_flag_clear 覆蓋列手法（app_registry_uncertain_test.go:328-344）
	// mutateErr = fmt.Errorf("%w: dir-sync-probe", wsregistry.ErrRegistryUncertain)
	// _, err := a.LoadTurnsBefore("w1", "", 20)
	// 斷言：err != nil；strings.Contains(err.Error(), "session registry 上一次寫入的結果不確定")；
	// strings.Contains(err.Error(), "dir-sync-probe")；errors.Is(err, wsregistry.ErrRegistryUncertain)
}

// 反向：一般 persist 錯誤不得誤標 latch 片語。
func TestLoadTurnsBeforeClearPlainErrorNoUIMarker(t *testing.T) {
	// mutateErr = errors.New("plain-persist-probe")
	// 斷言：err != nil；含 "plain-persist-probe"；不含前端片語
}
```

（實作時把註解展開為實際 code——stub 驅動細節照既有覆蓋列；既有 chmod 測試 `TestLoadTurnsBeforeClearPersistFailureFailsLoud` 不動。）

- [ ] **Step 2: Run to verify it fails**

Run: `go test . -run 'TestLoadTurnsBeforeClearUncertainCarriesUIMarker|TestLoadTurnsBeforeClearPlainErrorNoUIMarker' -v`
Expected: 正向測試紅（現行回 wsregistry 原始字串、無前端片語）；反向綠

- [ ] **Step 3: Write minimal implementation**

合併分支非哨兵路徑改：

```go
if errors.Is(cerr, wsregistry.ErrRegistryUncertain) {
    cerr = fmt.Errorf("%w（load turns wsid=%s：清 legacy 旗標時發現：%v）", errRegistryUncertain, wsid, cerr)
}
return nil, a.noteRegistryUncertainErr("legacy_flag_clear", wsid, cerr)
```

注意順序：wrap 要在 `noteRegistryUncertainErr` **之前或之後皆可**（helper 只對 `errors.Is` 判定、wrap 後哨兵仍成立——實作時擇一並在 doc 註明；稽核記錄若希望含原始 cerr 則 wrap 放後）。app_registry_uncertain_test.go:328 覆蓋列的「要回同一個錯誤」註解語意順手修。

- [ ] **Step 4: Run to verify it passes**

Run: `go test . -run 'TestLoadTurnsBeforeClear|TestRegistryUncertainAuditCoversStubbableWrites|TestErrRegistryUncertainKeepsUIMarker' -count=1 && go build ./... && go vet ./...`
Expected: 全綠（既有覆蓋列與片語守門不破）

- [ ] **Step 5: Commit**

```bash
git add rebuild_orchestrator.go app_legacy_transcript_test.go app_registry_uncertain_test.go
git commit -m "fix(app): 清旗標 uncertain 錯誤帶前端判別片語——保留 cerr 診斷文字"
```

---

### Task 2: Frontend store——pin／loadOlder 攔截＋重試前置＋i18n

**Files:**
- Modify: `frontend/src/stores/session.ts`（pin isNew 段、loadOlder、:419-423 註解）、`frontend/src/i18n/locales/zh-TW.ts`＋`en.ts`（兩 key）
- Test: `frontend/src/stores/session.loaderrors.test.ts`（新檔；binding stub 慣例照 PaneView.test.ts 的 `s.setBindings`）

**實作（spec §3 凍結，proxy 寫法逐字）：**

```ts
const isNew = !this.views[wsid]
if (isNew) this.views[wsid] = newView()
const createdView = this.views[wsid]   // proxy 讀回——比對與 applyToView 都用它
const m = this.sessions[wsid]
if (m) m.unread = 0
if (!isNew) return
const load = this.bindings?.LoadTurnsBefore
if (!load) return
try {
  const envs = await load(wsid, '', TURN_WINDOW_SIZE)
  if (this.views[wsid] !== createdView) return
  for (const e of envs ?? []) applyToView(createdView, e)
} catch (e) {
  if (this.views[wsid] === createdView) delete this.views[wsid]
  this.pushNotice(t('store.turnsLoadFailed', { wsid, error: String((e as Error)?.message ?? e) }))
}
```

（既有 `const v = this.views[wsid]` 重取段整段汰換——身分比對涵蓋存在性檢查；:419-423 註解改寫為「A→B→A 時序不需 unpin UI 即可達，已以 createdView 身分比對防護」。）loadOlder：`await load(...)` 段包 try/catch，catch 只 `pushNotice(t('store.olderTurnsLoadFailed', ...))`，不動 view。

- [ ] **Step 1: Write the failing tests**（spec §4 前端清單逐條；deferred promise 手法照 PaneView.test.ts:146-164）

1. pin reject → `views[wsid]` 不存在、notice lane 有錯誤、`busy`（**前置 true**）仍 true、`errorSeq` 增；再 `pin` 同 wsid → binding 第二次呼叫（重試 mutation 守門）。
2. pin reject 且錯誤含 `REGISTRY_UNCERTAIN_MARK` → `latchSeq` 增。
3. A→B→A（deferred）：(a) 第一個 load reject → `views['A']` 仍存在且為第二實例；(b) resolve → 舊 envelope 不套到新實例（第二實例 timeline 空）。
4. pin 不向外 reject：pane 0 load reject、pane 1 正常 → `restoreLayout` 後 pane 1 的 view 存在。
5. `persistentPins` 不回退：pin 失敗後 `persistentPins[idx]` 仍為該 wsid。
6. loadOlder reject（wsid 釘進 **focused** pane、busy 前置 true）→ timeline 內容不變、busy 仍 true、notice 有錯誤；再呼叫 → binding 再被呼叫。
7. loadOlder reject 且含 MARK → `latchSeq` 增。
8. 反向：成功路徑無 notice、既有行為不變（既有 pin/loadOlder 測試不破）。

- [ ] **Step 2: Run to verify it fails**

Run: `npm --prefix frontend run test -- session.loaderrors`
Expected: 1-7 紅（現行 unhandled rejection／永久早退）；8 綠

- [ ] **Step 3: Implement**（上方凍結 code＋i18n 兩 key＋註解更新）

- [ ] **Step 4: Run to verify it passes**

Run: `npm --prefix frontend run test && npm --prefix frontend run build`
Expected: 全套 vitest 綠（含 keyRefs／locales.parity）、vue-tsc 乾淨

- [ ] **Step 5: Commit**

```bash
git add frontend/src/stores/session.ts frontend/src/stores/session.loaderrors.test.ts frontend/src/i18n/locales/zh-TW.ts frontend/src/i18n/locales/en.ts
git commit -m "fix(frontend): pin/loadOlder 載入錯誤 store 層攔截——proxy 身分比對、重試前置、notice 不動 busy"
```

---

### Task 3: Component——SessionList／SettingsBar 已釘選重試分支

**Files:**
- Modify: `frontend/src/components/SessionList.vue`（:39-43）、`frontend/src/components/SettingsBar.vue`（:20-24）
- Test: `frontend/src/components/SessionList.test.ts`＋`SettingsBar.test.ts`（各補一組；mount 慣例照 SessionList.test.ts 既有手法）

- [ ] **Step 1: Write the failing tests**

各檔兩條：
1. 首載失敗（binding reject 一次）→ view 已清 → 再點同一入口 → binding 被**第二次**呼叫（真實 UI 重試）。
2. **反向**：view 存在時再點 → 只 setFocus、binding **不得**再被呼叫（gate 實測：無條件 pin 的 mutation 下現行 49 條全綠——此反向是唯一守門）。

- [ ] **Step 2: Run to verify it fails**

Run: `npm --prefix frontend run test -- SessionList SettingsBar`
Expected: 各檔第 1 條紅（現行已釘選只 setFocus）；第 2 條綠

- [ ] **Step 3: Implement**（兩檔已釘選分支：`if (!s.views[wsid]) { void s.pin(at, wsid) } else { s.setFocus(at) }`——`at`＝該檔各自基準查得的已釘選格；不另呼叫 setFocus）

- [ ] **Step 4: Run to verify it passes**

Run: `npm --prefix frontend run test && npm --prefix frontend run build`
Expected: 全綠

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/SessionList.vue frontend/src/components/SettingsBar.vue frontend/src/components/SessionList.test.ts frontend/src/components/SettingsBar.test.ts
git commit -m "fix(frontend): 已釘選但無 view 時重新 pin——真實 UI 重試路徑（含反向守門）"
```

---

### Task 4: 迴歸

- [ ] Step 1: `npm --prefix frontend run test && npm --prefix frontend run build`
- [ ] Step 2: `go test . -run 'TestLoadTurnsBefore|TestRegistryUncertain|TestErrRegistryUncertainKeepsUIMarker|TestLegacy' -count=1 && go build ./... && go vet ./...`
- [ ] Step 3: `go test -race ./internal/wsregistry/ ./internal/replayindex/ -count=1 && go test . -count=1 -timeout 900s`（牆鐘名單規則照舊）
- [ ] Step 4: 無修正則無 commit。

---

## Self-Review

- 四要件對映：攔截（T2 store try/catch＋T1 backend 片語）✓；顯示不動 busy（T2 測試 1/6 busy 前置 true）✓；判別片語（T1 兩條＋T2 測試 2/7 latchSeq）✓；重試（T2 測試 1 store 層＋T3 兩個 component 各含正反向）✓。
- proxy 凍結寫法：T2 實作段逐字（gate 376 綠版本）。
- 已知限制：SettingsBar.test.ts 若不存在則新建（動手前確認——SessionList.test.ts 存在已核；SettingsBar 有無既有測試檔待實作時確認，無則沿用同 mount 慣例建檔）。
- 「不在讀路徑加 registryUncertain() 早退」為 spec 裁定，實作不得自行加（review checklist 項）。
