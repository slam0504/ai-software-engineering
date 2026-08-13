// planDag.ts（Task 14，spec §6）：plan YAML → mermaid flowchart 的純函式 projection。
//
// parsePlanDoc 是**輕量、schema-bound 的行掃描 parser**，不是通用 YAML parser——
// 只認得本專案 plan 文件（internal/plan/types.go）目前寫出的固定縮排形狀：
//   tasks:
//     - id: T1
//       title: ...
//       depends_on: [] 或 depends_on:\n    - T2
//       minimum_risk_tier: medium
//       planner_risk_tier: medium
// 也就是 2-space 縮排的 task 列表項、4-space 縮排的欄位（對齊 "- " 後的
// 第一個字元）。depends_on 的 block list 支援兩種縮排（go-yaml v3 兩種寫法都
// 合法、後端實際會寫出）：項目比 key 多縮 2 space（"      - T2"），或項目與
// key 同縮排（"    - T2"，go-yaml v3 預設 marshal 風格）。不支援其他縮排
// 深度、多行 scalar（| / >）、或欄位順序以外的 YAML 特性；解析失敗（缺
// plan_id、缺 tasks、task 缺 id、或 depends_on 後面接了看似 list 項目但縮排
// 既非上述兩種的可疑行——寧可 fail-null 也不要靜默吞成空陣列產出自信的錯圖）
// 一律回傳 null，呼叫端（DagPane）決定如何呈現錯誤，不在這裡拋例外或猜測。

export interface PlanTask {
  id: string
  title: string
  dependsOn: string[]
  minimumRiskTier: string
  plannerRiskTier: string
}

export interface PlanDoc {
  planId: string
  tasks: PlanTask[]
}

// stripScalar：去掉行內 YAML scalar 常見的前後空白與一層包住的引號——足夠應付
// 本專案 planner 產出的欄位值，不處理跳脫序列（本 schema 不需要）。
function stripScalar(raw: string): string {
  const v = raw.trim()
  if (v.length >= 2) {
    const first = v[0]
    const last = v[v.length - 1]
    if ((first === '"' && last === '"') || (first === "'" && last === "'")) return v.slice(1, -1)
  }
  return v
}

// parseInlineList："[a, b, c]" 或 "[]" → string[]；非中括號形式回傳 null，
// 讓呼叫端改走 block list 掃描。
function parseInlineList(raw: string): string[] | null {
  const v = raw.trim()
  if (!v.startsWith('[') || !v.endsWith(']')) return null
  const inner = v.slice(1, -1).trim()
  if (!inner) return []
  return inner.split(',').map(s => stripScalar(s)).filter(Boolean)
}

export function parsePlanDoc(yamlText: string): PlanDoc | null {
  try {
    const lines = yamlText.split(/\r\n|\n/)

    let planId = ''
    for (const line of lines) {
      const m = /^plan_id:\s*(.*)$/.exec(line)
      if (m) { planId = stripScalar(m[1]); break }
    }
    if (!planId) return null

    const tasksIdx = lines.findIndex(l => /^tasks:\s*$/.test(l))
    if (tasksIdx === -1) return null

    const tasks: PlanTask[] = []
    let current: PlanTask | null = null
    // pendingDepsBlock：目前 task 的 depends_on 是 block list 形式（值留白），
    // 等後續 dash 行才收集依賴——見下方迴圈。malformed：曾出現「看起來像 depends_on
    // 的 list 項目，但縮排既非同縮排也非多縮 2 space」的可疑行——與其把它當成
    // 別的欄位跳過（等於把 depends_on 靜默解析成空陣列），整份文件直接判定解析
    // 失敗（fail-null），不要產出自信但錯誤的圖。
    let pendingDepsBlock = false
    let malformed = false

    function flush() {
      if (current) tasks.push(current)
      current = null
      pendingDepsBlock = false
    }

    for (let i = tasksIdx + 1; i < lines.length; i++) {
      const line = lines[i]
      if (line.trim() === '') continue

      const taskStart = /^ {2}- id:\s*(.*)$/.exec(line)
      if (taskStart) {
        flush()
        current = { id: stripScalar(taskStart[1]), title: '', dependsOn: [], minimumRiskTier: '', plannerRiskTier: '' }
        continue
      }

      // 頂層新 key（非 tasks 區塊內容、非 depends_on block 項目）＝tasks 區段結束。
      if (!/^\s/.test(line)) break

      if (!current) continue

      if (pendingDepsBlock) {
        const dep = /^( +)- ?(.*)$/.exec(line)
        if (dep) {
          // 同縮排（4-space，go-yaml v3 預設 marshal 風格）或多縮 2 space（6-space，
          // 人工編排常見寫法）都算合法 depends_on 項目；其他縮排視為 malformed。
          if (dep[1].length === 4 || dep[1].length === 6) { current.dependsOn.push(stripScalar(dep[2])); continue }
          malformed = true
          continue
        }
        pendingDepsBlock = false // 非 dash 行 → depends_on block 正常結束，往下當一般欄位處理
      }

      const field = /^ {4}(title|minimum_risk_tier|planner_risk_tier|depends_on):\s*(.*)$/.exec(line)
      if (!field) continue
      const [, key, rawVal] = field
      if (key === 'title') current.title = stripScalar(rawVal)
      else if (key === 'minimum_risk_tier') current.minimumRiskTier = stripScalar(rawVal)
      else if (key === 'planner_risk_tier') current.plannerRiskTier = stripScalar(rawVal)
      else if (key === 'depends_on') {
        const inline = parseInlineList(rawVal)
        if (inline !== null) current.dependsOn = inline
        else pendingDepsBlock = true // "depends_on:"（值留白）→ 下面幾行是 block list
      }
    }
    flush()

    if (malformed) return null
    if (tasks.length === 0) return null
    if (tasks.some(t => !t.id)) return null

    return { planId, tasks }
  } catch {
    return null
  }
}

// sanitizeNodeId：mermaid 節點 id 只允許 [A-Za-z0-9_-]，其餘字元換成 "_"。
// 匯出供 DagPane 在渲染後的 SVG 節點 id（flowchart-<nodeId>-<idx>，mermaid 慣例）
// 換回原始 task id 時複用同一套規則，避免兩處各自維護一份正規化邏輯。
// 注意：這一步不保證唯一（見下方 buildNodeIdMap）——後端對 task id 只驗非空＋
// 唯一，沒有字元限制，"a b" 與 "a_b" 這種不同 id sanitize 後會撞在一起。
export function sanitizeNodeId(id: string): string {
  return id.replace(/[^A-Za-z0-9_-]/g, '_')
}

// buildNodeIdMap：task.id → 保證唯一的 mermaid 節點 id。sanitizeNodeId 本身
// 不是單射（不同原始 id 可能 sanitize 成同一個字串），若直接拿它當節點 id，
// 碰撞的兩個 task 會被 mermaid 疊成同一個節點（邊接錯、DagPane 點選對回錯的
// taskId）。這裡照 doc.tasks 順序遇碰撞就加遞增後綴（_2, _3, ...）分開，
// planToMermaid 與 DagPane 的點選反查都呼叫這個函式，兩邊永遠算出同一份
// id 對應，不會各自維護、走鐘。
export function buildNodeIdMap(doc: PlanDoc): Map<string, string> {
  const used = new Set<string>()
  const nodeIds = new Map<string, string>()
  for (const task of doc.tasks) {
    const base = sanitizeNodeId(task.id)
    let candidate = base
    let suffix = 2
    while (used.has(candidate)) {
      candidate = `${base}_${suffix}`
      suffix++
    }
    used.add(candidate)
    nodeIds.set(task.id, candidate)
  }
  return nodeIds
}

// escapeLabel：mermaid 字串節點內 `"` 需寫成 #quot; 才不會提早結束標籤字串、
// 破壞整張圖；`[`/`]`/反引號同樣會被 mermaid 解析成語法字元，一併跳脫。
function escapeLabel(s: string): string {
  return s
    .replace(/"/g, '#quot;')
    .replace(/\[/g, '#91;')
    .replace(/\]/g, '#93;')
    .replace(/`/g, '#96;')
}

// planToMermaid：`flowchart TD`，節點 `T1["T1 · 標題 · tier"]`（tier 優先取
// planner_risk_tier——planner 實際指派的風險等級較貼近「這個 task 現在是什麼
// 風險」；缺值時退回 minimum_risk_tier；兩者皆空則該段省略），邊
// `dep --> task`（依 depends_on 展開，缺依賴的任務不產生入邊）。translateTier：
// 這裡刻意維持純函式、不直接 import i18n——呼叫端（DagPane）注入
// `tier => resolveState(riskTierKeys, tier, t)`，未提供時退回 identity（不翻譯），
// 讓既有呼叫端與測試維持原行為。
export function planToMermaid(doc: PlanDoc, translateTier: (tier: string) => string = (tier) => tier): string {
  const nodeIds = buildNodeIdMap(doc)
  const lines = ['flowchart TD']
  for (const task of doc.tasks) {
    const nodeId = nodeIds.get(task.id)!
    const tierRaw = task.plannerRiskTier || task.minimumRiskTier
    const tier = tierRaw ? translateTier(tierRaw) : tierRaw
    const parts = [task.id, task.title, tier].filter(Boolean)
    lines.push(`  ${nodeId}["${escapeLabel(parts.join(' · '))}"]`)
  }
  for (const task of doc.tasks) {
    const nodeId = nodeIds.get(task.id)!
    for (const dep of task.dependsOn) {
      // 依賴指向的 task id 若不在 doc.tasks 內（dangling ref），退回單純 sanitize
      // ——這種 plan 本身不合法，由 internal/plan.Validate 擋，不是本函式的職責，
      // 這裡只求不崩、不誤把 dangling id 對到別的節點。
      const depNodeId = nodeIds.get(dep) ?? sanitizeNodeId(dep)
      lines.push(`  ${depNodeId} --> ${nodeId}`)
    }
  }
  return lines.join('\n')
}
