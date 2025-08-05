# LINE Bot 檔案備份機器人

這是一個功能強大的 LINE Bot，可以讓使用者輕鬆地將聊天室中的圖片、影片、音訊和檔案備份到自己的 Google Drive。它會自動整理檔案，並提供方便的查詢功能。

[![Go Report Card](https://goreportcard.com/badge/github.com/kkdai/linebot-file)](https://goreportcard.com/report/github.com/kkdai/linebot-file)
[![MIT License](https://img.shields.io/badge/license-Apache2-blue.svg)](https://www.apache.org/licenses/LICENSE-2.0)

<img width="1024" height="1024" alt="image" src="https://github.com/user-attachments/assets/fd567ff2-0589-483e-ae7f-986921c3d303" />

## ✨ 主要功能

*   **多媒體檔案備份**：支援備份圖片、影片、音訊和一般檔案。
*   **智慧資料夾整理**：自動在您的 Google Drive 建立 `LINE Bot Uploads` 資料夾，並以年月 (`YYYY-MM`) 為單位建立子資料夾存放檔案，保持雲端硬碟整潔。
*   **安全帳號連結**：使用 Google OAuth 2.0 進行授權，安全可靠。
*   **查詢最近檔案**：透過 `/recent_files` 指令，快速查看最近上傳的 5 個檔案。
*   **完整的連線控制**：使用者可以隨時透過 `/disconnect_drive` 指令中斷連線並撤銷授權。

## 🚀 部署到 Google Cloud Platform

本專案已容器化 (Dockerfile)，強烈建議使用 [Google Cloud Run](https://cloud.google.com/run) 進行部署，它能提供全代管、自動擴展的無伺服器環境。

### 前置作業

1.  擁有一個 Google Cloud 帳號。
2.  安裝並設定好 [Google Cloud SDK (gcloud CLI)](https://cloud.google.com/sdk/docs/install)。
3.  一個 LINE Bot 頻道，並取得 **Channel Secret** 和 **Channel Access Token**。

### 部署步驟

1.  **啟用必要的 API**

    在您的 Google Cloud 專案中，啟用以下服務：
    *   **Cloud Run API**
    *   **Cloud Build API** (用於自動建置容器映像檔)
    *   **Firestore API** (用於儲存使用者授權資料)

    您可以使用 gcloud CLI 快速啟用：
    ```bash
    gcloud services enable run.googleapis.com cloudbuild.googleapis.com firestore.googleapis.com
    ```

2.  **建立 Firestore 資料庫**

    *   前往 Google Cloud Console 的 Firestore 頁面。
    *   選擇「原生模式 (Native mode)」。
    *   選擇離您使用者最近的地區，然後建立資料庫。

3.  **取得 Google OAuth 憑證**

    這是讓您的機器人能代表使用者存取 Google Drive 的關鍵。
    *   前往 [Google Cloud Console -> APIs & Services -> Credentials](https://console.cloud.google.com/apis/credentials)。
    *   點擊 **+ CREATE CREDENTIALS**，選擇 **OAuth client ID**。
    *   在 **Application type** 中選擇 **Web application**。
    *   給它一個名稱，例如 "LINE Bot File Uploader"。
    *   **此步驟先不要填寫 Authorized redirect URIs**，我們先留空，等 Cloud Run 部署完成後再回來設定。
    *   建立後，您會得到一組 **Client ID** 和 **Client Secret**。請妥善保管，稍後會用到。

4.  **設定 LINE Rich Menu (重要)**

    為了提供最佳使用者體驗，本專案使用 Rich Menu 來引導使用者操作。您需要手動建立並上傳對應的圖片。

    **a. 建立 Rich Menu 物件**

    執行以下兩個 `curl` 指令來建立選單的「骨架」。請將 `{YOUR_CHANNEL_ACCESS_TOKEN}` 替換成您自己的 Channel Access Token。

    *   **建立「尚未連線」選單:**
        ```bash
        curl -s -X POST https://api.line.me/v2/bot/richmenu \
        -H 'Authorization: Bearer {YOUR_CHANNEL_ACCESS_TOKEN}' \
        -H 'Content-Type: application/json' \
        -d '{
            "size": { "width": 2500, "height": 1686 },
            "selected": false,
            "name": "Connect Menu",
            "chatBarText": "點我開始",
            "areas": [
              {
                "bounds": { "x": 0, "y": 0, "width": 2500, "height": 1686 },
                "action": { "type": "message", "text": "/connect_drive" }
              }
           ]
        }'
        ```
        執行後會回傳一個 JSON，請**複製 `richMenuId` 的值** (例如 `richmenu-xxxxxxxx...`)。

    *   **建立「已連線」選單:**
        ```bash
        curl -s -X POST https://api.line.me/v2/bot/richmenu \
        -H 'Authorization: Bearer {YOUR_CHANNEL_ACCESS_TOKEN}' \
        -H 'Content-Type: application/json' \
        -d '{
            "size": { "width": 2500, "height": 1686 },
            "selected": false,
            "name": "Main Menu",
            "chatBarText": "功能選單",
            "areas": [
              {
                "bounds": { "x": 0, "y": 0, "width": 1250, "height": 1686 },
                "action": { "type": "message", "text": "/recent_files" }
              },
              {
                "bounds": { "x": 1250, "y": 0, "width": 1250, "height": 1686 },
                "action": { "type": "message", "text": "/disconnect_drive" }
              }
           ]
        }'
        ```
        同樣地，**複製這個 `richMenuId` 的值**。

    **b. 準備並上傳圖片**

    *   準備兩張符合 Rich Menu 設計的圖片 (JPG 或 PNG 格式)，尺寸必須為 **2500x1686** 像素，且檔案大小**小於 1MB**。
    *   執行以下指令來上傳圖片，請將 `{YOUR_CHANNEL_ACCESS_TOKEN}`、`{RICH_MENU_ID_FOR_CONNECT}`、`{PATH_TO_CONNECT_IMAGE}` 等替換為您的實際資料。

    *   **上傳「尚未連線」圖片:**
        ```bash
        curl -v -X POST https://api-data.line.me/v2/bot/richmenu/{RICH_MENU_ID_FOR_CONNECT}/content \
        -H "Authorization: Bearer {YOUR_CHANNEL_ACCESS_TOKEN}" \
        -H "Content-Type: image/png" \
        -T {PATH_TO_CONNECT_IMAGE}
        ```

    *   **上傳「已連線」圖片:**
        ```bash
        curl -v -X POST https://api-data.line.me/v2/bot/richmenu/{RICH_MENU_ID_FOR_MAIN}/content \
        -H "Authorization: Bearer {YOUR_CHANNEL_ACCESS_TOKEN}" \
        -H "Content-Type: image/png" \
        -T {PATH_TO_MAIN_MENU_IMAGE}
        ```

    **c. 更新原始碼**

    *   打開 `main.go` 檔案。
    *   找到頂部的 `const` 區塊，將您剛剛取得的兩個 `richMenuId` 填入對應的常數中：
        ```go
        const (
            // ...
            richMenuConnect = "richmenu-xxxxxxxx..." // 填入您「尚未連線」選單的 ID
            richMenuMain    = "richmenu-yyyyyyyy..." // 填入您「已連線」選單的 ID
        )
        ```

5.  **部署到 Cloud Run**

    將此專案的程式碼 clone 到您的本地環境，然後在專案根目錄下執行以下指令：

    ```bash
    gcloud run deploy linebot-file-service \
      --source . \
      --platform managed \
      --region asia-east1 \
      --allow-unauthenticated \
      --set-env-vars="ChannelSecret=YOUR_CHANNEL_SECRET" \
      --set-env-vars="ChannelAccessToken=YOUR_CHANNEL_ACCESS_TOKEN" \
      --set-env-vars="GOOGLE_CLIENT_ID=YOUR_GOOGLE_CLIENT_ID" \
      --set-env-vars="GOOGLE_CLIENT_SECRET=YOUR_GOOGLE_CLIENT_SECRET" \
      --set-env-vars="GOOGLE_REDIRECT_URL=YOUR_CLOUD_RUN_URL/oauth/callback"
    ```
    **參數說明：**
    *   `linebot-file-service`: 您的 Cloud Run 服務名稱，可自訂。
    *   `--region`: 建議選擇離您最近的地區，例如 `asia-east1` (台灣)。
    *   `--allow-unauthenticated`: 允許來自 LINE Platform 的公開請求。
    *   `YOUR_...`: 請替換成您自己的金鑰和憑證。
    *   `GOOGLE_REDIRECT_URL`: **此處先隨意填寫一個臨時網址**，例如 `https://temp.com`。

6.  **設定 Webhook 和 Redirect URI**

    *   部署完成後，Cloud Run 會提供給您一個服務 **URL** (例如 `https://linebot-file-service-xxxxxxxx-an.a.run.app`)。
    *   **更新 LINE Webhook**：前往 [LINE Developers Console](https://developers.line.biz/)，在您的 Bot 頻道設定中，將 `Webhook URL` 設為您的 Cloud Run 服務 URL。
    *   **更新 Google OAuth Redirect URI**：回到步驟 3 的憑證頁面，點擊您建立的 Web application 憑證進行編輯。在 **Authorized redirect URIs** 中，加入 `YOUR_CLOUD_RUN_URL/oauth/callback` (例如 `https://linebot-file-service-xxxxxxxx-an.a.run.app/oauth/callback`)。
    *   **重新部署 Cloud Run**：執行一次步驟 5 的 `gcloud run deploy` 指令，這次將 `GOOGLE_REDIRECT_URL` 的值更新為**正確的 Cloud Run 回呼網址**。

至此，您的 LINE Bot 已成功部署並在雲端運行！

## 📜 License


本專案採用 [Apache License 2.0](LICENSE)。

## 🤝 如何貢獻

我們非常歡迎任何形式的貢獻！如果您有任何建議或發現 Bug，請隨時提出 Issue。如果您想直接貢獻程式碼，請遵循以下步驟：

1.  Fork 此專案。
2.  建立您的功能分支 (`git checkout -b feature/AmazingFeature`)。
3.  提交您的變更 (`git commit -m 'Add some AmazingFeature'`)。
4.  將您的分支推送到遠端 (`git push origin feature/AmazingFeature`)。
5.  開啟一個 Pull Request。

感謝所有貢獻者的努力！
