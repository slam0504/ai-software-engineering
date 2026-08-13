// diagramSyntax.test.ts — mermaid diagram-as-code 語法驗證（Task 26）：
// DiagramPane/DagPane 的既有測試一律 vi.mock('mermaid')，從未真的解析過
// docs/architecture/diagrams/*.mmd 或 README 內嵌的 ```mermaid``` 區塊——
// frontend build（vue-tsc + vite）也不會驗獨立 .mmd 檔案的語法。這裡補上：
// 用真正的 mermaid.parse() 逐一驗證，parse 失敗即測試失敗（圖與實作偏差同
// PR 修正，見 README「架構圖」一節）。
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it, expect } from 'vitest'
import mermaid from 'mermaid'

// __dirname = frontend/src/lib；repo root 在往上三層。
const repoRoot = join(__dirname, '..', '..', '..')
const diagramsDir = join(repoRoot, 'docs', 'architecture', 'diagrams')
const readmePath = join(repoRoot, 'README.md')

mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'dark' })

function mmdFiles(): string[] {
  return readdirSync(diagramsDir).filter(f => f.endsWith('.mmd')).sort()
}

// extractMermaidBlocks — 抽出 README 內每個 ```mermaid ... ``` 區塊的原始內容，
// 同 keyRefs.test.ts 掃檔慣例（正規表示式掃 markdown，不引入 markdown parser）。
function extractMermaidBlocks(md: string): string[] {
  const re = /```mermaid\n([\s\S]*?)```/g
  const blocks: string[] = []
  for (const m of md.matchAll(re)) blocks.push(m[1])
  return blocks
}

describe('docs/architecture/diagrams/*.mmd 語法驗證', () => {
  for (const file of mmdFiles()) {
    it(`${file} 是合法 mermaid 語法`, async () => {
      const content = readFileSync(join(diagramsDir, file), 'utf8')
      await expect(mermaid.parse(content)).resolves.not.toThrow()
    })
  }
})

describe('README.md 內嵌 mermaid 區塊語法驗證', () => {
  const md = readFileSync(readmePath, 'utf8')
  const blocks = extractMermaidBlocks(md)

  it('README 至少含一個 mermaid 區塊（回歸保護：正規表示式沒抽空）', () => {
    expect(blocks.length).toBeGreaterThan(0)
  })

  blocks.forEach((block, i) => {
    it(`README mermaid 區塊 #${i + 1} 是合法語法`, async () => {
      await expect(mermaid.parse(block)).resolves.not.toThrow()
    })
  })
})
