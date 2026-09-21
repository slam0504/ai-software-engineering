// B3a-2b-2 F2：app ready **之後**的 Claude setup 失敗收尾。
//
// 抽成獨立模組的理由：這段邏輯必須能用受控 stub 驗證「清理一定會被嘗試」，
// 而不是只能靠開真 App 才跑得到（reviewer #369）。
//
// 契約：
//   - 記 log 與標記 failed 各自獨立包起來——**其中任何一個自己 throw 都不得
//     讓 teardown 被跳過**（原本寫在同一個 catch 裡就有這個破口）。
//   - teardown 一定會被嘗試；它自己失敗也只記錄，不掩蓋原始錯誤。
//   - 原始錯誤由呼叫端 rethrow，本函式不吞掉。
export interface ClaudeSetupFailureDeps {
  log: { log(message: string): void };
  runState: { setStatus(status: 'failed', message: string): void };
  teardown: () => Promise<void>;
  onDiagnostic?: (message: string) => void;
}

export interface ClaudeSetupFailureOutcome {
  message: string;
  /** 記 log 這一步自己的失敗（非 null 代表 log 壞了，但流程仍繼續）。 */
  logError: string | null;
  /** 標記 failed 這一步自己的失敗。 */
  statusError: string | null;
  /** 是否真的嘗試過清理——**任何情況下都必須為 true**。 */
  teardownAttempted: boolean;
  /** 清理本身的失敗（不掩蓋原始錯誤）。 */
  teardownError: string | null;
}

export async function handleClaudeSetupFailure(
  error: unknown, deps: ClaudeSetupFailureDeps,
): Promise<ClaudeSetupFailureOutcome> {
  const message = error instanceof Error ? error.message : String(error);
  const diag = deps.onDiagnostic ?? ((m: string) => { console.error(m); });

  let logError: string | null = null;
  try {
    deps.log.log(`Claude setup 在 app ready 之後失敗：${message}`);
  } catch (e) {
    logError = String(e);
    diag(`[harness] 記錄 Claude setup 失敗訊息本身失敗（不影響收尾繼續執行）：${logError}`);
  }

  let statusError: string | null = null;
  try {
    deps.runState.setStatus('failed', `claude setup failed: ${message}`);
  } catch (e) {
    statusError = String(e);
    diag(`[harness] 標記 failed 本身失敗（不影響收尾繼續執行）：${statusError}`);
  }

  let teardownError: string | null = null;
  try {
    await deps.teardown();
  } catch (e) {
    teardownError = String(e);
    diag(`[harness] 失敗收尾本身失敗（不掩蓋原始錯誤）：${teardownError}`);
  }

  return { message, logError, statusError, teardownAttempted: true, teardownError };
}
