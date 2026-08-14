import { describe, it, expect } from 'vitest'
import { parseStaleTarget, resolveResubmitTarget } from './staleNav'

describe('parseStaleTarget', () => {
  it('gate1：subject 不含冒號', () => {
    expect(parseStaleTarget('stale:gate1:workspace')).toEqual({ gate: 'gate1', subject: 'workspace' })
  })
  it('gate2：subject 自身含冒號（plan:P1），不可全字串 split', () => {
    expect(parseStaleTarget('stale:gate2:plan:P1')).toEqual({ gate: 'gate2', subject: 'plan:P1' })
  })
  it('test_contract_approval：subject 帶 "/"', () => {
    expect(parseStaleTarget('stale:test_contract_approval:task:P1/T1'))
      .toEqual({ gate: 'test_contract_approval', subject: 'task:P1/T1' })
  })
  it('未知 gate → null', () => {
    expect(parseStaleTarget('stale:gate3:workspace')).toBeNull()
  })
  it('空 subject → null', () => {
    expect(parseStaleTarget('stale:gate1:')).toBeNull()
  })
  it('缺段（無 "stale:" 前綴）→ null', () => {
    expect(parseStaleTarget('gate1:workspace')).toBeNull()
  })
  it('缺段（無結尾冒號分段）→ null', () => {
    expect(parseStaleTarget('stale:gate1')).toBeNull()
  })
  it('空字串 → null', () => {
    expect(parseStaleTarget('')).toBeNull()
  })
})

describe('resolveResubmitTarget', () => {
  it('gate1 → { kind: gate1 }（無 subject 需解析）', () => {
    expect(resolveResubmitTarget('gate1', 'workspace')).toEqual({ kind: 'gate1' })
  })
  it('gate2 + plan:<id> → { kind: gate2, planId }', () => {
    expect(resolveResubmitTarget('gate2', 'plan:P1')).toEqual({ kind: 'gate2', planId: 'P1' })
  })
  it('test_contract_approval + task:<plan>/<task> → { kind: tca, planId, taskId }', () => {
    expect(resolveResubmitTarget('test_contract_approval', 'task:P1/T1'))
      .toEqual({ kind: 'tca', planId: 'P1', taskId: 'T1' })
  })
  it('畸形 subject：gate2 "plan:"（空 id）→ null', () => {
    expect(resolveResubmitTarget('gate2', 'plan:')).toBeNull()
  })
  it('畸形 subject：tca "task:x"（缺 "/"）→ null', () => {
    expect(resolveResubmitTarget('test_contract_approval', 'task:x')).toBeNull()
  })
  it('畸形 subject：tca "task:/T1"（plan 段為空）→ null', () => {
    expect(resolveResubmitTarget('test_contract_approval', 'task:/T1')).toBeNull()
  })
  it('畸形 subject：tca "task:P1/"（task 段為空）→ null', () => {
    expect(resolveResubmitTarget('test_contract_approval', 'task:P1/')).toBeNull()
  })
  it('未知 gate → null', () => {
    expect(resolveResubmitTarget('gate3', 'workspace')).toBeNull()
  })
})
