Feature: Test Contract Approval（TCA，Stage C 前段）

  Scenario: expected-red 與 negative-control 兩型 evidence
    Given 已核可宣告的 oracle-surface 與每個 task 的 test contract descriptor
    And test_commit 以 plan_commit 為祖先，且 plan_commit..test_commit 只修改 oracle-surface 路徑
    When runner 對 test_commit 執行 expected_red
    Then 該次執行的失敗特徵與 test contract descriptor 相符，result=passed
    When 我登記一筆 mutation 並在同一 test_commit 上執行 negative_control
    Then 該次執行同樣以已核可 descriptor 判定，result=passed 且記錄 mutation_digest
    But expected_red 執行不得攜帶 mutation_digest

  Scenario: 兩筆 evidence snapshot 不一致拒核
    Given 一筆 expected_red 與一筆 negative_control 的 evidence run 皆為 passed
    But 兩筆的 base_commit／test_commit／oracle_surface_digest 之中有一項不一致
    When 我以此組 evidence 送核 TCA
    Then 核可被拒絕，錯誤指出 snapshot 不一致

  Scenario: descriptor 不符拒核
    Given 兩筆 evidence run 彼此一致且 snapshot 相符
    But 其中一筆的 command 或 expected_failure 與已核可 Gate 2 plan 中該 task 的 test contract descriptor 不同
    When 我送核 TCA
    Then 核可被拒絕，錯誤指出 descriptor 不符

  Scenario: evidence base_commit 不等於 gate2_approval 的 plan_commit 拒核
    Given 一筆 expected_red 與一筆 negative_control 的 evidence run 彼此一致
    But 其 base_commit 不等於這筆 TCA 所綁 gate2_approval 記錄的 plan_commit
    When 我送核 TCA
    Then 核可被拒絕，錯誤指出 base_commit 與 gate2_approval 的 plan_commit 不符

  Scenario: Gate 2 STALE 連動 TCA STALE
    Given task:<plan_id>/<task_id> 的 TCA 已核可為 active，其綁定的 gate2_approval 目前仍 active
    When 該 gate2_approval 因綁定變更轉為 stale 或被修正版核可 supersede
    Then 下次 reconcile 時對應的 TCA 核可也轉為 stale

  Scenario: HEAD 前移不觸發 STALE
    Given Gate 2 已核可，base_commit 綁定為 plan_commit
    When Stage C 在 plan_commit 之後建立新的 test commit（HEAD 前移）
    Then Gate 2 核可不因 HEAD 前移而 STALE（base_commit 只驗 commit 是否仍存在於 repo）
