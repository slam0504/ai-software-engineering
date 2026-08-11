Feature: 雙 provider session 並存與切換

  Scenario: 切換 view 不中斷進行中的 session（E1/E3）
    Given claude session 正在 streaming 回覆
    When 我切換到 codex tab 再切回 claude tab
    Then claude 對話完整呈現且包含切換期間收到的內容
    And 切換過程沒有任何 provider 呼叫發生

  Scenario: 一個 provider 進行中，另一個仍可送訊息（E2）
    Given claude session 正在 streaming 回覆
    When 我在 codex view 送出訊息
    Then codex 開始自己的 session 並完成回覆
    And claude 的回覆不受影響地完成

  Scenario: 背景 view 的未讀提示（E4）
    Given 我在 claude view 而 codex session 在背景收到 result
    Then codex tab 顯示未讀計數
    When 我切換到 codex tab
    Then 未讀計數歸零

  Scenario: 背景 provider 的核可請求彈出並自動切 tab（E5）
    Given 我在 claude view
    When codex 觸發工具核可請求
    Then ApprovalDialog 立即彈出且畫面自動切到 codex tab
    When 我核可該請求
    Then codex 繼續執行且兩個 view 的狀態各自正確

  Scenario: Esc 等同拒絕（E5b）
    Given ApprovalDialog 開啟中
    When 我按下 Esc
    Then 該核可請求被拒絕且理由記為 esc
    And 稽核流記下 approval_decision 為 deny

  Scenario: New 只作用於目前 view 的 provider（E6）
    Given claude 與 codex 各有 active session
    When 我在 claude view 按 New
    Then claude session 收尾且 claude view 重置
    And codex session 與 view 完全不受影響

  Scenario: 稽核流無交叉汙染（E7）
    Given 兩個 provider 各自完成至少一輪且任務標籤不同
    Then events.jsonl 中每筆 envelope 的 provider、session_id、task_id 與其來源一致
    And event_id 全檔案嚴格遞增

  Scenario: 重啟後預設恢復（E9）
    Given 前次執行時兩個 view 各有對話且未按 New
    When 我重新開啟 app
    Then 兩個 view 的對話、totals 與任務標籤自動還原
    When 我在 claude view 送出訊息
    Then claude 以前次 session id resume 並能引用前次對話內容
    When 我在 codex view 送出訊息
    Then codex 以 thread/resume 接回前次 thread 並能引用前次對話內容

  Scenario: New 之後不恢復（E10）
    Given 前次執行時我在 claude view 按過 New 後關閉 app
    When 我重新開啟 app
    Then claude view 為空白
    And codex view 仍照前次內容恢復
