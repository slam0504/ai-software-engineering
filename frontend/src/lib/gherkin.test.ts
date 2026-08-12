import { describe, expect, it } from 'vitest'
import { extractGherkin } from './gherkin'

describe('extractGherkin', () => {
  it('extracts a single gherkin fence, dropping surrounding prose', () => {
    const md = '我沒辦法直接讀寫檔案，這是草稿：\n\n```gherkin\nFeature: 登入\n  Scenario: 成功登入\n```\n\n希望有幫助！'
    expect(extractGherkin(md)).toBe('Feature: 登入\n  Scenario: 成功登入')
  })

  it('concatenates two gherkin blocks with a blank line', () => {
    const md = '```gherkin\nFeature: A\n```\n說明文字\n```gherkin\nFeature: B\n```'
    expect(extractGherkin(md)).toBe('Feature: A\n\nFeature: B')
  })

  it('extracts a feature-labeled fence as an alias for gherkin', () => {
    const md = '前言\n```feature\nFeature: 登出\n```\n'
    expect(extractGherkin(md)).toBe('Feature: 登出')
  })

  it('falls back to a generic fence when no gherkin/feature fence exists', () => {
    const md = '一些說明\n```\nFeature: 通用\n```\n結語'
    expect(extractGherkin(md)).toBe('Feature: 通用')
  })

  it('returns the trimmed input unchanged when there is no fence at all', () => {
    const md = '  plain text, no fence here  '
    expect(extractGherkin(md)).toBe('plain text, no fence here')
  })

  it('takes an unterminated final fence to the end of the string', () => {
    const md = '前言\n```gherkin\nFeature: 未收尾\n  Scenario: 缺少結尾 fence'
    expect(extractGherkin(md)).toBe('Feature: 未收尾\n  Scenario: 缺少結尾 fence')
  })

  it('handles CRLF line endings inside a fence', () => {
    const md = '```gherkin\r\nFeature: CRLF\r\n  Scenario: 換行\r\n```\r\n'
    expect(extractGherkin(md)).toBe('Feature: CRLF\n  Scenario: 換行')
  })
})
