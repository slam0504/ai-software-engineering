// extractGherkin：Accept 只寫「```gherkin fenced block」內容，不寫 assistant 的
// 整段 prose（見 SpecWorkspace.vue acceptDraft）。純函式，方便獨立測試涵蓋各種
// fence 型態（gherkin/feature/generic/無 fence/未收尾 fence）。
const FENCE_RE = /```[ \t]*([a-zA-Z0-9_-]*)[ \t]*\r?\n([\s\S]*?)(?:\r?\n```|$)/g

export function extractGherkin(md: string): string {
  const normalized = md.replace(/\r\n/g, '\n')

  const gherkinBlocks: string[] = []
  const otherBlocks: string[] = []

  let match: RegExpExecArray | null
  FENCE_RE.lastIndex = 0
  while ((match = FENCE_RE.exec(normalized))) {
    const info = match[1].trim().toLowerCase()
    const content = match[2]
    if (info === 'gherkin' || info === 'feature') {
      gherkinBlocks.push(content.trim())
    } else {
      otherBlocks.push(content.trim())
    }
  }

  if (gherkinBlocks.length > 0) return gherkinBlocks.join('\n\n').trim()
  if (otherBlocks.length > 0) return otherBlocks.join('\n\n').trim()
  return md.trim()
}
