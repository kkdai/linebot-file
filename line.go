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
	"log"

	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
	"google.golang.org/api/drive/v3"
)

// messageAction builds a quick reply button that sends text as the user.
func messageAction(label, text string) messaging_api.QuickReplyItem {
	return messaging_api.QuickReplyItem{
		Action: &messaging_api.MessageAction{Label: label, Text: text},
	}
}

// connectQuickReplies is offered when the user has no Drive connection yet.
func connectQuickReplies() []messaging_api.QuickReplyItem {
	return []messaging_api.QuickReplyItem{
		messageAction("連接 Google Drive", cmdConnect),
	}
}

// reconnectQuickReplies is offered when the stored token no longer works.
func reconnectQuickReplies() []messaging_api.QuickReplyItem {
	return []messaging_api.QuickReplyItem{
		messageAction("重新連線", cmdReconnect),
	}
}

// fileQuickReplies is offered alongside any successful file operation.
func fileQuickReplies() []messaging_api.QuickReplyItem {
	return []messaging_api.QuickReplyItem{
		messageAction("查詢最近檔案", cmdRecent),
		messageAction("中斷連線", cmdDisconnect),
	}
}

// replyText answers an event with a plain text message.
func replyText(replyToken, text string, items ...messaging_api.QuickReplyItem) {
	reply(replyToken, textMessage(text, items...))
}

func textMessage(text string, items ...messaging_api.QuickReplyItem) *messaging_api.TextMessage {
	msg := &messaging_api.TextMessage{Text: text}
	if len(items) > 0 {
		msg.QuickReply = &messaging_api.QuickReply{Items: items}
	}
	return msg
}

func flexMessage(altText string, contents messaging_api.FlexContainerInterface, items ...messaging_api.QuickReplyItem) *messaging_api.FlexMessage {
	msg := &messaging_api.FlexMessage{AltText: altText, Contents: contents}
	if len(items) > 0 {
		msg.QuickReply = &messaging_api.QuickReply{Items: items}
	}
	return msg
}

func reply(replyToken string, messages ...messaging_api.MessageInterface) {
	if _, err := bot.ReplyMessage(&messaging_api.ReplyMessageRequest{
		ReplyToken: replyToken,
		Messages:   messages,
	}); err != nil {
		log.Printf("Failed to send reply: %v", err)
	}
}

// replyOrPush answers with a reply token, falling back to a push message.
// Uploads can outlive the reply token's validity, and a push still reaches
// the user in that case.
func replyOrPush(replyToken, userID string, messages ...messaging_api.MessageInterface) {
	_, err := bot.ReplyMessage(&messaging_api.ReplyMessageRequest{
		ReplyToken: replyToken,
		Messages:   messages,
	})
	if err == nil {
		return
	}
	log.Printf("Reply failed, falling back to push: %v", err)

	if _, err := bot.PushMessage(&messaging_api.PushMessageRequest{
		To:       userID,
		Messages: messages,
	}, ""); err != nil {
		log.Printf("Failed to push message to user %s: %v", userID, err)
	}
}

// sendConnectionPrompt asks a user who has never connected to do so.
func sendConnectionPrompt(replyToken string) {
	replyText(replyToken, "請先連接您的 Google Drive 帳號。", connectQuickReplies()...)
}

// sendReconnectionPrompt asks a user whose token has expired or been revoked
// to authorize again.
func sendReconnectionPrompt(replyToken string) {
	replyText(replyToken,
		"您的 Google Drive 授權似乎已失效。\n請執行 "+cmdReconnect+" 指令來重新連線。",
		reconnectQuickReplies()...)
}

// linkRichMenu switches the user's rich menu, logging rather than failing the
// surrounding operation: the menu is cosmetic.
func linkRichMenu(userID, richMenuID string) {
	if _, err := bot.LinkRichMenuIdToUser(userID, richMenuID); err != nil {
		log.Printf("Failed to link rich menu %s for user %s: %v", richMenuID, userID, err)
	}
}

// buildFileCarousel renders a list of Drive files as a Flex carousel. header
// labels each bubble, e.g. "搜尋結果".
func buildFileCarousel(header string, files []*drive.File) *messaging_api.FlexCarousel {
	bubbles := make([]messaging_api.FlexBubble, 0, len(files))
	for _, file := range files {
		bubbles = append(bubbles, messaging_api.FlexBubble{
			Body: &messaging_api.FlexBox{
				Layout: "vertical",
				Contents: []messaging_api.FlexComponentInterface{
					&messaging_api.FlexText{
						Text:   header,
						Weight: "bold",
						Size:   "sm",
						Color:  "#1DB446",
					},
					&messaging_api.FlexText{
						Text:   file.Name,
						Weight: "bold",
						Size:   "xl",
						Margin: "md",
						Wrap:   true,
					},
				},
			},
			Footer: &messaging_api.FlexBox{
				Layout:  "vertical",
				Spacing: "sm",
				Contents: []messaging_api.FlexComponentInterface{
					&messaging_api.FlexButton{
						Style:  "link",
						Height: "sm",
						Action: &messaging_api.UriAction{
							Label: "在 Drive 中開啟",
							Uri:   file.WebViewLink,
						},
					},
				},
			},
		})
	}
	return &messaging_api.FlexCarousel{Contents: bubbles}
}
