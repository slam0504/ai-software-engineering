import { describe, it, expect } from 'vitest'
import ExternalChangeCompare from './ExternalChangeCompare.vue'
import { mountWithI18n } from '../test/i18n'

const leftAt = new Date(2026, 8, 14, 9, 5, 7)
const rightAt = new Date(2026, 8, 14, 10, 1, 2)

describe('ExternalChangeCompare', () => {
  it('renders left and right content verbatim, including multiline and surrounding whitespace', () => {
    const left = '  line one\nline two  \n\nline four'
    const right = 'disk line one\n  disk line two'
    const w = mountWithI18n(ExternalChangeCompare, {
      props: { left, leftCapturedAt: leftAt, right, rightCapturedAt: rightAt },
    })
    // .text() (vue-test-utils) trims leading/trailing whitespace, which would
    // hide a real bug where the component itself trims content — read raw
    // textContent instead so leading/trailing whitespace is actually verified.
    expect(w.find('[data-test=external-compare-left]').element.textContent).toBe(left)
    expect(w.find('[data-test=external-compare-right]').element.textContent).toBe(right)
  })

  it('labels include source text and HH:mm:ss capture time', () => {
    const w = mountWithI18n(ExternalChangeCompare, {
      props: { left: 'a', leftCapturedAt: leftAt, right: 'b', rightCapturedAt: rightAt },
    })
    const leftLabel = w.find('[data-test=external-compare-left-label]').text()
    const rightLabel = w.find('[data-test=external-compare-right-label]').text()
    expect(leftLabel).toContain('09:05:07')
    expect(leftLabel).toContain('目前編輯器內容')
    expect(rightLabel).toContain('10:01:02')
    expect(rightLabel).toContain('磁碟內容')
  })

  it('right read failure: shows failure message and does not render right content even when right prop is non-empty', () => {
    const w = mountWithI18n(ExternalChangeCompare, {
      props: {
        left: 'a', leftCapturedAt: leftAt,
        right: 'STALE CONTENT THAT MUST NOT BE SHOWN', rightCapturedAt: rightAt,
        rightError: 'permission denied',
      },
    })
    const err = w.find('[data-test=external-compare-right-error]')
    expect(err.exists()).toBe(true)
    expect(err.text()).toContain('permission denied')
    expect(w.find('[data-test=external-compare-right]').exists()).toBe(false)
    expect(w.html()).not.toContain('STALE CONTENT THAT MUST NOT BE SHOWN')
  })

  it('right loading: shows loading indicator and no right content', () => {
    const w = mountWithI18n(ExternalChangeCompare, {
      props: {
        left: 'a', leftCapturedAt: leftAt,
        right: 'should not appear while loading', rightCapturedAt: rightAt,
        rightLoading: true,
      },
    })
    expect(w.find('[data-test=external-compare-right-loading]').exists()).toBe(true)
    expect(w.find('[data-test=external-compare-right]').exists()).toBe(false)
  })

  it('clicking close emits close', async () => {
    const w = mountWithI18n(ExternalChangeCompare, {
      props: { left: 'a', leftCapturedAt: leftAt, right: 'b', rightCapturedAt: rightAt },
    })
    await w.find('[data-test=external-compare-close]').trigger('click')
    expect(w.emitted('close')?.length).toBe(1)
  })

  it('is read-only: no textarea, input, or contenteditable element', () => {
    const w = mountWithI18n(ExternalChangeCompare, {
      props: { left: 'a', leftCapturedAt: leftAt, right: 'b', rightCapturedAt: rightAt },
    })
    expect(w.find('textarea').exists()).toBe(false)
    expect(w.find('input').exists()).toBe(false)
    expect(w.find('[contenteditable]').exists()).toBe(false)
  })

  it('shows no diff markup even when left and right differ', () => {
    const left = '+ this looks like a diff add\n- this looks like a diff remove'
    const right = '- this looks like a diff remove\n+ this looks like a diff add'
    const w = mountWithI18n(ExternalChangeCompare, {
      props: { left, leftCapturedAt: leftAt, right, rightCapturedAt: rightAt },
    })
    expect(w.html()).not.toContain('<ins')
    expect(w.html()).not.toContain('<del')
    // content is emitted verbatim (including any literal +/- chars from the
    // source text itself), so we only assert no diff-styling elements exist —
    // covered separately by the verbatim-content test above.
    expect(w.find('.diff').exists()).toBe(false)
  })
})
