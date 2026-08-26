# 貢獻指南

感謝你願意花時間改善這個專案！

## 從哪裡開始

想找題目的話，[專案路線圖](docs/01_plan/project-roadmap.md)的 **Phase 1** 列了幾個範圍小、不牽動架構的任務，適合第一次貢獻。

## 開發環境

需要 Go 1.24 以上（版本以 `go.mod` 為準）。

```bash
git clone https://github.com/kkdai/linebot-file.git
cd linebot-file
go mod download
go test ./...
```

### 在本機執行

本專案需要 LINE 與 Google 的憑證才能實際運作。取得方式見 [README 的部署章節](README.md#-部署到-google-cloud-platform)。

```bash
export GOOGLE_CLOUD_PROJECT=your-gcp-project
export ChannelSecret=...
export ChannelAccessToken=...
export GOOGLE_CLIENT_ID=...
export GOOGLE_CLIENT_SECRET=...
export GOOGLE_REDIRECT_URL=https://your-tunnel/oauth/callback

go run .
```

LINE 的 webhook 必須是公開的 HTTPS 網址，本機開發建議搭配 [ngrok](https://ngrok.com/) 之類的通道工具。

## 程式碼結構

| 檔案 | 職責 |
|------|------|
| `main.go` | 啟動流程、環境變數檢查、HTTP 路由 |
| `config.go` | 常數與套件層級的共用狀態 |
| `webhook.go` | LINE 事件分派、指令解析、各指令的處理邏輯 |
| `line.go` | LINE 訊息組裝（文字、Flex、Quick Reply、Rich Menu） |
| `drive.go` | Google Drive 的查詢、上傳、資料夾管理 |
| `auth.go` | OAuth 流程、token 儲存與撤銷 |

## 送出變更前

以下三項是 CI 會檢查的項目，請先在本機跑過：

```bash
gofmt -l .        # 應該沒有任何輸出
go vet ./...
go test -race ./...
```

### 撰寫規範

- **每個新功能都要有對應測試。** 尤其是指令解析、Drive query 組裝這類純函式，寫測試的成本很低。
- **使用者輸入進入 Drive query 前一定要經過 `escapeDriveQuery`。** 未跳脫的 `'` 會讓查詢失效，也是注入風險。
- **不要用未檢查的型別斷言取得使用者 ID。** 請用 `userIDFromSource`，它會正確處理群組與聊天室來源。
- **讀取路徑不要建立資料夾。** 查詢用 `findFolder`，只有上傳流程才該用 `findOrCreateFolder`。
- **所有外部呼叫都要帶 `context`。**
- 註解說明「為什麼」，而不是重述程式碼在做什麼。

## PR 流程

1. Fork 專案。
2. 開一個功能分支：`git checkout -b feature/AmazingFeature`。
3. 提交變更：`git commit -m 'feat: add amazing feature'`。
4. 推送分支：`git push origin feature/AmazingFeature`。
5. 開啟 Pull Request，說明改了什麼、為什麼這樣改。
6. 如果變更影響到路線圖上的項目，請一併更新 [project-roadmap.md](docs/01_plan/project-roadmap.md) 的狀態。

### Commit 訊息

建議使用 [Conventional Commits](https://www.conventionalcommits.org/)：

- `feat:` 新功能
- `fix:` 錯誤修正
- `docs:` 文件
- `refactor:` 重構（不改變外部行為）
- `test:` 測試
- `chore:` 建置流程、相依套件等雜項

## 回報問題

開 Issue 時，請盡量包含：

- 你做了什麼（哪個指令、什麼類型的檔案）
- 預期的結果與實際的結果
- 相關的日誌（**請務必移除 access token、Client Secret 等機密資料**）
