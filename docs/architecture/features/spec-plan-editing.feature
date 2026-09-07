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
    Then 該回應因請求世代已過期而被整筆丟棄
    And 編輯器內容、受控 buffer、已儲存快照與持有的 digest 仍屬於檔案二

  Scenario: 同一檔案的舊世代回應不得覆蓋最新一次載入
    Given 我選取檔案一，其載入回應被延遲
    And 我改為選取檔案二，再改回選取檔案一，最後這次載入已完成
    When 第一次選取檔案一的延遲回應才到達
    Then 該回應因請求世代已過期而被整筆丟棄，即使它的路徑與目前檔案相同
    And 編輯器內容為最後一次載入的結果

  Scenario: 載入進行中不得讓回應覆蓋新輸入
    Given 我選取一個檔案，其載入尚未回應
    Then 編輯在該次載入回應前為暫停狀態
    When 該次載入回應且屬於最新世代
    Then 編輯恢復，且編輯器內容為該次載入的結果

  Scenario: 儲存失敗——digest 衝突
    Given 我使編輯器文件改變
    And 該檔在磁碟上已被其他來源改動，我持有的 digest 已過期
    When 我按下儲存
    Then 儲存被拒絕，並依 sentinel 文字契約辨識為 digest 衝突，而不是泛用錯誤
    And 編輯器內容與受控 buffer 不被覆蓋，未儲存狀態依內容比較決定
    And 持有的 digest 與已儲存快照都不被更新
    And 系統不會自動重新載入該檔

  Scenario: 儲存失敗——非衝突錯誤保留原訊息
    Given 我使編輯器文件改變
    And 後端寫入因非衝突原因失敗
    When 我按下儲存
    Then 錯誤訊息原樣呈現，不被吞掉也不被判為衝突
    And 持有的 digest 與已儲存快照都不被更新，未儲存狀態依內容比較決定

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

  Scenario Outline: 選擇捨棄後導覽到正確目標——<workspace>／<entry>
    Given 我在 <workspace> 編輯器中使文件改變，該檔顯示為未儲存
    When 我以 <entry> 觸發離開目前工作區
    And 我選擇捨棄
    Then 導覽完成並到達該入口指定的目標
    And 原未儲存內容不再保留

    Examples:
      | workspace | entry          |
      | spec      | 分頁按鈕       |
      | spec      | 檔案樹選取     |
      | spec      | 重新送核導向   |
      | plan      | 分頁按鈕       |
      | plan      | 檔案樹選取     |
      | plan      | 重新送核導向   |

  Scenario: Plan 套用草稿或確認 bump——只更新 buffer，不寫檔
    Given 我在 plan 編輯器中使文件改變
    When 我套用 AI 草稿或確認 analysis_base bump
    Then 編輯器內容與受控 buffer 更新為該操作的結果
    And 該次操作不寫入磁碟，已儲存快照與持有的 digest 都不變
    And 未儲存狀態依內容比較決定——結果與已儲存快照不同才是未儲存

  Scenario: Plan 操作結果恰等於已儲存快照時不算未儲存
    Given 我在 plan 編輯器中使文件改變，該檔顯示為未儲存
    When 我套用的草稿內容恰好與已儲存快照完全相同
    Then 該檔不顯示為未儲存

  Scenario: Spec 接受草稿——立即寫入並更新快照
    Given spec 工作區有一份 AI 草稿，且草稿在我按下接受前不會被寫入磁碟
    When 我按下接受草稿
    Then 受控 buffer 先被替換為草稿萃取結果，該內容即為此次寫入的送出快照
    And 該次寫入納入寫入互斥，等待期間不得再次儲存或接受、不得切檔或切分頁
    And 寫入成功後已儲存快照更新為送出的草稿內容，持有的 digest 更新為新值

  Scenario: Spec 接受草稿等待期間的新輸入不被丟棄
    Given 我按下接受草稿，該次寫入尚未回應
    When 我在等待期間繼續編輯內容
    And 該次寫入成功回應
    Then 受控 buffer 仍是我等待期間編輯後的內容
    And 已儲存快照為送出的草稿內容，因此該檔仍顯示為未儲存

  Scenario: Spec 接受草稿失敗不更新快照
    Given 我按下接受草稿
    When 該次寫入失敗
    Then 已儲存快照與持有的 digest 都不變
    And 受控 buffer 保留接受草稿後的內容，錯誤依其種類呈現

  Scenario: bump 確認等待期間的操作限制
    Given 我在 plan 確認 analysis_base bump，該次回應尚未到達
    And 該次確認送出時已凍結當時的文件識別與 buffer 版本
    Then 我仍可以繼續打字
    And 切換檔案、切換分頁與其他會替換編輯器內容的操作被阻止
    When 我沒有繼續打字，且該次確認的回應到達時版本仍相符
    Then 編輯器內容與受控 buffer 更新為 bump 結果
    And 操作封鎖解除

  Scenario: bump 等待期間續打使版本過期——回應不套用
    Given 我在 plan 確認 analysis_base bump，該次回應尚未到達
    When 我在等待期間繼續打字
    And 該次確認的回應才到達
    Then 回應不被套用，我續打的內容原樣保留
    And 顯示「內容已變更，請重新預覽」這類明確訊息，而不是後端錯誤原文
    And 操作封鎖解除

  Scenario: bump 等待期間文件識別被替換——防禦性驗證
    Given 我在 plan 確認 analysis_base bump
    And 正常導覽在等待期間已被阻止，因此以測試注入替換該次確認的文件識別
    When 該次確認的回應到達
    Then 回應不被套用，編輯器內容不被覆蓋
    And 顯示「內容已變更，請重新預覽」這類明確訊息
    And 操作封鎖解除

  Scenario: bump 真正的後端錯誤仍保留原訊息
    Given 我在 plan 確認 analysis_base bump
    When 後端回傳錯誤（例如 token 過期）
    Then 錯誤訊息原樣呈現，不被換成版本已變更的訊息
    And 系統要求重新預覽
    And 操作封鎖解除
