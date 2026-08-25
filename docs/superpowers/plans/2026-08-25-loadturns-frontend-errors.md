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

合併分支非哨兵路徑改為下方**唯一版本**（owner review rev2 P1：原本並列的
wrap-before snippet 已刪除——兩套互斥實作並存會讓執行者採到相反順序）。

**wrap 順序凍結（plan gate 實測校正）**：**wrap 放在 `noteRegistryUncertainErr` 之後**——即稽核收到的是 registry 原始錯誤（含原始 errno），使用者可見訊息才加 app 片語，對齊既有慣例（app.go:7167-7176 的 tombstone_persist 同形）。（gate 實測：wrap-before 會讓 audit error 欄膨脹且多一份 app 片語——「wrap 放後稽核才含原始 cerr」的舊理由方向錯誤，wrap-before 其實是 superset，真正差異是稽核要不要多片語。）實作形：

```go
uerr := a.noteRegistryUncertainErr("legacy_flag_clear", wsid, cerr)
if errors.Is(uerr, wsregistry.ErrRegistryUncertain) {
    uerr = fmt.Errorf("%w（load turns wsid=%s：清 legacy 旗標時發現：%v）", errRegistryUncertain, wsid, uerr)
}
return nil, uerr
```

app_registry_uncertain_test.go:328 覆蓋列的「要回同一個錯誤」註解語意順手修。已知登記（gate P2，不改）：wrap 後使用者可見字串會含 wsregistry 那句兩次（errRegistryUncertain 的 `%w`＋`%v`），spec §2 已凍結格式。

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
9. **i18n placeholder 守門**（gate P2：locales.parity 只比 key 路徑不查 placeholder；owner rev2 P2：**zh-TW 與 en 各驗一次**，只測現行 locale 會放過另一語系漏 `{wsid}`／`{error}`）：兩個 key × 兩語系，`t(...)` 產出同時含 `w1` 與 `x`。

Binding stub 慣例統一採 `registryUncertain.test.ts:43` 的 `LoadTurnsBefore: vi.fn()` 形（spec 與 plan 原引兩處不同前例，擇此定案）；deferred promise 手法照 PaneView.test.ts:146-164。

- [ ] **Step 2: Run to verify it fails**

Run: `npm --prefix frontend run test -- session.loaderrors`
Expected: **1–4、6–7、9 紅；5、8 綠**（owner rev2 P2 校正：第 5 條 persistentPins 不回退是現況即成立的反向、實作前就綠；第 9 條 i18n key 尚不存在、實作前紅）

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
2. **反向（owner review rev2 P1 定案——action spy）**：fixture 建立完成後對
   store action 加 spy（`const pinSpy = vi.spyOn(s, 'pin'); const focusSpy = vi.spyOn(s, 'setFocus')`）
   → view 存在時再點同一入口 → 斷言 **`setFocus(at)` 被呼叫且 `pin` 未被呼叫**。
   兩個 component caller 都覆蓋。這直接區辨 `setFocus(at)` 與誤用 `pin(at, wsid)`
   ——行為面快照（pins／focused／view 實例）在無 transient 的 fixture 下兩條
   路徑結果相同、無法區辨（gate P1 的行為面版本不足，owner 裁定改 spy；「守衛
   目的就是保留 view 已存在時的既有呼叫語意」，不得登記為已知缺口）。
   （scratch 實測定案，owner 指定的補驗：兩檔 spy 版反向在現行 code 綠（26/26
   含既有 24）、把已釘選分支 mutate 成 `void s.pin(at, wsid)` 後兩檔各自紅在
   `focusSpy` 斷言（呼叫次數 0、pinSpy 實收 `[at, wsid]`）——失敗精準對應誤用
   pin，非無關紅；`vi.spyOn(s, 'pin'/'setFocus')` 在 mount 後、點擊前設下即可
   攔截，不需 `$onAction` 或重新 mount。）

- [ ] **Step 2: Run to verify it fails**

Run: `npm --prefix frontend run test -- SessionList SettingsBar`
Expected: 各檔第 1 條紅（現行已釘選只 setFocus）；第 2 條綠

- [ ] **Step 3: Implement**——兩檔已釘選分支，**snippet 必須嵌在既有 `if (at === 0 || at === 1)` 內**（gate P2：此處才有 `0 | 1` type narrowing，外提會過不了 vue-tsc）：

```ts
if (at === 0 || at === 1) {
  if (!s.views[wsid]) void s.pin(at, wsid)
  else s.setFocus(at)
} else {
  s.pin(s.focused, wsid)   // 未釘選分支不變
}
```

`at`＝該檔各自基準查得的已釘選格（SessionList：persistentPins；SettingsBar：pins）；重新 pin 分支不另呼叫 setFocus。

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
- proxy 凍結寫法：T2 實作段逐字（gate scratch 實測版本：374 條既有全綠＋9 條 scratch 綠、build 乾淨）。
- SettingsBar.test.ts **已存在**（controller 核實）——Task 3 為 modify 非新建，沿用其 `vi.hoisted`＋`mountWithI18n`＋`two()` helper 慣例。
- **前端 baseline 已知間歇紅（gate 實測，兩條、皆非本票範圍）**：`PlanWorkspace.test.ts` 的 PlanAssist loading 案例與 `SpecWorkspace.test.ts` 的 spec-assist 檔案切換案例——三輪 clean-tree 有一輪紅。Task 2-4 的「Expected: 全綠」允差規則：紅落在此兩條時單獨重跑該檔判定（重跑綠＝不算回歸、report 記錄）；其他紅一律如實回報。此兩條為前端首度登記的間歇名單（Go 側 5 條牆鐘名單之外），關票時提醒 owner 是否入 memory。
- 「不在讀路徑加 registryUncertain() 早退」為 spec 裁定，實作不得自行加（review checklist 項）。
