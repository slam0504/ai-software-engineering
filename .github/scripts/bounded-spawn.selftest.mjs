#!/usr/bin/env node
// bounded-spawn.selftest.mjs — B3a-CI-1 review round 5（#483 S3）。
//
// 直接單元測試 bounded-spawn.mjs 的 SIGTERM→SIGKILL 升級路徑，用極小的
// ms 值（不是 run-batch.mjs 的分鐘級 production 常數），讓這支 selftest 在
// 幾秒內跑完。對照 reviewer 的 timeout-probe.mjs（見
// /Users/eason_tseng/b3a-evidence/2026-09-28-review483/timeout-probe.mjs）：
// 子行程接住 SIGTERM、晚一點才自行退出，spawnSync(timeout) 不保證在
// timeout 附近返回、也可能同時是 error=ETIMEDOUT 與 status=0——這裡逐案證
// 明 boundedSpawn() 對這些情況的處理方式。
//
// 執行：node .github/scripts/bounded-spawn.selftest.mjs
import assert from 'node:assert/strict';
import { boundedSpawn, isAbnormalBoundedSpawnResult } from './bounded-spawn.mjs';

let passed = 0;
let failed = 0;
async function check(name, fn) {
  try {
    await fn();
    passed += 1;
    console.log(`ok - ${name}`);
  } catch (e) {
    failed += 1;
    console.error(`FAIL - ${name}`);
    console.error(e);
  }
}

await check('正常路徑：child 快速正常結束 → status 正確、不算異常', async () => {
  const r = await boundedSpawn(process.execPath, ['-e', 'process.exit(0)'], { softTimeoutMs: 5000, killGraceMs: 1000, reapGraceMs: 1000 });
  assert.equal(r.status, 0);
  assert.equal(r.signal, null);
  assert.equal(r.error, null);
  assert.equal(r.timedOut, false);
  assert.equal(r.hardKilled, false);
  assert.equal(isAbnormalBoundedSpawnResult(r), false);
});

await check('正常路徑：child 非零 exit code → 如實回報，不算「異常」（呼叫端另外核對 status）', async () => {
  const r = await boundedSpawn(process.execPath, ['-e', 'process.exit(7)'], { softTimeoutMs: 5000, killGraceMs: 1000, reapGraceMs: 1000 });
  assert.equal(r.status, 7);
  assert.equal(isAbnormalBoundedSpawnResult(r), false);
});

// review round 5（#483 S3）：直接對照 reviewer 的 timeout-probe.mjs 場景
// ——child 接住 SIGTERM、晚一點才用 exit(0) 自行結束。reviewer 證實
// spawnSync(timeout) 在這種情況下可能同時回報 error=ETIMEDOUT、status=0。
// boundedSpawn() 必須把這種情況標記 timedOut=true（即使最終 status=0），
// 讓呼叫端（run-batch.mjs 的 isAbnormalBoundedSpawnResult()）一律計入失
// 敗，不論 status 是否為 0。
await check('S3 核心反例（對照 timeout-probe.mjs）：child 接住 SIGTERM、晚一點才 exit(0) → timedOut=true，即使 status=0 仍判定異常', async () => {
  // softTimeoutMs 給 300ms 的餘裕，確保 `node -e` 自己的行程啟動與註冊
  // SIGTERM handler 一定已經完成（避免與 node 啟動延遲競爭，不是在測
  // boundedSpawn 本身）；child 用 setInterval 保持行程存活（沒有這個，
  // Node 在事件迴圈空了之後會自然結束，跟我們要測的訊號時序無關），收到
  // SIGTERM 之後才啟動自己的 300ms 計時器再 exit(0)，不是從行程啟動當下
  // 算，徹底排除這個時序競爭。
  const r = await boundedSpawn(
    process.execPath,
    ['-e', "setInterval(()=>{},1000); process.on('SIGTERM',()=>{setTimeout(()=>process.exit(0),300)})"],
    { softTimeoutMs: 300, killGraceMs: 2000, reapGraceMs: 1000 },
  );
  assert.equal(r.timedOut, true);
  assert.equal(r.status, 0);
  assert.equal(isAbnormalBoundedSpawnResult(r), true, 'status=0 但 timedOut=true 仍必須整體判定異常（reviewer 的核心反例）');
});

await check('SIGTERM 升級到 SIGKILL：child 徹底忽略 SIGTERM，只有 SIGKILL 能終止它 → hardKilled=true，signal=SIGKILL', async () => {
  // softTimeoutMs 給 300ms 餘裕排除 node 啟動時序競爭（同上一案）。
  const r = await boundedSpawn(
    process.execPath,
    ['-e', "process.on('SIGTERM',()=>{}); setInterval(()=>{}, 1000)"],
    { softTimeoutMs: 300, killGraceMs: 300, reapGraceMs: 2000 },
  );
  assert.equal(r.timedOut, true);
  assert.equal(r.hardKilled, true);
  assert.equal(r.signal, 'SIGKILL');
  assert.equal(isAbnormalBoundedSpawnResult(r), true);
  // worst-case 上限核對：softTimeoutMs(300)+killGraceMs(300)+reapGraceMs(2000)=2600ms，
  // 實際應該遠低於這個上限（SIGKILL 幾乎立即生效，不需要用完 reapGraceMs）。
  assert.ok(r.elapsedMs < 300 + 300 + 2000, `elapsedMs=${r.elapsedMs} 超過理論上限`);
});

// review round 5（#483 S3）：對應決定文件要求的「package 卡住」離線反
// 例——package-e2e-evidence.sh 是 bash 腳本，run-batch.mjs 對它的
// boundedSpawn() 呼叫用同一份邏輯，這裡用 bash（不是 node）直接模擬「完全
// 忽略 TERM 的卡住子行程」，證明同一套 SIGTERM→SIGKILL 升級對 bash 子行
// 程一樣有效，不是只對 node 子行程才有效。
await check('S3「package 卡住」反例：bash 子行程 trap 掉 SIGTERM、無限迴圈 → hardKilled=true，SIGKILL 終止', async () => {
  const r = await boundedSpawn(
    'bash',
    ['-c', "trap '' TERM; while true; do sleep 0.05; done"],
    { softTimeoutMs: 300, killGraceMs: 300, reapGraceMs: 2000 },
  );
  assert.equal(r.timedOut, true);
  assert.equal(r.hardKilled, true);
  assert.equal(r.signal, 'SIGKILL');
  assert.equal(isAbnormalBoundedSpawnResult(r), true);
});

await check('spawn 本身失敗（cmd 不存在）→ 立刻回報 error，不等待 softTimeoutMs', async () => {
  const startedAt = Date.now();
  const r = await boundedSpawn('this-command-definitely-does-not-exist-xyz', [], { softTimeoutMs: 5000, killGraceMs: 1000, reapGraceMs: 1000 });
  const elapsed = Date.now() - startedAt;
  assert.ok(r.error, '應該回報 spawn error');
  assert.equal(isAbnormalBoundedSpawnResult(r), true);
  assert.ok(elapsed < 2000, `spawn 失敗應該立刻回報，不應該等到 softTimeoutMs（實際 elapsed=${elapsed}ms）`);
});

await check('只對自己持有的 child handle 送訊號：boundedSpawn 不接受、也不使用外部 pid 參數', () => {
  // 結構性核對（非執行期行為）：boundedSpawn() 的簽名只有 cmd/args/opts，
  // 沒有任何「target pid」「process group」參數，`child.kill()` 只能作用
  // 在 spawn() 自己回傳的 handle 上——這是 API 設計層面就排除越界送訊號的
  // 可能性，不是靠呼叫端自律。
  assert.equal(boundedSpawn.length, 3, 'boundedSpawn(cmd, args, opts) 應恰好 3 個參數，沒有額外的 pid/group 參數');
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed > 0 ? 1 : 0);
