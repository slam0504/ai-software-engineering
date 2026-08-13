Feature: 計畫工作區與 Gate 2 核可（Stage B 閉環）

  Scenario: Stage B 閉環——草稿到 Gate 2 核可
    Given Gate 1 已核可且 spec manifest 生效
    And PlannerAssist 已產出計畫草稿並經人編修為完整 plan（含 oracle-surface 宣告與每個 task 的 test contract descriptor）
    And 確定性驗證通過
    When 我 Preview／Confirm plan commit，產生 plan_commit
    And 我以 subject "plan:<plan_id>"、base_commit=plan_commit 送核 Gate 2
    Then 送核成功並進入 pending
    When 核可者針對每個 task 選定 selected_risk_tier 並核可
    Then Gate 2 核可記錄變為 active，metadata 含依 task_id 排序的完整 risk_decisions

  Scenario: lineage 拒絕——analysis_base_commit..plan_commit 混入非 plan 變更
    Given plan 檔宣告的 analysis_base_commit 是 plan_commit 的祖先
    But analysis_base_commit..plan_commit 範圍內有一筆變更觸及 plan/** 以外的路徑
    When 我以該 plan_commit 送核 Gate 2
    Then 送核被拒絕，錯誤指出該路徑不在允許範圍內

  Scenario: selected_risk_tier 低於 planner 建議需理由
    Given plan 中某 task 的 minimum_risk_tier 為 medium、planner_risk_tier 為 high
    When 核可者選 selected_risk_tier=medium 且未填 override_reason
    Then 核可被拒絕，錯誤指出該 task 需要 override_reason
    When 核可者補上 override_reason 再次核可
    Then 核可成功，該 task 的 risk_decisions 記錄該 override_reason

  Scenario: selected_risk_tier 低於 minimum 一律拒絕
    Given plan 中某 task 的 minimum_risk_tier 為 medium
    When 核可者選 selected_risk_tier=low
    Then 核可被拒絕，錯誤指出低於 minimum_risk_tier，即使已填 override_reason

  Scenario: spec 變更觸發 Gate 2 STALE
    Given plan:<plan_id> 的 Gate 2 已核可為 active
    When active Gate 1 綁定的 spec manifest 內容在核可後變更
    Then 下次 reconcile 時該 Gate 2 核可轉為 stale
    And 尚無修正版核可時系統於收件匣建立 hard、block_scope=gate2:<plan_id> 的項目
