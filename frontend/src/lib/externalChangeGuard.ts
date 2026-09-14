// A1b-1：外部檔案變更檢查的所有權與回應失效判定。
//
// 為什麼要有這個模組——三個反例（設計稿 rev5 §2.4／§2.5）都不是「回應時看一下
// busy」能擋的：
//
//   C1  寫入已送出 → focus 觸發新讀取 → 若用單一世代淘汰，寫入成功回應會被丟棄，
//       磁碟已更新但 savedContent／寫入基準沒更新（使用者內容其實已落盤，UI 卻
//       顯示未儲存，後續操作以錯誤基準進行）。
//   C2  新讀取已確認同步 → 舊失敗回應後到 → 若把它導向「未知」就會把新的有效
//       結果清掉。
//   C3  背景讀取 A 尚未返回 → 使用者儲存、預檢與寫入都成功、基準已更新 → A 才
//       返回舊內容。此時 busy 已清除，只看 busy 判不出 A 已過期；若採用 A，當下
//       clean 會觸發自動重載舊內容，把剛儲存的內容退回舊版。
//
// 因此拆成兩個互不取代的機制：
//
//   前景操作所有權  saveFile／acceptDraft 按下時取得，涵蓋「預檢 → 寫入 → 結果
//                   套用」整段。前景回應的有效性**只**由所有權決定，不被任何背景
//                   讀取淘汰（擋 C1）。取得所有權當下即遞增背景世代，使此前在途
//                   的背景讀取立即失效——失效在取得當下定案，不等回應（擋 C3）。
//   背景四項核對    B1 世代／B2 目前檔案／B3 元件生命週期／B4 基準版本。B4 與 B1
//                   並列為必要條件，不是二擇一：B1 靠世代、B4 靠基準值，後者與世
//                   代機制無關，將來若新增寫入路徑忘了取得所有權，B4 仍能擋下
//                   「發出時基準 X、返回時已是 Y」的過期回應。
//
// 過期回應一律完全 no-op：不改同步狀態、錯誤、標記、busy，也不改 buffer／
// savedContent／寫入基準（擋 C2）。舊成功與舊失敗一視同仁。

/** 同步三值——**僅內部判定**，不外顯為常駐 UI 狀態或 badge（設計稿條款 14）。 */
export type SyncState = 'insync' | 'unknown' | 'diverged'

/**
 * 背景讀取的取樣戳記；B1–B4 四項核對就是拿發出當時的戳記與回應到達當時的戳記
 * 逐欄比對。四欄都是純值，所以判定可以是純函式（見 shouldApplyBackground）。
 */
export interface GuardSnapshot {
  /** B1：發出當時的背景世代。 */
  gen: number
  /** B2：發出當時的目前檔案（workspace 相對路徑）。 */
  path: string
  /** B3：發出當時的元件掛載實例。 */
  instanceId: number
  /** B4：發出當時的寫入基準 digest（`sha256:…`）。 */
  baselineDigest: string
}

/**
 * B1–B4 四項核對。純函式：呼叫端把「發出當時」與「到達當時」兩份戳記交進來，
 * 四欄全等才可套用，任一不符即完全 no-op。
 *
 * 這是 B1–B4 的**唯一實作**——元件只透過它判定，測試也直接對它下測，
 * production 不因此多出任何 seam、參數或旗標（owner 要求：不得為驗證 B4 新增
 * production 注入介面）。因為四欄獨立比對，要證明「B4 在其他三項有效、唯獨基準
 * 改變時仍拒絕」只需給一組 gen／path／instanceId 相同而 baselineDigest 不同的
 * 輸入，不必動用元件或偽造時序。
 */
export function shouldApplyBackground(stamp: GuardSnapshot, current: GuardSnapshot): boolean {
  return stamp.gen === current.gen
    && stamp.path === current.path
    && stamp.instanceId === current.instanceId
    && stamp.baselineDigest === current.baselineDigest
}

export interface ExternalChangeGuard {
  /** B3 用的掛載實例識別；每個 guard 實例一個，卸載後不再變。 */
  readonly instanceId: number
  /**
   * 取得前景操作所有權。回傳 token 供後續 isOwner／release 使用。
   * **副作用（刻意）**：遞增背景世代，使此前所有在途背景讀取立即失效（C3）。
   */
  acquire(): number
  /** 釋放所有權；非目前擁有者呼叫時忽略（避免遲到的 finally 誤放新操作的所有權）。 */
  release(token: number): void
  /**
   * 使所有在途的背景讀取立即失效，但**不**取得所有權。
   *
   * 給「不走 guard 所有權、卻會改動 buffer／寫入基準／目前檔案」的既有操作用——
   * 目前是 loadFile（busy='load'，兩側）與 confirmBump（busy='bump'，Plan）。
   * 這些操作沒有 acquire()，光靠 B4 擋不住：`confirmBump` 只改 buffer 不改基準，
   * `loadFile` 換到內容相同的檔時基準也可能不變。失效必須在**操作開始當下**定案，
   * 不能等回應到達時看 busy（操作可能已結束、busy 已清）。
   */
  invalidateBackground(): void
  /** 前景回應的唯一有效性判準——不看 busy、不看世代。 */
  isOwner(token: number): boolean
  /** 是否有前景操作持有所有權（背景檢查據此延後或略過）。 */
  hasOwner(): boolean
  /**
   * 背景檢查是否可以發出。所有權存在期間一律不發，也**不遞增世代**——
   * 遞增會把進行中的寫入判成過期（C1）。
   */
  canStartBackgroundRead(): boolean
  /**
   * 取得目前戳記。發出讀取前呼叫一次留存，回應到達後再呼叫一次比對。
   * 背景讀取發出時（forBackgroundRead=true）才遞增世代，讓較新的背景讀取淘汰較舊的。
   */
  snapshot(path: string, baselineDigest: string, forBackgroundRead?: boolean): GuardSnapshot
  /** 元件卸載：之後任何回應都不得套用（B3）。 */
  dispose(): void
  /** 是否仍為有效實例（未卸載）。 */
  isActive(): boolean
}

let nextInstanceId = 1

export function createExternalChangeGuard(): ExternalChangeGuard {
  const instanceId = nextInstanceId++
  let gen = 0
  let ownerToken: number | null = null
  let nextToken = 1
  let disposed = false

  return {
    instanceId,

    acquire() {
      // 先遞增世代再交出 token：此後任何「發出時世代較舊」的背景回應都會在 B1
      // 落敗，且這個判定在此刻就定案，不依賴回應到達時的 busy 狀態（C3）。
      gen += 1
      ownerToken = nextToken++
      return ownerToken
    },

    release(token) {
      if (ownerToken === token) ownerToken = null
    },

    invalidateBackground() {
      gen += 1
    },

    isOwner(token) {
      return !disposed && ownerToken === token
    },

    hasOwner() {
      return ownerToken !== null
    },

    canStartBackgroundRead() {
      // 只知道 guard 自己的前景所有權。load／bump 這類既有 busy 不經過 guard，
      // 呼叫端必須另外排除（見兩個 workspace 的 busyReason 判斷）——本方法不
      // 假裝知道元件的 busy 模型。
      return !disposed && ownerToken === null
    },

    snapshot(path, baselineDigest, forBackgroundRead = false) {
      // 只有「發出背景讀取」才遞增；比對用的取樣不得遞增，否則自己會淘汰自己。
      if (forBackgroundRead) gen += 1
      return { gen, path, instanceId, baselineDigest }
    },

    dispose() {
      disposed = true
      ownerToken = null
    },

    isActive() {
      return !disposed
    },
  }
}
