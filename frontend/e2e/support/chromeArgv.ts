// 保存 Chrome 實際的 argv／sandbox 設定證據（reviewer 新增要求）。
//
// 背景：`browser.process()` 在 Playwright 的公開 Node API 沒有這個方法（不像
// Puppeteer），之前用它探測回傳 null 不能證明 Chrome 有沒有帶某個旗標——
// 必須直接從作業系統的行程資訊讀「真正跑起來的那一行命令」才算數。
//
// 做法：這支模組跑在 Playwright worker 行程內（spec 檔呼叫），`process.pid`
// 就是 worker 自己的 pid；Chrome 是 worker 呼叫 `browser.launch()` 之後才冒出來
// 的子孫行程，用 descendantsOf(worker pid) 找，天然不會誤認使用者自己另外
// 開的 Chrome（那個跟 worker 沒有親代關係）。用命令列排除 "Helper" 取得主
// 程序（不是 renderer／GPU／plugin helper）。
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { descendantsOf, snapshotProcessTableWithCommand, ToolObservationError } from './psUtil.js';

export function captureChromeArgv(artifactsDir: string): void {
  const harnessLogPath = path.join(artifactsDir, 'harness.log');
  const ts = new Date().toISOString();
  const appendLog = (msg: string) => {
    try { fs.appendFileSync(harnessLogPath, `[${ts}] ${msg}\n`); } catch { /* 證據擷取本身失敗不應該讓測試掛掉 */ }
  };

  const ownWorkerPid = process.pid;
  let table: ReturnType<typeof snapshotProcessTableWithCommand>;
  try {
    table = snapshotProcessTableWithCommand();
  } catch (e) {
    const reason = e instanceof ToolObservationError ? e.message : String(e);
    appendLog(`chrome-argv 擷取失敗：無法取得行程快照（${reason}）`);
    return;
  }
  const treePids = descendantsOf(ownWorkerPid, table);
  const chromeMain = treePids.find(p => p.command.includes('Google Chrome') && !p.command.includes('Helper'));

  if (!chromeMain) {
    appendLog(`chrome-argv 擷取失敗：在 worker pid=${ownWorkerPid} 的後代樹（共 ${treePids.length} 個 pid）中找不到 Chrome 主程序（非 Helper）`);
    return;
  }

  let full: string;
  try {
    full = execFileSync('ps', ['-o', 'pid=,command=', '-p', String(chromeMain.pid)], { encoding: 'utf8' }).trim();
  } catch (e) {
    appendLog(`chrome-argv 擷取失敗：ps -p ${chromeMain.pid} 執行錯誤（程序可能已經結束）：${String(e)}`);
    return;
  }
  if (!full) {
    appendLog(`chrome-argv 擷取失敗：ps -p ${chromeMain.pid} 沒有回傳任何內容（程序可能已經結束）`);
    return;
  }

  const content = `captured_at: ${ts}\nworker_pid: ${ownWorkerPid}\nchrome_main_pid: ${chromeMain.pid}\nfull_command:\n${full}\n`;
  fs.writeFileSync(path.join(artifactsDir, 'chrome-argv.txt'), content);
  appendLog(`chrome-argv 擷取完成：chrome_main_pid=${chromeMain.pid}，見 chrome-argv.txt`);
}
