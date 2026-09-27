#!/usr/bin/env node
// bounded-spawn.mjs — B3a-CI-1 review round 5（#483 S3）。
//
// spawnSync(timeout) 不是硬性返回上限——reviewer 用 timeout-probe.mjs 實測：
// 子行程接住 SIGTERM、800ms 後才自行退出，spawnSync(timeout:250) 直到子行
// 程真的退出才返回（897ms，遠超過 250ms），且同時是 `error=ETIMEDOUT`、
// `status=0`、`signal=null`——run-batch.mjs 先前的 `passed` 判定只看
// status／verdict，這種合法的 Node 回傳形狀會被誤判成功。
//
// 這支模組改用非同步 `spawn()` + 自己實作的 SIGTERM→SIGKILL 兩段式逾時升
// 級，只對「自己持有的 ChildProcess handle」送訊號（不對外部 pid／process
// group 送）。三段時間（軟逾時／SIGKILL 前的等待／SIGKILL 後的等待）都是
// 明確常數，worst-case 總時長可核算：
//   softTimeoutMs + killGraceMs + reapGraceMs
// 這是「真正有界」的定義（decision.md S3 點 2：「不以spawnSync(timeout)宣
// 稱硬性返回界線。採真正有界的等待/停止策略」），不是寄望 spawnSync 自己的
// timeout 語意。呼叫端一律要把 error／signal／timeout／hardKilled 任一為真
// 都計入最終失敗，不論子行程最後回報的 status 是什麼（S3 點 1）。
//
// 單獨成一個模組（不是留在 run-batch.mjs 內）是為了讓
// bounded-spawn.selftest.mjs 可以用極小的 ms 值直接單元測試 SIGTERM→
// SIGKILL 升級路徑，不必透過 run-batch.mjs 整條鏈路（那條鏈路的 wrapper／
// package 各自的 production 逾時值是分鐘級，直接整合測試會讓 selftest 慢到
// 不現實）。
import { spawn } from 'node:child_process';

/**
 * @param {string} cmd
 * @param {string[]} args
 * @param {{cwd?:string, env?:object, stdio?:any, softTimeoutMs:number, killGraceMs?:number, reapGraceMs?:number}} opts
 * @returns {Promise<{status:number|null, signal:string|null, error:Error|null, timedOut:boolean, hardKilled:boolean, elapsedMs:number}>}
 */
export function boundedSpawn(cmd, args, {
  cwd, env, stdio, softTimeoutMs, killGraceMs = 15_000, reapGraceMs = 10_000,
}) {
  return new Promise((resolve) => {
    const startedAt = Date.now();
    let child;
    try {
      child = spawn(cmd, args, { cwd, env, stdio });
    } catch (err) {
      resolve({
        status: null, signal: null, error: err, timedOut: false, hardKilled: false,
        elapsedMs: Date.now() - startedAt,
      });
      return;
    }

    let settled = false;
    let timedOut = false;
    let hardKilled = false;
    let spawnError = null;
    let softTimer = null;
    let killTimer = null;
    let reapTimer = null;

    function clearAllTimers() {
      if (softTimer) clearTimeout(softTimer);
      if (killTimer) clearTimeout(killTimer);
      if (reapTimer) clearTimeout(reapTimer);
    }

    function finish(status, signal) {
      if (settled) return;
      settled = true;
      clearAllTimers();
      resolve({
        status, signal, error: spawnError, timedOut, hardKilled, elapsedMs: Date.now() - startedAt,
      });
    }

    child.on('error', (err) => {
      // spawn 本身失敗（例如 ENOENT）：不會有 'exit' 事件，直接判定。
      spawnError = err;
      finish(null, null);
    });
    child.on('exit', (code, signal) => finish(code, signal));

    softTimer = setTimeout(() => {
      timedOut = true;
      try { child.kill('SIGTERM'); } catch { /* 子行程可能已經結束，忽略 */ }
      killTimer = setTimeout(() => {
        hardKilled = true;
        try { child.kill('SIGKILL'); } catch { /* 子行程可能已經結束，忽略 */ }
        reapTimer = setTimeout(() => {
          // 已經送過 SIGKILL 仍未觀察到 'exit'：不再等待，直接判定失敗並
          // 保留診斷（timedOut／hardKilled 都是 true），不冒充 cleanup 完
          // 成——「無法確認退出就失敗並保留診斷」（S3 點 2）。只對這個
          // child handle 送過訊號，沒有越界掃描或操作任何其他 pid／
          // process group。
          finish(null, null);
        }, reapGraceMs);
      }, killGraceMs);
    }, softTimeoutMs);
  });
}

/** 一律視為失敗的判定條件（S3 點 1：error／timeout／signal 一律計入）。 */
export function isAbnormalBoundedSpawnResult(result) {
  return Boolean(result.error) || result.signal !== null || result.timedOut || result.hardKilled;
}
