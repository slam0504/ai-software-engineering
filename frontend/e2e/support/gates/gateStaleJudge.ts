// B3a-2a：STALE flow（S-N1／S-P1）的判定邏輯——抽成純函式（review #427
// point 5：「判定邏輯要抽成純函式放在 support，才能寫 selftest」），不含
// Playwright／fs 依賴，方便寫離線負控制（錯誤 digest／錯誤 evidence_ref
// 都必須被拒絕）。

export interface BindingJudgeResult {
  ok: boolean;
  mismatches: string[];
}

/**
 * S-N1 用：逐一比對「實際 bindings」與「獨立重算的期望值」，只要有一個
 * kind 對不上就判 fail，並列出所有不符的 kind（不是只回一個 boolean）。
 */
export function judgeBindingsUnchanged(actual: Readonly<Record<string, string>>, expected: Readonly<Record<string, string>>): BindingJudgeResult {
  const mismatches: string[] = [];
  for (const kind of Object.keys(expected)) {
    if (actual[kind] !== expected[kind]) {
      mismatches.push(`${kind}: actual=${JSON.stringify(actual[kind])} expected=${JSON.stringify(expected[kind])}`);
    }
  }
  return { ok: mismatches.length === 0, mismatches };
}

export interface StaleTransitionExpectation {
  cause: string;
  evidenceRef: string;
}

export interface StaleTransitionJudgeResult {
  ok: boolean;
  reason?: string;
}

/**
 * S-P1 用：判定一筆 transition 是否符合預期的 cause／evidence_ref——找不到
 * transition、cause 不符、evidence_ref 不符都判 fail 並說明原因。
 */
export function judgeStaleTransition(
  transition: { cause?: unknown; evidence_ref?: unknown; to?: unknown } | undefined,
  expected: StaleTransitionExpectation,
): StaleTransitionJudgeResult {
  if (!transition) return { ok: false, reason: 'transition 不存在' };
  if (transition.to !== 'stale') return { ok: false, reason: `to 不是 stale（實際：${JSON.stringify(transition.to)}）` };
  if (transition.cause !== expected.cause) {
    return { ok: false, reason: `cause 不符：實際=${JSON.stringify(transition.cause)} 期望=${JSON.stringify(expected.cause)}` };
  }
  if (transition.evidence_ref !== expected.evidenceRef) {
    return { ok: false, reason: `evidence_ref 不符：實際=${JSON.stringify(transition.evidence_ref)} 期望=${JSON.stringify(expected.evidenceRef)}` };
  }
  return { ok: true };
}
