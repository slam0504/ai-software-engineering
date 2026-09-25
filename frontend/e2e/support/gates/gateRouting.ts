// B3a-2a：gates 入口（playwright.gates.config.ts／global-setup.gates.ts）的
// flow 解析——純函式，鏡射 support/scenario/specRouting.ts 的檔案角色，但
// **行為刻意不同**（reviewer #424 (d) 裁定）：scenario 入口對缺失／未知
// E2E_SCENARIO 回傳中性預設值（讓 --list 能載入 config，拒絕留給
// global-setup.scenario.ts 的 resolveScenario()）；gates 入口不套用同一個
// fallback 慣例——缺失／空字串／未知值一律在這裡直接 throw，包含 --list，
// 不給中性預設。一個 run-id 只跑指定的一支 gates spec。
export type GateFlow = 'gate1' | 'gate2' | 'stale';

export const GATE_FLOW_SPEC_FILE: Record<GateFlow, string> = {
  gate1: 'gate1.spec.ts',
  gate2: 'gate2.spec.ts',
  stale: 'stale.spec.ts',
};

const KNOWN_FLOWS: readonly GateFlow[] = ['gate1', 'gate2', 'stale'];

function isGateFlow(v: string): v is GateFlow {
  return (KNOWN_FLOWS as readonly string[]).includes(v);
}

/**
 * resolveGateFlow：E2E_GATE 的嚴格 resolver。缺失（undefined）、空字串、
 * 未知值一律 throw——不回退到任何「預設案」。呼叫端（playwright.gates.config.ts
 * 的頂層、global-setup.gates.ts 的開頭）都必須直接把這裡的例外往外拋，
 * 讓 `--list` 與實際執行都在同一個地方失敗，不允許中間層默默換成別的 flow。
 */
export function resolveGateFlow(raw: string | undefined): GateFlow {
  if (raw === undefined || raw === '') {
    throw new Error(
      `resolveGateFlow: E2E_GATE 未設定或為空字串（${JSON.stringify(raw)}）——`
      + 'gates 入口不提供預設 flow，必須明確指定 gate1｜gate2｜stale',
    );
  }
  if (!isGateFlow(raw)) {
    throw new Error(`resolveGateFlow: 未知的 E2E_GATE 值 ${JSON.stringify(raw)}——僅接受 gate1｜gate2｜stale`);
  }
  return raw;
}

/** 供 config／spec 用：某個 flow 對應的唯一 spec 檔名。 */
export function specFileForFlow(flow: GateFlow): string {
  return GATE_FLOW_SPEC_FILE[flow];
}
