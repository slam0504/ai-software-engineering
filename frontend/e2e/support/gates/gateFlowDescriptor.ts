// B3a-2a：spec 端讀取 artifacts/gate-flow.json 的核對 helper（design v4
// §2.4／§2.5）。由 global-setup.gates.ts 單一寫入，這裡只讀取＋核對，
// **必須在 test callback 內呼叫**（不是模組頂層 import 時）——`--list`
// 模式不執行 globalSetup／test callback，若放在模組頂層會讓 `--list` 因
// 檔案不存在而失敗，牴觸 gates config 的分工（缺失／未知 flow 該在 config
// 層擋下，不是在 test body 擋）。
import fs from 'node:fs';
import path from 'node:path';
import type { RunEnv } from '../env.js';
import type { GateFlow } from './gateRouting.js';
import { GATE_FLOW_SPEC_FILE } from './gateRouting.js';

export interface GateFlowDescriptor {
  runId?: unknown;
  flow?: unknown;
  specFile?: unknown;
}

export class GateFlowDescriptorError extends Error {}

/**
 * 讀取並核對 gate-flow.json：runId 必須等於本次 run-env 的 runId，flow 必須
 * 等於呼叫端宣稱的 expectedFlow，specFile 必須等於該 flow 對應的既定檔名。
 * 任一項不符或檔案缺失／壞 JSON 一律 throw。
 */
export function verifyGateFlowDescriptor(env: Pick<RunEnv, 'artifactsDir' | 'runId'>, expectedFlow: GateFlow): GateFlowDescriptor {
  const p = path.join(env.artifactsDir, 'gate-flow.json');
  let raw: string;
  try {
    raw = fs.readFileSync(p, 'utf8');
  } catch (e) {
    throw new GateFlowDescriptorError(`verifyGateFlowDescriptor: 無法讀取 ${p}：${e instanceof Error ? e.message : String(e)}`);
  }
  let parsed: GateFlowDescriptor;
  try {
    parsed = JSON.parse(raw) as GateFlowDescriptor;
  } catch (e) {
    throw new GateFlowDescriptorError(`verifyGateFlowDescriptor: ${p} 不是合法 JSON：${e instanceof Error ? e.message : String(e)}`);
  }
  if (parsed.runId !== env.runId) {
    throw new GateFlowDescriptorError(`verifyGateFlowDescriptor: gate-flow.json runId 不符——期望 ${env.runId}，實際 ${String(parsed.runId)}`);
  }
  if (parsed.flow !== expectedFlow) {
    throw new GateFlowDescriptorError(`verifyGateFlowDescriptor: gate-flow.json flow 不符——期望 ${expectedFlow}，實際 ${String(parsed.flow)}`);
  }
  if (parsed.specFile !== GATE_FLOW_SPEC_FILE[expectedFlow]) {
    throw new GateFlowDescriptorError(
      `verifyGateFlowDescriptor: gate-flow.json specFile 與預期 flow 的 spec 檔名不符——期望 ${GATE_FLOW_SPEC_FILE[expectedFlow]}，實際 ${String(parsed.specFile)}`,
    );
  }
  return parsed;
}
