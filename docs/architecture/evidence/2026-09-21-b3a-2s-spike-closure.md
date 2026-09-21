# B3a-2s 結案紀錄

> 日期：2026-09-21（台灣時間）。以下時刻一律為原始 UTC 值。
> 結論：**spike 技術結論通過；browser 整合未驗收。**（codex-reviewer #198 裁定）
> 本文件只是**結案紀錄**，不是 browser E2E 驗收，也不是 B3a-2b 施工授權；
> 授權範圍見「6. 授權範圍與下一步」。

## 1. 受測對象與來源

| 項目 | 值 |
|---|---|
| Worktree pin | `db1e8b0a40aee7bc0766ac6c22658b684860d373`（detached，`/tmp/b3a2s-wt`；與本次結案文件分支基準的 `origin/main` 同一 commit） |
| 證據根目錄 | `/tmp/b3a2s-20260921T022524Z/`（rev1 原始 spike＋`rev2/` 補件＋`rev3/` 封存 addendum，三者皆保留、不互相覆寫） |
| manifest 版本 | rev1：`manifest/MANIFEST.txt`（生成於 2026-09-21T02:35:29Z）／rev2：`rev2/manifest/MANIFEST-rev2.txt`（生成於 2026-09-21T02:59:58Z）／rev3：`rev3/README.md`＋`rev3/SHA256SUMS`（本次結案文件撰寫前完成，見第 5 節） |
| reviewer 裁定 | #198（本次結案授權，「spike 技術結論通過，可進入結案文件與最小實作設計」）；#190（rev1 原 binary 雜湊與實跑核對，見第 5 節限制） |

本票僅涵蓋 fake／replay provider 對 session recovery 與 approval 流程的**技術可行性評估**，
不涉及真實 provider、Wails、browser 的任何實跑。

## 2. 四個 probe（現用來源與雜湊）

| Probe | 測試名稱 | 檔案（worktree 相對） | sha256（現用版本） |
|---|---|---|---|
| 1 | `TestProbeRealSubprocessApprovalAllow`／`TestProbeRealSubprocessBrokerDownFailsClosed` | `internal/approval/zz_b3a2s_probe_subprocess_test.go` | `bfd8b8e9ce8b5de2526d91c07a4f3de2078ae6820d045cd46e71c1e4f81605c9`（rev2 R4 修法版；原 rev1 版本 `0094677...` 已非現用，見第 5 節） |
| 2 | Codex `EnsureThread` resume-id 檢查（負向） | `internal/codex/zz_b3a2s_probe_resume_mismatch_test.go` | `ad558628ea61f3e9adcb5a0de0304384a0f43c9f9c8bf17ad360d57ecf119010` |
| 3 | Claude resume-refused 閘（負向） | `zz_b3a2s_probe_resume_refused_test.go` | `320ec55f69e0eca55c46f7c3b92d3ec064852b29e128b7fce27bad67337df73f` |
| 4 | `TestProbeApprovalResponseRoundTrip`／`TestProbeApprovalResponseRoundTripDeny`（正向＋負控制） | `zz_b3a2s_probe_approval_response_test.go` | `294c0872c06027a1227b18f91fa3e0462c9808b89b263be017a8e3c34cfe9fe8` |

四個 probe 均為 package main／package-local 的臨時檔，**未修改任何既有 production 或
harness 檔案**；reviewer #198 獨立重跑，六個測試（Probe 1 兩個＋Probe 4 兩個＋Probe 2／3
各一個）全 PASS、無 SKIP。

## 3. 兩種 provider 的證據層次

技術結論範圍嚴格依 reviewer #198 定調：只接受下列兩項**已實測**事實，**不**由此推論兩個
provider 契約對稱或不對稱，**不**定性為 production defect，維持**待判契約**。

### 3.1 Codex

- **已實測**：`internal/codex/turns.go:60-89`（`EnsureThread`）送出 `thread/resume` 後，
  **不比對**回應 `thread.id` 與請求的 `resume` 參數是否一致——Codex 回應 ID 不同仍被
  `EnsureThread` 採納（Probe 2）。
- **Approval 往返證據**：**分段覆蓋**，但 response 段**現在有實際 wire 證據**——
  - request 發起段：既有測試（`app_test.go:363-430`
    `TestCodexFirstTurnCompletedBeforeResponse`）驗證 `item/commandExecution/requestApproval`
    server→client 的真實 wire 送出。
  - response 段：本次新增 Probe 4（`capturingCodexWire`／`newCapturingCodexConn`，平行於
    既有 `fakeCodexWire`／`newFakeCodexConn` 但不修改它），實測 App 寫回 wire 的 response
    frame 確實帶對 `id`／`decision`、無 error，並以「錯 id」「錯 decision」兩個負控制證實
    `verifyApprovalResponse` 正確拒絕——這是本次補上的、此前完全空白的一段。
  - **仍不成立的宣稱**：不得把「request 段既有測試綠燈」＋「response 段 Probe 4 綠燈」
    相加宣稱為「完整驗證整條往返」；两段之間（codex app-server 真正何時決定要求核可）
    是 provider 內部行為，本次刻意排除、未測。

### 3.2 Claude

- **已實測**：`app.go:7372-7383`（送出前，對 App 自己的 `internal/claude/registry.go`
  做本地一致性檢查）在 Claude 送出前會**拒絕**接回錯的 WSID（Probe 3，正確性閘存在且
  本次新驗證了真實子行程邊界）。
- **未實測、僅為讀碼結果**：`app.go:7499-7510`（`Bind(info.SessionID, ...)`，init 事件
  處理層）**未顯式比對** claude CLI 回報的 `info.SessionID` 與原本請求的 resume 參數，
  無條件接受——這是**讀碼**得出的結論（含 `internal/claude/registry.go:57-59` `Bind()`
  的文件註解佐證），**下游行為未經任何測試實際跑過**，不同於 3.1 節 Codex 那一層有
  Probe 2 直接執行驗證。
- **Approval 往返證據**：**分段覆蓋**——MCP handshake、broker↔App↔UI 各自有雙向協定
  fake 級別的既有測試，Probe 1（R4 修法版）補強了「真實 OS 子行程邊界」這一段；但
  claude CLI 是否真的照 `--mcp-config` 內容 spawn 宣告的子行程，中間銜接處**沒有任何
  一段測試涵蓋**，不得把三段綠燈相加宣稱等於完整鏈路。

### 3.3 兩者共通、不得跨層混淆的邊界

Probe 2（Codex）與 Probe 3（Claude）驗證的是**不同層**：Probe 3 是「送出前」對 App 自己
registry 的一致性檢查（Claude 有、且本次證實會攔下錯接），Probe 2 是「送出後」對 provider
回應 id 的比對（Codex 沒有）。「送出後比對回應 id」這一層，3.1／3.2 已分別確認 **Codex 與
Claude 都沒有**（Claude 的對應層是 3.2 節「未實測、僅為讀碼結果」那一條）——這是兩個
provider **共有**的待判契約，不是單一 provider 的不對稱，也不得由此定性為 production
defect。是否該補、補在哪一層，超出本次授權，留給 owner／reviewer 另行決定。

browser E2E 整合（fakeCli.ts 擴充、MCP config 讀取／spawn、真實 codex app-server 行為）
**全部未驗收**——本票結論僅止於 App／adapter 層與已補強的 wire 層證據。

## 4. rev1 原 binary 遭覆寫的限制

rev1 spike 的 probe binary（`probes/bin/workbench`）原始 build 的 sha256 為
`396b43b74b1cd583e1c5fbaad11f6e277803a1e026452e9a6e17c6666d025026`（記錄於
`manifest/MANIFEST.txt`）。rev2 session 在同一路徑重新 `go build`，**覆寫**了這個檔案；
本次（rev3）以 `shasum -a 256` 直接核對，確認 `probes/bin/workbench` 與
`rev2/probes/bin/workbench` 目前**都**是 rev2 重建版本（sha256
`e11dc88682357ea8ee931531522b548f7a84f97ca2bd7e65e7db3065fe9a4fb5`），**rev1 原始位元組
已不可再由該路徑取得**。

reviewer 在 #190 曾核對過 rev1 原 binary 的雜湊並實際執行過它——這是既有 review 流程的
既定紀錄；本 session 在 repo 內（worktree 與 main checkout）搜尋未找到本地留存的
「#190」文字紀錄，因此這一點**如實標記為轉述、非本 session 獨立重新驗證**，與本節前段
「rev1 位元組已不可取得」這件事本身（由本 session 直接雜湊比對確認）分開陳述，不混為一談。

**不追加任何建置鑑識**：rev2 manifest 原文曾將「binary 位元組不同」歸因於「Go build
metadata／不可重現」，**該因果宣稱本次（rev3）已撤回**——它是未經查證的猜測，rev2
session 並未實際比對 build metadata 或用 `-trimpath` 等手法排除其他差異來源。撤回後
唯一站得住的事實是：目前的 binary 雜湊與 rev1 記錄值不同，**成因未知、未查證**。詳見
`rev3/README.md`。

## 5. rev3 封存 addendum

- 位置：`/tmp/b3a2s-20260921T022524Z/rev3/`（additive，不覆寫 rev1／rev2）。
- 內容：`README.md`（第 1.1／1.2 節對應上方第 4 節兩項變更的完整敘述、舊 manifest 的
  路徑基準表）、`sources/`（四個現用 probe 來源＋`frontend/dist/index.html` placeholder
  的複製）、`bin/workbench`（rev2 重建 binary 的複製，非重建、非新建置）、
  `logs/rev3-session.log`（本次 session 時間軸）、`SHA256SUMS`（純雜湊清單）。
- 驗證：`cd /tmp/b3a2s-20260921T022524Z/rev3 && shasum -a 256 -c SHA256SUMS` 全數
  `OK`（本次結案文件撰寫前實際執行過，見交付回報的證據區塊）。

## 6. 授權範圍與下一步

- **通過**：B3a-2s spike 的技術探索結論（見第 3 節範圍界定，reviewer #198 裁定）。
- **未通過／未涵蓋**：browser E2E 驗收、B3a-2b 全部施工授權、B3a-2a、正式 CI、A5。
- 工時：原 **0.45 pt（4–5 hr timebox，中位 4.5 hr）只留為估算**，不代表最終實績；
  已知 active 時間（未核實為完整覆蓋，僅為量測到的片段）：
  - rev1（原始 spike）：`02:26:11Z–02:39:xx Z`，約 13 分鐘（見
    `/tmp/b3a2s-20260921T022524Z/timeline.log`）。
  - rev2（R1–R4 補件）：`02:46:00Z–02:59:58Z`（manifest 生成時點），約 14 分鐘（見
    `rev2/manifest/MANIFEST-rev2.txt` 開頭時間注記）；manifest 生成後是否還有額外
    active 時間**未逐秒核實**，不併入上述數字。
  - rev3＋本結案文件＋設計草稿＋估點重整（本次 session）：見交付回報的時間戳章節
    （`rev3/logs/rev3-session.log` 與最終回報）。
  - 三段相加**不構成**一個新的「總實際工時」宣稱——各段量測範圍與精確度不同，本文件
    不做加總換算成 pt。
- B3a-2b 最小施工設計草稿：**留在 repo 外**（依任務指示，設計階段不落地 repo 檔案），
  路徑與重點見交付回報。
- 估點重整：見交付回報「估點表與算術驗算過程」，本階段**不核定**任何新的 B3a-2b 總量
  pt 數字（依 reviewer 指示，見設計草稿與估點段）。
