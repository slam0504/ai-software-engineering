// fixture-grandchild-holds-pipe.child.mjs — 孫行程本體（新增診斷檔，有界
// fixture）。
// 不回應任何訊號邏輯（模擬 wrapper 完全不知道、也管不到的後代行程）；明確
// 的自我 deadline：無論如何最晚 4 秒後自行結束，寫入自己的退出證據到獨立
// 檔案（不能倚賴繼承來的 stdout——那組管線可能已經沒有讀者）。
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';

const evidenceDir = process.env.GRANDCHILD_EVIDENCE_DIR || os.tmpdir();
const evidenceFile = path.join(evidenceDir, `grandchild-exit-${process.pid}.json`);

setTimeout(() => {
  try {
    fs.writeFileSync(evidenceFile, `${JSON.stringify({
      pid: process.pid,
      exitedAtIso: new Date().toISOString(),
      reason: 'self-deadline 4s reached (bounded fixture, holds inherited stdio pipe open until this point)',
    }, null, 2)}\n`);
  } catch {
    // 診斷檔寫入失敗不阻擋自我結束——這是有界 fixture 的硬性要求。
  }
  process.exit(0);
}, 4_000).unref?.();
setInterval(() => {}, 1_000_000);
