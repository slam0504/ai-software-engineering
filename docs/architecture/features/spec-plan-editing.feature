Feature: Spec／Plan 手動編輯閉環（A1a）

  # 對應 backlog A1a 驗收條件 (1)(2)(3)(4)(6)；外部檔案變更的 reload／compare 屬 A1b，不在本檔。
  # 「真實輸入」＝對編輯器本身輸入，不是把內容注入 props 或直接呼叫 store action。

  Background:
    Given 工作區已選定一個納管的 spec 或 plan 檔
    And 該檔已載入編輯器，且畫面內容與磁碟內容相同

  Scenario: 真實輸入回寫受控 buffer
    When 我在編輯器中直接鍵入文字
    Then 受控 buffer 的內容與編輯器目前內容相同
    And 該檔顯示為未儲存狀態

  Scenario: Plan 真實輸入後儲存與重載
    Given 我在 plan 編輯器中直接鍵入文字
    When 我按下儲存
    Then 送出的內容是編輯器目前內容，不是上次載入或上次套用草稿的內容
    And 儲存成功後未儲存狀態被清除，且持有的 digest 更新為寫入後的新值
    When 我重新載入該檔
    Then 載入的內容與我鍵入後儲存的內容相同

  Scenario: Spec 真實輸入後儲存與重載
    Given spec 工作區提供「儲存目前內容」的動作
    And 我在 spec 編輯器中直接鍵入文字
    When 我按下儲存
    Then 送出的內容是編輯器目前內容，不是 AI 草稿萃取結果
    And 儲存成功後未儲存狀態被清除，且持有的 digest 更新為寫入後的新值
    When 我重新載入該檔
    Then 載入的內容與我鍵入後儲存的內容相同

  Scenario: 內容改回原樣不應誤報未儲存
    Given 我在編輯器中鍵入文字，該檔顯示為未儲存
    When 我把內容改回與載入時完全相同
    Then 該檔不再顯示為未儲存

  Scenario: 儲存失敗——digest 衝突
    Given 我在編輯器中直接鍵入文字
    And 該檔在磁碟上已被其他來源改動，我持有的 digest 已過期
    When 我按下儲存
    Then 儲存被拒絕，並以 digest 衝突呈現，而不是泛用錯誤
    And 編輯器內容與受控 buffer 不被覆蓋，未儲存狀態維持
    And 我持有的 digest 不被更新

  Scenario: 儲存失敗——其他錯誤原樣揭露
    Given 我在編輯器中直接鍵入文字
    And 後端寫入因非衝突原因失敗
    When 我按下儲存
    Then 錯誤訊息原樣呈現，不被吞掉也不被誤判為衝突
    And 未儲存狀態維持，digest 不被更新

  Scenario: 切換檔案時保護未儲存內容
    Given 我在編輯器中直接鍵入文字，該檔顯示為未儲存
    When 我切換到另一個檔案
    Then 系統先要求我選擇保留或捨棄未儲存內容，不會靜默覆蓋
    When 我選擇捨棄
    Then 新檔案載入，原未儲存內容不再保留
    When 我改為選擇保留
    Then 停留在原檔案，未儲存內容仍在編輯器中

  Scenario: 切換分頁時保護未儲存內容
    Given 我在 plan 編輯器中直接鍵入文字，該檔顯示為未儲存
    When 我切換到其他分頁再切回來
    Then 未儲存內容沒有被磁碟內容靜默覆蓋
