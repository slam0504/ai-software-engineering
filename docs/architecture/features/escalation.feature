Feature: 升級收件匣（fail-closed 閉環）

  Scenario: evidence run 錯誤產生 fail-closed 升級項目
    Given task 的 test contract command 因環境因素（如編譯失敗）無法產生預期的紅燈特徵
    When evidence runner 執行 expected_red
    Then 該次執行判定為 result=error 並在收件匣建立 block_scope=tca:<plan_id>/<task_id>、hard=false 的項目

  Scenario: 修復後系統重驗自動解除
    Given 收件匣中有一筆 evidence-error 項目尚未 resolve
    When 我修復測試腳本並以新 test_commit 重跑同一 kind 的 evidence run
    And 該次重跑 result=passed
    Then 系統以同一 condition_key 自動 resolve 該收件匣項目，不需人工操作

  Scenario: hard 項目使用者不可手動 resolve
    Given Gate 1 核可後 spec 變更，reconcile 建立一筆 hard=true 的 stale 收件匣項目
    When 使用者嘗試以 accepted_risk 手動 resolve 該項目
    Then 操作被拒絕
    But 使用者仍可 acknowledge 該項目（acknowledge 不解除阻擋）

  Scenario: blocking 項目擋下核可
    Given plan:<plan_id> 有一筆 block_scope=gate2:<plan_id> 的未 resolved 項目
    When 核可者嘗試核可該 plan 的 Gate 2 請求
    Then GateDecide 被拒絕，錯誤列出擋下的收件匣項目
    And 該筆 Gate 2 請求維持 pending

  Scenario: 修正版核可解除 stale blocker 後核可通過
    Given Gate 2 因 plan 變更 stale，收件匣有一筆對應的 hard blocker
    When 我送出修正版 plan 並取得核可者核可
    Then GateDecide 的 2b 步驟以同一 condition_key 系統解除該 blocker
    And 核可成功、記錄變為 active
