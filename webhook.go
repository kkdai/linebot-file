// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/line/line-bot-sdk-go/v8/linebot/webhook"
)

// Supported chat commands.
const (
	cmdConnect     = "/connect_drive"
	cmdReconnect   = "/reconnect"
	cmdDisconnect  = "/disconnect_drive"
	cmdRecent      = "/recent_files"
	cmdSearch      = "/search_files"
	cmdSearchShort = "/q"
)

// parseCommand splits a text message into a command and its argument. It
// reports false for anything that is not one of the supported commands, so
// that lookalikes such as "/quit" are not treated as "/q uit".
func parseCommand(text string) (name, arg string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}

	name, arg, _ = strings.Cut(text, " ")
	switch name {
	case cmdConnect, cmdReconnect, cmdDisconnect, cmdRecent, cmdSearch, cmdSearchShort:
		return name, strings.TrimSpace(arg), true
	}
	return "", "", false
}

// userIDFromSource extracts the LINE user ID from an event source. It reports
// false for group and room sources, which carry no per-user identity we can
// map to a Drive account.
func userIDFromSource(source webhook.SourceInterface) (string, bool) {
	switch s := source.(type) {
	case webhook.UserSource:
		return s.UserId, s.UserId != ""
	case *webhook.UserSource:
		return s.UserId, s.UserId != ""
	}
	return "", false
}

// handledEvents remembers recently processed webhook event IDs. LINE retries a
// delivery it considers failed, and without this a retry would upload the same
// file to Drive a second time.
type handledEvents struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

var processedEvents = &handledEvents{seen: make(map[string]time.Time)}

// markHandled records id and reports whether this is the first time we have
// seen it. Events without an ID are always treated as new.
func (h *handledEvents) markHandled(id string) bool {
	if id == "" {
		return true
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	for k, seenAt := range h.seen {
		if now.Sub(seenAt) > eventTTL {
			delete(h.seen, k)
		}
	}

	if _, dup := h.seen[id]; dup {
		return false
	}
	h.seen[id] = now
	return true
}

// webhookHandler receives deliveries from the LINE platform.
func webhookHandler(w http.ResponseWriter, req *http.Request) {
	cb, err := webhook.ParseRequest(channelSecret, req)
	if err != nil {
		if errors.Is(err, webhook.ErrInvalidSignature) {
			log.Printf("Invalid signature: %v", err)
			w.WriteHeader(http.StatusBadRequest)
		} else {
			log.Printf("Cannot parse request: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}

	ctx, cancel := context.WithTimeout(req.Context(), webhookTimeout)
	defer cancel()

	// Every event in the batch is handled; a failure in one must not drop the
	// rest, and must not fail the delivery into a retry.
	for _, event := range cb.Events {
		handleEvent(ctx, event)
	}
	w.WriteHeader(http.StatusOK)
}

func handleEvent(ctx context.Context, event webhook.EventInterface) {
	switch e := event.(type) {
	case webhook.MessageEvent:
		if !processedEvents.markHandled(e.WebhookEventId) {
			log.Printf("Skipping duplicate delivery of event %s", e.WebhookEventId)
			return
		}
		handleMessageEvent(ctx, e)

	case webhook.FollowEvent:
		if userID, ok := userIDFromSource(e.Source); ok {
			log.Printf("Follow event for user %s", userID)
			linkRichMenu(userID, richMenuConnect)
		}

	case webhook.UnfollowEvent:
		if userID, ok := userIDFromSource(e.Source); ok {
			log.Printf("Unfollow event for user %s", userID)
		}

	case webhook.MemberJoinedEvent:
		log.Printf("Member joined: %+v", e.Joined)

	case webhook.MemberLeftEvent:
		log.Printf("Member left: %+v", e.Left)

	case webhook.BeaconEvent:
		if userID, ok := userIDFromSource(e.Source); ok {
			log.Printf("Beacon event from user %s", userID)
		}

	default:
		log.Printf("Unsupported event: %T", event)
	}
}

func handleMessageEvent(ctx context.Context, e webhook.MessageEvent) {
	switch message := e.Message.(type) {
	case webhook.TextMessageContent:
		handleTextMessage(ctx, e, message.Text)

	case webhook.ImageMessageContent:
		handleMediaUpload(ctx, e, message.Id, "line-bot-upload-"+message.Id+".jpg")

	case webhook.VideoMessageContent:
		handleMediaUpload(ctx, e, message.Id, "line-bot-upload-"+message.Id+".mp4")

	case webhook.AudioMessageContent:
		handleMediaUpload(ctx, e, message.Id, "line-bot-upload-"+message.Id+".m4a")

	case webhook.FileMessageContent:
		handleMediaUpload(ctx, e, message.Id, message.FileName)

	case webhook.StickerMessageContent:
		replyText(e.ReplyToken, fmt.Sprintf(
			"貼圖訊息: sticker id is %s, stickerResourceType is %s",
			message.StickerId, message.StickerResourceType))

	default:
		log.Printf("Unsupported message content: %T", e.Message)
	}
}

func handleTextMessage(ctx context.Context, e webhook.MessageEvent, text string) {
	name, arg, ok := parseCommand(text)
	if !ok {
		replyText(e.ReplyToken, text)
		return
	}

	userID, ok := userIDFromSource(e.Source)
	if !ok {
		replyText(e.ReplyToken, "此指令只能在與機器人的一對一聊天中使用。")
		return
	}

	switch name {
	case cmdConnect:
		handleConnect(ctx, e.ReplyToken, userID, false)
	case cmdReconnect:
		handleConnect(ctx, e.ReplyToken, userID, true)
	case cmdDisconnect:
		handleDisconnect(ctx, e.ReplyToken, userID)
	case cmdRecent:
		handleRecentFiles(ctx, e.ReplyToken, userID)
	case cmdSearch, cmdSearchShort:
		handleSearch(ctx, e.ReplyToken, userID, arg)
	}
}

// handleConnect starts the OAuth flow. When reconnect is set, any existing
// grant is revoked first so the user gets a clean re-authorization.
func handleConnect(ctx context.Context, replyToken, userID string, reconnect bool) {
	if reconnect {
		if err := revokeGoogleToken(ctx, userID); err != nil && !errors.Is(err, ErrOauth2TokenNotFound) {
			log.Printf("Error revoking token during reconnect for user %s: %v", userID, err)
		}
	}

	url, err := authCodeURL(ctx, userID)
	if err != nil {
		log.Printf("Failed to start OAuth flow for user %s: %v", userID, err)
		replyText(replyToken, "啟動授權流程時發生錯誤，請稍後再試。")
		return
	}

	prompt := "請授權本服務將檔案上傳至您的 Google Drive：\n"
	if reconnect {
		prompt = "請重新授權本服務存取您的 Google Drive：\n"
	}
	replyText(replyToken, prompt+url)
}

func handleDisconnect(ctx context.Context, replyToken, userID string) {
	switch err := revokeGoogleToken(ctx, userID); {
	case err == nil:
		replyText(replyToken, "已成功中斷與 Google Drive 的連線。", connectQuickReplies()...)
	case errors.Is(err, ErrOauth2TokenNotFound):
		replyText(replyToken, "您的帳號尚未連接 Google Drive。", connectQuickReplies()...)
	default:
		log.Printf("Failed to revoke token for user %s: %v", userID, err)
		replyText(replyToken, "中斷連線時發生錯誤，請稍後再試。")
	}
}

func handleRecentFiles(ctx context.Context, replyToken, userID string) {
	srv, err := getGoogleDriveService(ctx, userID)
	if err != nil {
		reportDriveError(replyToken, userID, "get drive service", err)
		return
	}

	files, err := getRecentFiles(ctx, srv, recentFileCount)
	if err != nil {
		reportDriveError(replyToken, userID, "get recent files", err)
		return
	}

	if len(files) == 0 {
		replyText(replyToken, "您還沒有上傳過任何檔案。", fileQuickReplies()...)
		return
	}

	reply(replyToken, flexMessage(
		"以下是您最近上傳的檔案",
		buildFileCarousel("最近上傳", files),
		fileQuickReplies()...))
}

func handleSearch(ctx context.Context, replyToken, userID, query string) {
	if query == "" {
		replyText(replyToken, "請輸入要搜尋的檔案名稱，例如："+cmdSearch+" 照片")
		return
	}

	srv, err := getGoogleDriveService(ctx, userID)
	if err != nil {
		reportDriveError(replyToken, userID, "get drive service", err)
		return
	}

	files, err := searchFilesInDrive(ctx, srv, query)
	if err != nil {
		reportDriveError(replyToken, userID, "search files", err)
		return
	}

	if len(files) == 0 {
		replyText(replyToken, fmt.Sprintf("找不到包含「%s」的檔案。", query), fileQuickReplies()...)
		return
	}

	reply(replyToken, flexMessage(
		fmt.Sprintf("找到 %d 個包含「%s」的檔案", len(files), query),
		buildFileCarousel("搜尋結果", files),
		fileQuickReplies()...))
}

func handleMediaUpload(ctx context.Context, e webhook.MessageEvent, messageID, fileName string) {
	userID, ok := userIDFromSource(e.Source)
	if !ok {
		log.Printf("Ignoring media message from non-user source %T", e.Source)
		return
	}

	content, err := blob.GetMessageContent(messageID)
	if err != nil {
		log.Printf("Failed to get message content %s: %v", messageID, err)
		replyText(e.ReplyToken, "無法讀取這個檔案，請稍後再試。")
		return
	}
	defer content.Body.Close()

	file, err := uploadToDrive(ctx, content.Body, fileName, userID)
	if err != nil {
		reportDriveError(e.ReplyToken, userID, "upload to drive", err)
		return
	}

	// A large upload can outlive the reply token, so fall back to a push.
	replyOrPush(e.ReplyToken, userID, textMessage(
		"檔案已上傳至 Google Drive：\n"+file.WebViewLink,
		fileQuickReplies()...))
}

// reportDriveError turns a Drive or token failure into the right user-facing
// prompt, and logs anything the user cannot act on.
func reportDriveError(replyToken, userID, operation string, err error) {
	switch {
	case errors.Is(err, ErrOauth2TokenNotFound):
		sendConnectionPrompt(replyToken)
	case isGoogleAuthError(err):
		sendReconnectionPrompt(replyToken)
	default:
		log.Printf("Failed to %s for user %s: %v", operation, userID, err)
		replyText(replyToken, "操作 Google Drive 時發生錯誤，請稍後再試。")
	}
}
