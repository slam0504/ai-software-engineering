<script setup lang="ts">
import { useI18n } from 'vue-i18n'

// A1b-2 Phase 1：外部變更三選一按鈕列。只負責呈現三個按鈕與 emit 對應事件——
// 訊息文字（外部變更偵測到了什麼）由呼叫端既有的 [data-test=external-change]／
// [data-test=external-abort] 顯示，不放進這個元件（避免與既有逐字斷言衝突）。
// 接線（何時顯示、按下後做什麼）留給 Phase 2。
const { t } = useI18n()

withDefaults(defineProps<{ disabled?: boolean }>(), { disabled: false })
const emit = defineEmits<{ (e: 'reload'): void; (e: 'compare'): void; (e: 'keep'): void }>()
</script>

<template>
  <div class="external-choice" data-test="external-choice">
    <button
      type="button" data-test="external-choice-reload" :disabled="disabled"
      @click="emit('reload')"
    >{{ t('externalChange.choice.reload') }}</button>
    <button
      type="button" data-test="external-choice-compare" :disabled="disabled"
      @click="emit('compare')"
    >{{ t('externalChange.choice.compare') }}</button>
    <button
      type="button" data-test="external-choice-keep" :disabled="disabled"
      @click="emit('keep')"
    >{{ t('externalChange.choice.keep') }}</button>
  </div>
</template>

<style scoped>
.external-choice { display: flex; gap: 6px; flex-wrap: wrap; }
</style>
