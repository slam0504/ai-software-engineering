import { defineStore } from 'pinia'
import type { escalation } from '../../wailsjs/go/models'

interface State {
  entries: escalation.Entry[]
  unavailable: string // EscalationList 失敗時的錯誤原文；空字串＝可用
  loadSeq: number // A2-1：每次 load 遞增；回應到達時序號已變＝舊回應，整個結果丟棄
}

// escalation store（Task 25，spec §3.8／§6）：EscalationInbox 與 App.vue 的
// 未 resolved badge 共用的 projection——鏡射 gate store 的角色，但 gate store
// 是純事件 reducer，這裡沒有專屬事件 lane（brief 明講不加），改用
// GateConsole／TcaWorkspace 既有的「props 注入的 wails 函式＋store 落地」
// 慣例：load() 接收真正的 EscalationList 綁定（生產路徑在 App.vue 注入），
// 不在 store 內 import wailsjs——保持與其他 store 一致，也讓測試不必碰真實
// binding。Project 失敗（後端 §3.8：收件匣不可用）回錯原文落地到
// unavailable，絕不裝空。
export const useEscalation = defineStore('escalation', {
  state: (): State => ({ entries: [], unavailable: '', loadSeq: 0 }),

  getters: {
    open: (s): escalation.Entry[] => s.entries.filter(e => e.State === 'open'),
    acknowledged: (s): escalation.Entry[] => s.entries.filter(e => e.State === 'acknowledged'),
    resolved: (s): escalation.Entry[] => s.entries.filter(e => e.State === 'resolved'),
    unresolvedCount: (s): number => s.entries.filter(e => e.State !== 'resolved').length,
  },

  actions: {
    async load(list: () => Promise<escalation.Entry[]>) {
      const seq = ++this.loadSeq // A2-1：連續 load() 時，只有最新一次的回應能落地
      try {
        const entries = await list()
        if (seq !== this.loadSeq) return // 序號已被更新的 load() 蓋過，這次回應是舊的，丟棄
        this.entries = entries
        this.unavailable = ''
      } catch (e) {
        if (seq !== this.loadSeq) return // 同上：舊呼叫的失敗不得覆蓋新狀態
        this.unavailable = String(e) // 錯誤原文顯示，不裝空（§3.8）
      }
    },
  },
})
