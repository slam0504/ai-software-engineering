<script setup lang="ts">
import PaneView from './PaneView.vue'

// DualPane（Task 28，spec §3.7／§4）：固定 50/50 grid，兩個 PaneView 各自綁定
// `s.pins[0]`／`s.pins[1]`。兩個 PaneView 元件實例本身不隨釘選內容改變而重新
// 掛載（v-for 不涉及，就兩個固定 slot），切焦點只是換 CSS class＋composer
// 有無，不卸載、不重設捲動（§3.7 凍結）。
</script>

<template>
  <div class="dual-pane">
    <PaneView :idx="0" />
    <PaneView :idx="1" />
  </div>
</template>

<style scoped>
.dual-pane { display: grid; grid-template-columns: 1fr 1fr; height: 100%; min-height: 0; }
.dual-pane > :first-child { border-right: 1px solid var(--border); }
</style>
