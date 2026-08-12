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
// 第一個字元）、depends_on 的 block list 項目再多縮 2 space。不支援其他縮排
// 深度、多行 scalar（| / >）、或欄位順序以外的 YAML 特性；解析失敗（缺
// plan_id、缺 tasks、task 缺 id）一律回傳 null，呼叫端（DagPane）決定如何
// 呈現錯誤，不在這裡拋例外或猜測。

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
    // 等後續 "      - X" 行才收集依賴——見下方迴圈。
    let pendingDepsBlock = false

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
        const dep = /^ {6}- (.*)$/.exec(line)
        if (dep) { current.dependsOn.push(stripScalar(dep[1])); continue }
        pendingDepsBlock = false // 縮排不符 → depends_on block 結束，往下當一般欄位處理
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
export function sanitizeNodeId(id: string): string {
  return id.replace(/[^A-Za-z0-9_-]/g, '_')
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
// `dep --> task`（依 depends_on 展開，缺依賴的任務不產生入邊）。
export function planToMermaid(doc: PlanDoc): string {
  const lines = ['flowchart TD']
  for (const task of doc.tasks) {
    const nodeId = sanitizeNodeId(task.id)
    const tier = task.plannerRiskTier || task.minimumRiskTier
    const parts = [task.id, task.title, tier].filter(Boolean)
    lines.push(`  ${nodeId}["${escapeLabel(parts.join(' · '))}"]`)
  }
  for (const task of doc.tasks) {
    const nodeId = sanitizeNodeId(task.id)
    for (const dep of task.dependsOn) {
      lines.push(`  ${sanitizeNodeId(dep)} --> ${nodeId}`)
    }
  }
  return lines.join('\n')
}
