Feature: Spec／Plan 手動編輯閉環（A1a）

  # 對應 backlog A1a 驗收條件 (1)(2)(3)(4)(6)；外部檔案變更的 reload／compare 屬 A1b，不在本檔。
  # 「真實輸入」＝使編輯器文件本身改變，不是把內容注入 props 或直接呼叫 store action。
  # 「已儲存快照」＝最近一次成功寫入所送出的內容（不是回應到達當下的編輯器內容）。

  Background:
    Given 工作區已選定一個納管的 spec 或 plan 檔
    And 該檔已載入編輯器，且編輯器內容、受控 buffer 與已儲存快照三者相同

  Scenario: 真實輸入回寫受控 buffer
    When 我使編輯器文件改變
    Then 受控 buffer 的內容與編輯器目前內容相同
    And 該檔顯示為未儲存狀態

  Scenario: Plan 真實輸入後儲存並重新載入
    Given 我使 plan 編輯器文件改變
    When 我按下儲存
    Then 送出的內容是編輯器目前內容，不是上次載入或上次套用草稿的內容
    And 儲存成功後未儲存狀態被清除，且持有的 digest 更新為寫入後的新值
    When 我重新載入該檔
    Then 編輯器文件的內容與我先前儲存的內容相同

  Scenario: Spec 真實輸入後儲存並重新載入
    Given spec 工作區提供「儲存目前內容」的動作
    And 我使 spec 編輯器文件改變
    When 我按下儲存
    Then 送出的內容是編輯器目前內容，不是 AI 草稿萃取結果
    And 儲存成功後未儲存狀態被清除，且持有的 digest 更新為寫入後的新值
    When 我重新載入該檔
    Then 編輯器文件的內容與我先前儲存的內容相同

  Scenario: 內容改回原樣不應誤報未儲存
    Given 我使編輯器文件改變，該檔顯示為未儲存
    When 我把內容改回與已儲存快照完全相同
    Then 該檔不再顯示為未儲存

  Scenario: 儲存進行中繼續輸入——只有送出的內容算已儲存
    Given 我把內容編輯為 A 並按下儲存，該次寫入尚未回應
    When 我在等待期間把內容繼續編輯為 B
    And 該次寫入成功回應
    Then 已儲存快照為 A，不是 B
    And 該檔仍顯示為未儲存
    And 持有的 digest 為該次寫入回傳的新值

  Scenario: 儲存進行中不得重疊寫入或替換內容
    Given 我按下儲存，該次寫入尚未回應
    Then 儲存動作在該次寫入回應前不可再次觸發
    And 切換檔案、切換分頁與其他會替換編輯器內容的操作在該次寫入回應前被暫停
    When 該次寫入回應
    Then 上述操作恢復可用

  Scenario: 延遲的載入回應不得覆蓋後來選取的檔案
    Given 我選取檔案一，其載入回應被延遲
    When 我在回應到達前改為選取檔案二，且檔案二已完成載入
    And 檔案一的載入回應才到達
    Then 編輯器內容、受控 buffer、已儲存快照與持有的 digest 仍屬於檔案二

  Scenario: 儲存失敗——digest 衝突
    Given 我使編輯器文件改變
    And 該檔在磁碟上已被其他來源改動，我持有的 digest 已過期
    When 我按下儲存
    Then 儲存被拒絕，並依 sentinel 文字契約辨識為 digest 衝突，而不是泛用錯誤
    And 編輯器內容與受控 buffer 不被覆蓋，未儲存狀態維持
    And 持有的 digest 與已儲存快照都不被更新
    And 系統不會自動重新載入該檔

  Scenario: 儲存失敗——非衝突錯誤保留原訊息
    Given 我使編輯器文件改變
    And 後端寫入因非衝突原因失敗
    When 我按下儲存
    Then 錯誤訊息原樣呈現，不被吞掉也不被判為衝突
    And 未儲存狀態維持，持有的 digest 與已儲存快照都不被更新

  Scenario: 切換檔案時選擇捨棄未儲存內容
    Given 我使編輯器文件改變，該檔顯示為未儲存
    When 我切換到另一個檔案
    Then 系統要求我選擇保留或捨棄，不會靜默覆蓋
    When 我選擇捨棄
    Then 新檔案載入，原未儲存內容不再保留

  Scenario: 切換檔案時選擇保留未儲存內容
    Given 我使編輯器文件改變，該檔顯示為未儲存
    When 我切換到另一個檔案
    Then 系統要求我選擇保留或捨棄，不會靜默覆蓋
    When 我選擇保留
    Then 仍停留在原檔案，未儲存內容仍在編輯器中
    And 新檔案沒有被載入

  Scenario Outline: 切換分頁時保護未儲存內容——<workspace>／<entry>
    Given 我在 <workspace> 編輯器中使文件改變，該檔顯示為未儲存
    When 我以 <entry> 觸發離開目前工作區
    Then 系統要求我選擇保留或捨棄，不會靜默切換
    And 在我做出選擇前，工作區元件沒有被切走或卸載
    When 我選擇保留
    Then 仍停留在原工作區，未儲存內容仍在編輯器中

    Examples:
      | workspace | entry          |
      | spec      | 分頁按鈕       |
      | spec      | 檔案樹選取     |
      | spec      | 重新送核導向   |
      | plan      | 分頁按鈕       |
      | plan      | 檔案樹選取     |
      | plan      | 重新送核導向   |

  Scenario: 套用草稿與 bump 後三者同步
    Given 我使編輯器文件改變，該檔顯示為未儲存
    When 我套用 AI 草稿或確認 analysis_base bump
    Then 編輯器內容與受控 buffer 更新為該操作的結果
    And 該檔顯示為未儲存，直到我按下儲存為止
    And 持有的 digest 與已儲存快照只在寫入成功後才更新
