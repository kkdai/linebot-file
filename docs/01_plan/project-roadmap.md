# 專案路線圖 (Project Roadmap)

> 這份文件說明專案目前的狀態、以及接下來想做的事。
> 想貢獻但不知道從哪裡開始的話，請直接看 [Phase 1](#phase-1--短期歡迎認領)。

**最後更新**：2026-08-26

---

## 目前狀態

| 項目 | 狀態 |
|------|------|
| 核心備份功能（圖片／影片／音訊／檔案 → Google Drive） | ✅ Completed |
| Google OAuth 2.0 連線與撤銷 | ✅ Completed |
| 檔案查詢（`/recent_files`）與搜尋（`/search_files`、`/q`） | ✅ Completed |
| Rich Menu 連線狀態切換 | ✅ Completed |
| 程式碼健檢與重構（2026-08） | ✅ Completed |
| 非同步上傳架構 | ⬜ Not Started |
| 群組聊天支援 | ⬜ Not Started |

專案目前處於**穩定維護**階段，功能可用，歡迎社群提出新想法。

---

## 已完成：2026-08 程式碼健檢

一次針對全專案的檢查與重構，主要成果：

**修正的錯誤**

- `FollowEvent` 等事件原本寫在錯誤的 `switch` 層級，導致新使用者加好友時 Rich Menu 沒有被綁定。
- 群組／多人聊天室傳送訊息會使程式 panic（未檢查的型別斷言）。
- `/recent_files` 只查詢根資料夾，實際回傳的是月份資料夾本身而非檔案。
- 搜尋關鍵字未經跳脫就組進 Drive query，含 `'` 的關鍵字會讓查詢失效，也存在注入風險。
- `/q` 前綴比對過寬，`/quit` 會被當成搜尋指令。
- 搜尋結果宣稱依時間排序，實際是 map 的隨機順序。
- 一次 webhook 只處理批次中的第一個事件，其餘被丟棄。
- OAuth state 永不過期。

**效能與架構**

- 搜尋從 `1 + N` 次 Drive API 呼叫（N = 月份資料夾數）降為固定 2 次。
- 更新後的 access token 會寫回 Firestore，不再每次請求都重新 refresh。
- 全流程改用 `context`，所有外部呼叫都有逾時。
- 以 webhook event ID 去重，避免 LINE 重送造成重複上傳。
- `main.go`（1039 行）拆分為 `config.go` / `main.go` / `webhook.go` / `line.go` / `drive.go` / `auth.go`。

---

## Phase 1 — 短期（歡迎認領）

適合第一次貢獻的題目，範圍小、不牽動架構。

| 任務 | 說明 | 預估 | 狀態 |
|------|------|------|------|
| `/help` 指令 | 回覆所有可用指令與用法，目前使用者只能靠 README 得知 | 1h | ⬜ Not Started |
| 搜尋結果分頁 | 目前硬性上限 10 筆（`maxSearchResults`），超過就被截斷且無提示 | 3h | ⬜ Not Started |
| 依類型篩選 | 例如 `/q 報告 type:pdf`，對應 Drive query 的 `mimeType` 條件 | 3h | ⬜ Not Started |
| 可設定的根資料夾名稱 | `rootFolderName` 目前寫死為 `LINE Bot Uploads` | 1h | ⬜ Not Started |
| 訊息文案集中管理 | 回覆訊息中英夾雜且散落各處，抽成單一訊息表以便日後 i18n | 2h | ⬜ Not Started |
| 上傳大小上限 | 超過上限時明確告知使用者，而不是等到失敗 | 2h | ⬜ Not Started |

## Phase 2 — 架構強化

| 任務 | 說明 | 狀態 |
|------|------|------|
| 非同步上傳 | 大檔案目前在 webhook 請求內同步處理。改用 Cloud Tasks 或 Pub/Sub 後可立即回應 LINE。**注意**：不能單純開 goroutine，Cloud Run 預設只在請求期間配置 CPU | ⬜ Not Started |
| 跨執行個體的事件去重 | 目前 `handledEvents` 是行程內的記憶體快取，多個 Cloud Run 執行個體無法共用。改存 Firestore 並設 TTL | ⬜ Not Started |
| `oauth_states` 自動清理 | 已在讀取時檢查 `stateTTL`，但過期文件仍留在 Firestore。需設定 Firestore TTL policy | ⬜ Not Started |
| 結構化日誌 | 改用 `log/slog`，並對應 Cloud Logging 的 severity 欄位 | ⬜ Not Started |
| Rich Menu 建立腳本 | 取代 README 中一長串手動 `curl` 步驟 | ⬜ Not Started |
| Drive API 分頁 | 月份子資料夾的列舉已改用 `Pages()` 完整分頁。剩下搜尋／查詢結果本身仍只取第一頁，但那是刻意的顯示上限，見 Phase 1 的「搜尋結果分頁」 | ✅ Completed |

## Phase 3 — 功能擴充

| 任務 | 說明 | 狀態 |
|------|------|------|
| 群組聊天支援 | 指令目前僅限一對一聊天。群組需要先決定「檔案要存進誰的 Drive」 | ⬜ Not Started |
| 其他雲端硬碟 | Dropbox、OneDrive，需要先把 Drive 相關程式抽象成介面 | ⬜ Not Started |
| LIFF 設定頁 | 用網頁介面管理連線狀態與偏好設定，取代部分指令 | ⬜ Not Started |
| 用量統計 | 每月備份數量、容量統計 | ⬜ Not Started |

---

## 已知限制

- **測試覆蓋**：純函式與 Drive 層有測試，webhook handler 缺少端對端測試（需要能注入 LINE client 的介面）。
- **單一 Drive 帳號**：一個 LINE 使用者只能連結一個 Google 帳號。
- **無流量限制**：沒有 per-user rate limiting，被濫用時只能靠 Cloud Run 的擴展上限擋。
- **健康檢查端點不能叫 `/healthz`**：Cloud Run 的前端保留了這個精確路徑，會用自己的 404 攔截，請求不會進到容器。目前用的是 `/health`。
- **Rich Menu 需手動建立**：ID 目前由 `RICH_MENU_CONNECT` / `RICH_MENU_MAIN` 環境變數提供，選單本身仍需手動以 API 建立。

---

## 如何認領任務

1. 到 [Issues](https://github.com/kkdai/linebot-file/issues) 看看是否已有對應討論；沒有的話請先開一個 Issue 說明你想做的內容。
2. 留言表示你要接手，避免重工。
3. 依 [CONTRIBUTING.md](../../CONTRIBUTING.md) 的流程開發並送出 PR。
4. PR 合併後，請一併更新這份文件的狀態欄位。
