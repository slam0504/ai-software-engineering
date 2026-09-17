// 供 lateWriteAfterStop.selftest.ts 的端到端受控回歸使用的獨立子行程腳本
// （B3a-1 offline attempt-1 crash-after-pass 修正，2026-09-17）。刻意獨立成
// 另一個真正的 Node 行程（不是 in-process 呼叫）——要驗證的正是「未捕捉例外
// 會不會讓整個命令非零結束」這件事本身，同一個行程裡沒辦法安全重現一次真的
// 未捕捉例外而不影響呼叫端 selftest 自己的執行。
//
// 只忠實重現 global-setup.ts／global-teardown.ts 這條路徑真正有風險的最小切片：
//   1. 建立 HarnessLogger（跟 globalSetup 一樣，寫真正的 harness.log）。
//   2. spawn 一個真實的短命子行程（detached，比照 processTree.ts 的
//      spawnWailsDev），註冊 `child.on('exit', ...)`——跟 global-setup.ts:258
//      完全同一種寫法：直接呼叫 `log.log`，沒有額外防護。
//   3. 呼叫真正的 `stopProcessGroup`（不是假的整個函式，只有 `deps.snapshot`
//      被換成「每次查詢都回報查無此 pid」的假 ps）——這不是賭系統負載，是
//      決定性地構造「ps 觀測跑在 Node 自己的 exit 回呼前面」這個已經在真實
//      log 重現過的時序（見
//      frontend/e2e/.artifacts/1b-final-20260917T021716Z/item3-offline/
//      attempt-1-crash-after-pass/stdout.log 的實際堆疊）。送出 TERM 之後
//      立刻進入等待迴圈的第一次查詢，此時真正的子行程幾乎必然還沒被 Node
//      自己的事件迴圈回報結束——若 heldChild 沒有被優先信任，會被這一輪
//      「查無此 pid」誤判成已死。
//   4. `stopProcessGroup` resolve 後，依環境變數指定的模式模擬
//      global-teardown.ts 最後一步：
//        - mode=default：PASSED 且未設 E2E_KEEP_ARTIFACTS 的路徑，
//          立刻 `fs.rmSync(artifactsDir, ...)`。
//        - mode=keep：E2E_KEEP_ARTIFACTS=1 的路徑，不刪，讀回 harness.log
//          內容供呼叫端核對是否留下完整的最後紀錄。
//   5. 印出一行 `RESULT ...` 給呼叫端解析後正常結束。如果修法沒生效、
//      exit listener 在清理之後才觸發，會在這裡真的丟出未捕捉的
//      ENOENT——那正是這支腳本要驗證的東西，讓它真的把這個 Node 行程弄死
//      （非零 exit code），呼叫端據此判斷有沒有重現／修好。
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import { HarnessLogger } from '../logger.ts';
import { type StopProcedureDeps, stopProcessGroup } from '../stopProcedure.ts';
import type { TrackedProc } from '../runState.ts';

const artifactsDir = process.env.LATE_WRITE_FIXTURE_ARTIFACTS_DIR;
const mode = process.env.LATE_WRITE_FIXTURE_MODE; // 'default' | 'keep'
if (!artifactsDir || !mode) {
  console.error('缺少 LATE_WRITE_FIXTURE_ARTIFACTS_DIR／LATE_WRITE_FIXTURE_MODE');
  process.exit(2);
}

const log = new HarnessLogger(artifactsDir);

// detached:true，比照 processTree.spawnWailsDev——讓它成為自己的 process
// group leader，killGroup 的 `-pgid` 才不會誤傷呼叫端自己的 process group。
const child = spawn(process.execPath, ['-e', 'setTimeout(() => process.exit(0), 80)'], {
  detached: true,
  stdio: 'ignore',
});
const pid = child.pid;
if (!pid) {
  console.error('spawn 假子行程失敗：沒有拿到 pid');
  process.exit(2);
}
const pgid = pid;
const startedAt = new Date().toString();
const command = `${process.execPath} -e setTimeout...`;

// 完全比照 global-setup.ts:258 的寫法（修法前的狀態）：沒有額外的 try/catch
// 防護，直接呼叫 `log.log`。
child.on('exit', (code, signal) => {
  log.log(`wails dev 行程結束：exit code=${code} signal=${signal}`);
});

const tracked: TrackedProc[] = [{ pid, ppid: process.pid, pgid, command, startedAt, samePgid: true }];

let snapshotCalls = 0;
const deps: StopProcedureDeps = {
  // 每次查詢都回報查無此 pid——決定性地模擬「ps 觀測跑在 Node 自己的 exit
  // 回呼前面」，不依賴真實系統負載才偶爾命中的競態視窗。
  snapshot: () => { snapshotCalls += 1; return []; },
  kill: (targetPid, signal) => process.kill(targetPid, signal),
};

async function main(): Promise<void> {
  const result = await stopProcessGroup(tracked, log, child, deps);
  log.log(`stopProcessGroup 結果：clean=${result.clean} residualPids=${result.residualPids.join(',')}`);

  if (mode === 'default') {
    fs.rmSync(artifactsDir!, { recursive: true, force: true });
  }
  // mode === 'keep'：什麼都不刪，讓呼叫端讀回 harness.log 核對內容。

  console.log(`RESULT snapshotCalls=${snapshotCalls} clean=${result.clean} residualPids=${result.residualPids.join(',')}`);
}

void main();
