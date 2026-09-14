<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

// A1b-2 Phase 1：外部變更「比較」畫面——唯讀並列兩欄。設計稿 rev7 條款 18／
// §2.9：左欄是開啟當下編輯器內容的凍結快照，右欄是開啟時重新讀取的磁碟內容
// 的凍結快照；凍結是呼叫端的責任，這個元件只呈現 props，不自行讀檔、不監看
// 外部資料。唯讀、不自動合併、不做行對齊／語法高亮／差異標記／逐段套用，也
// 不沿用 commit 預覽的 unified diff——純文字用 <pre> 原樣呈現。
const { t } = useI18n()

const props = defineProps<{
  left: string
  leftCapturedAt: Date
  right: string | null
  rightCapturedAt: Date | null
  rightError?: string
  rightLoading?: boolean
}>()
const emit = defineEmits<{ (e: 'close'): void }>()

// formatTime：HH:mm:ss、24 小時制、本地時間，自行補零——不依賴 locale 格式
// （toLocaleTimeString 依環境可能輸出 12 小時制或不同分隔符，測試無法穩定斷言）。
function formatTime(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

const leftLabel = computed(() => t('externalChange.compare.left', { time: formatTime(props.leftCapturedAt) }))
// 右欄讀取失敗／進行中時沒有有效的擷取時點可標示，標題連同內容一併不顯示，
// 只顯示失敗／載入中訊息（契約：讀取失敗時不得顯示任何右欄內容，即使 right
// 與 rightCapturedAt 仍帶有上一輪的殘留值）。
const showRightContent = computed(() => !props.rightError && !props.rightLoading && props.right !== null && props.rightCapturedAt !== null)
const rightLabel = computed(() => props.rightCapturedAt ? t('externalChange.compare.right', { time: formatTime(props.rightCapturedAt) }) : '')
</script>

<template>
  <div class="external-compare" data-test="external-compare">
    <div class="col">
      <p class="label" data-test="external-compare-left-label">{{ leftLabel }}</p>
      <pre class="content" data-test="external-compare-left">{{ left }}</pre>
    </div>
    <div class="col">
      <template v-if="rightError">
        <p class="err" data-test="external-compare-right-error">{{ t('externalChange.compare.rightFailed', { error: rightError }) }}</p>
      </template>
      <template v-else-if="rightLoading">
        <p class="notice" data-test="external-compare-right-loading">{{ t('externalChange.compare.rightLoading') }}</p>
      </template>
      <template v-else-if="showRightContent">
        <p class="label" data-test="external-compare-right-label">{{ rightLabel }}</p>
        <pre class="content" data-test="external-compare-right">{{ right }}</pre>
      </template>
    </div>
    <div class="actions">
      <button type="button" data-test="external-compare-close" @click="emit('close')">{{ t('externalChange.compare.close') }}</button>
    </div>
  </div>
</template>

<style scoped>
.external-compare { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
.col { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.label { color: var(--text-muted); font-size: var(--fs-s); margin: 0; }
.content { white-space: pre-wrap; background: var(--bg-inset); border: 1px solid var(--border); border-radius: var(--radius-s); padding: 8px; margin: 0; max-height: 320px; overflow-y: auto; }
.err { color: var(--err); font-size: var(--fs-s); }
.notice { color: var(--text-muted); font-size: var(--fs-s); }
.actions { grid-column: 1 / -1; }
</style>
