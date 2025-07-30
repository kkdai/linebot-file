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
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
	"github.com/line/line-bot-sdk-go/v8/linebot/webhook"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

var (
	googleOauthConfig *oauth2.Config
	// TODO: This should be stored in a database.
	oauth2Token            *oauth2.Token
	ErrOauth2TokenNotFound = errors.New("oauth2 token not found")
)

func main() {
	googleOauthConfig = &oauth2.Config{
		RedirectURL:  os.Getenv("GOOGLE_REDIRECT_URL"),
		ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		Scopes:       []string{drive.DriveFileScope},
		Endpoint:     google.Endpoint,
	}

	channelSecret := os.Getenv("ChannelSecret")
	bot, err := messaging_api.NewMessagingApiAPI(
		os.Getenv("ChannelAccessToken"),
	)
	if err != nil {
		log.Fatal(err)
	}
	blob, err := messaging_api.NewMessagingApiBlobAPI(
		os.Getenv("ChannelAccessToken"),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Setup HTTP Server for receiving requests from LINE platform
	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		// The LINE Platform always POSTs to the webhook URL.
		// We only handle requests to the root path.
		if req.URL.Path != "/" {
			http.NotFound(w, req)
			return
		}

		log.Println("Webhook handler called...")

		cb, err := webhook.ParseRequest(channelSecret, req)
		if err != nil {
			log.Printf("Cannot parse request: %+v\n", err)
			if errors.Is(err, webhook.ErrInvalidSignature) {
				w.WriteHeader(400)
			} else {
				w.WriteHeader(500)
			}
			return
		}

		log.Println("Handling events...")
		for _, event := range cb.Events {
			log.Printf("/callback called%+v...\n", event)

			switch e := event.(type) {
			case webhook.MessageEvent:
				switch message := e.Message.(type) {
				case webhook.TextMessageContent:
					if message.Text == "/connect_drive" {
						state := generateStateOauthCookie(w)
						url := googleOauthConfig.AuthCodeURL(state)
						if _, err = bot.ReplyMessage(
							&messaging_api.ReplyMessageRequest{
								ReplyToken: e.ReplyToken,
								Messages: []messaging_api.MessageInterface{
									messaging_api.TextMessage{
										Text: "Please authorize this app to upload files to your Google Drive: " + url,
									},
								},
							},
						); err != nil {
							log.Print(err)
						}
						return
					}

					if _, err = bot.ReplyMessage(
						&messaging_api.ReplyMessageRequest{
							ReplyToken: e.ReplyToken,
							Messages: []messaging_api.MessageInterface{
								messaging_api.TextMessage{
									Text: message.Text,
								},
							},
						},
					); err != nil {
						log.Print(err)
					} else {
						log.Println("Sent text reply.")
					}
				case webhook.StickerMessageContent:
					replyMessage := fmt.Sprintf(
						"貼圖訊息: sticker id is %s, stickerResourceType is %s", message.StickerId, message.StickerResourceType)
					if _, err = bot.ReplyMessage(
						&messaging_api.ReplyMessageRequest{
							ReplyToken: e.ReplyToken,
							Messages: []messaging_api.MessageInterface{
								messaging_api.TextMessage{
									Text: replyMessage,
								},
							},
						}); err != nil {
						log.Print(err)
					} else {
						log.Println("Sent sticker reply.")
					}
				case webhook.ImageMessageContent:
					content, err := blob.GetMessageContent(message.Id)
					if err != nil {
						log.Printf("Failed to get message content: %v", err)
						return
					}
					defer content.Body.Close()

					file, err := uploadToDrive(content.Body, "line-bot-upload-"+message.Id)
					if err != nil {
						log.Printf("Failed to upload to drive: %v", err)
						if errors.Is(err, ErrOauth2TokenNotFound) {
							if _, err = bot.ReplyMessage(
								&messaging_api.ReplyMessageRequest{
									ReplyToken: e.ReplyToken,
									Messages: []messaging_api.MessageInterface{
										messaging_api.TextMessage{
											Text: "Please connect your Google Drive account first by sending `/connect_drive`.",
										},
									},
								},
							); err != nil {
								log.Print(err)
							}
						}
						return
					}

					if _, err = bot.ReplyMessage(
						&messaging_api.ReplyMessageRequest{
							ReplyToken: e.ReplyToken,
							Messages: []messaging_api.MessageInterface{
								messaging_api.TextMessage{
									Text: "File uploaded to Google Drive: " + file.WebViewLink,
								},
							},
						},
					); err != nil {
						log.Print(err)
					}
				case webhook.VideoMessageContent:
					content, err := blob.GetMessageContent(message.Id)
					if err != nil {
						log.Printf("Failed to get message content: %v", err)
						return
					}
					defer content.Body.Close()

					file, err := uploadToDrive(content.Body, "line-bot-upload-"+message.Id)
					if err != nil {
						log.Printf("Failed to upload to drive: %v", err)
						if errors.Is(err, ErrOauth2TokenNotFound) {
							if _, err = bot.ReplyMessage(
								&messaging_api.ReplyMessageRequest{
									ReplyToken: e.ReplyToken,
									Messages: []messaging_api.MessageInterface{
										messaging_api.TextMessage{
											Text: "Please connect your Google Drive account first by sending `/connect_drive`.",
										},
									},
								},
							); err != nil {
								log.Print(err)
							}
						}
						return
					}

					if _, err = bot.ReplyMessage(
						&messaging_api.ReplyMessageRequest{
							ReplyToken: e.ReplyToken,
							Messages: []messaging_api.MessageInterface{
								messaging_api.TextMessage{
									Text: "File uploaded to Google Drive: " + file.WebViewLink,
								},
							},
						},
					); err != nil {
						log.Print(err)
					}
				case webhook.AudioMessageContent:
					content, err := blob.GetMessageContent(message.Id)
					if err != nil {
						log.Printf("Failed to get message content: %v", err)
						return
					}
					defer content.Body.Close()

					file, err := uploadToDrive(content.Body, "line-bot-upload-"+message.Id)
					if err != nil {
						log.Printf("Failed to upload to drive: %v", err)
						if errors.Is(err, ErrOauth2TokenNotFound) {
							if _, err = bot.ReplyMessage(
								&messaging_api.ReplyMessageRequest{
									ReplyToken: e.ReplyToken,
									Messages: []messaging_api.MessageInterface{
										messaging_api.TextMessage{
											Text: "Please connect your Google Drive account first by sending `/connect_drive`.",
										},
									},
								},
							); err != nil {
								log.Print(err)
							}
						}
						return
					}

					if _, err = bot.ReplyMessage(
						&messaging_api.ReplyMessageRequest{
							ReplyToken: e.ReplyToken,
							Messages: []messaging_api.MessageInterface{
								messaging_api.TextMessage{
									Text: "File uploaded to Google Drive: " + file.WebViewLink,
								},
							},
						},
					); err != nil {
						log.Print(err)
					}
				case webhook.MemberJoinedEvent:
					log.Printf("Member joined: %s\n", e.Source.(webhook.UserSource).UserId)
				case webhook.MemberLeftEvent:
					log.Printf("Member joined: %s\n", e.Source.(webhook.UserSource).UserId)
				case webhook.FollowEvent:
					log.Printf("Follow event: %s\n", e.Source.(webhook.UserSource).UserId)
				case webhook.BeaconEvent:
					log.Printf("Beacon event: %s\n", e.Source.(webhook.UserSource).UserId)
				default:
					log.Printf("Unsupported message content: %T\n", e.Message)
				}
			default:
				log.Printf("Unsupported message: %T\n", event)
			}
		}
		w.WriteHeader(http.StatusOK)
	})

	http.HandleFunc("/oauth/callback", oauthCallbackHandler)

	// This is just sample code.
	// For actual use, you must support HTTPS by using `ListenAndServeTLS`, a reverse proxy or something else.
	port := os.Getenv("PORT")
	if port == "" {
		port = "5000"
	}
	fmt.Println("http://localhost:" + port + "/")
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

func generateStateOauthCookie(w http.ResponseWriter) string {
	var expiration = time.Now().Add(20 * time.Minute)
	b := make([]byte, 16)
	rand.Read(b)
	state := base64.URLEncoding.EncodeToString(b)
	cookie := http.Cookie{Name: "oauthstate", Value: state, Expires: expiration}
	http.SetCookie(w, &cookie)

	return state
}

func oauthCallbackHandler(w http.ResponseWriter, r *http.Request) {
	oauthState, _ := r.Cookie("oauthstate")

	if r.FormValue("state") != oauthState.Value {
		log.Printf("invalid oauth google state")
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	token, err := googleOauthConfig.Exchange(context.Background(), r.FormValue("code"))
	if err != nil {
		log.Printf("failed to exchange token: %v", err)
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}
	oauth2Token = token

	// In a real-world app, you would store the token in a database.
	// For this example, we'll just log that it was successful.
	log.Println("Successfully authenticated with Google")
	w.Write([]byte("Successfully authenticated with Google. You can close this window."))
}

func getGoogleDriveService() (*drive.Service, error) {
	if oauth2Token == nil {
		return nil, ErrOauth2TokenNotFound
	}
	return drive.NewService(context.Background(), option.WithTokenSource(googleOauthConfig.TokenSource(context.Background(), oauth2Token)))
}

func uploadToDrive(content io.Reader, filename string) (*drive.File, error) {
	srv, err := getGoogleDriveService()
	if err != nil {
		return nil, err
	}

	file := &drive.File{
		Name: filename,
	}

	return srv.Files.Create(file).Media(content).Do()
}
