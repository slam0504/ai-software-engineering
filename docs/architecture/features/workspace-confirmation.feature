Feature: 高風險 workspace 必須先經使用者確認，才能開始 AI 工作

  Scenario: 從 Finder 開啟退回家目錄，確認前送出訊息被拒（R1 (a)、R2）
    Given 我從 Finder 開啟 app，workspace 退回使用者家目錄
    Then 頂部列出現高風險警示，列出實際目錄、來源 home 與原因
    And 送出訊息與 Spec／Plan 的 AI 輔助都是停用的
    When 前端被繞過而直接呼叫 StartSession
    Then 後端回 workspace_confirmation_required，沒有任何 provider 行程、broker、MCP config、recorder 被建立
    And 後端 best-effort 嘗試寫入一筆 workspace_confirmation_blocked 稽核（不含 prompt）；稽核寫入失敗不影響拒絕本身

  Scenario: 明確確認之後可以開始，且同一次執行內所有 session 共用（R3、R4）
    Given workspace 處於待確認，且 audit writer 運作正常
    When 我按下「確認在此目錄進行 AI 工作」並在對話中再確認一次
    Then 後端把狀態轉為已確認，並 best-effort 嘗試寫入一筆 workspace_confirmed 稽核（audit writer 正常時恰好留下一筆）
    And 稽核寫入失敗不阻擋確認與後續啟動
    And 所有 session 與 Spec／Plan 的 AI 輔助都可以開始
    When 我重複確認
    Then 結果成功，狀態不變，不再嘗試寫稽核

  Scenario: 取消確認不改變狀態（R5）
    Given 警示的確認對話已開啟
    When 我按下「取消」
    Then 對話關閉，沒有呼叫 ConfirmWorkspace，AI 工作仍是停用的

  Scenario: 重啟後回到未確認（R4）
    Given 我已確認過 workspace
    When 我重新開啟 app
    Then 依 R1 重新判定，仍是高風險時再次要求確認，不從稽核、registry 或 resume 恢復

  Scenario: WORKBENCH_WORKSPACE 有效的專案目錄不需確認（R1）
    Given WORKBENCH_WORKSPACE 指向家目錄底下的專案目錄，且家目錄身分可判定
    Then 不顯示警示，行為與沒有 B4 之前相同

  Scenario: WORKBENCH_WORKSPACE 無效而靜默退回其他候選，仍要確認（R1 (b)）
    Given WORKBENCH_WORKSPACE 指向不存在的路徑，app 退回目前工作目錄
    Then 警示顯示退回的原因（路徑無法解析或無法建立 .workbench）

  Scenario Outline: 實際目錄就是家目錄，不論來源，都要確認（R1 (c)）
    Given workspace 的來源是 <來源>，且實際目錄與使用者家目錄是同一個目錄
    Then 警示出現，原因包含「實際目錄就是使用者家目錄」

    Examples:
      | 來源                                       |
      | WORKBENCH_WORKSPACE 設成家目錄             |
      | 在終端機 cd ~ 後啟動（來源 cwd）            |
      | WORKBENCH_WORKSPACE 指向家目錄的 symlink    |

  Scenario: 退回系統暫存目錄要確認（R1 (a)）
    Given 所有候選目錄都無法使用，app 退回系統暫存目錄
    Then 警示出現，原因包含「退回系統暫存目錄」

  Scenario: 家目錄身分無法判定時要確認（R1 (d)）
    Given 無法取得使用者家目錄，或無法對家目錄或 workspace 取得檔案資訊
    Then 即使 WORKBENCH_WORKSPACE 本身正常也要確認，警示顯示可辨認的原因

  Scenario: 前端顯示的目錄與後端快照不一致時，確認被拒（R3）
    Given 畫面上的目錄與後端實際的 workspace 字串不完全相同
    When 呼叫 ConfirmWorkspace
    Then 回傳錯誤，狀態維持原樣，不嘗試寫成功稽核

  Scenario: 查詢失敗或尚未取得狀態時，前端不放行（R5）
    Given WorkspaceConfirmationStatus 尚未回覆、查詢失敗，或回覆形狀不對
    Then 送出訊息與 Spec／Plan 的 AI 輔助都是停用的
    And 晚到的舊回應不會覆蓋較新的狀態
