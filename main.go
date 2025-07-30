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

	"cloud.google.com/go/firestore"
	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
	"github.com/line/line-bot-sdk-go/v8/linebot/webhook"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	googleOauthConfig      *oauth2.Config
	firestoreClient        *firestore.Client
	ErrOauth2TokenNotFound = errors.New("oauth2 token not found")
)

const (
	stateCollection = "oauth_states"
	tokenCollection = "user_tokens"
)

func main() {
	ctx := context.Background()
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		log.Fatal("GOOGLE_CLOUD_PROJECT environment variable must be set.")
	}

	var err error
	firestoreClient, err = firestore.NewClient(ctx, projectID)
	if err != nil {
		log.Fatalf("Failed to create Firestore client: %v", err)
	}
	defer firestoreClient.Close()

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
				var userID string
				switch s := e.Source.(type) {
				case *webhook.UserSource:
					userID = s.UserId
				case *webhook.GroupSource:
					userID = s.UserId
				case *webhook.RoomSource:
					userID = s.UserId
				}

				switch message := e.Message.(type) {
				case webhook.TextMessageContent:
					if message.Text == "/connect_drive" {
						if userID == "" {
							log.Println("Cannot get UserID from the event.")
							// Handle cases where user ID might be missing
							return
						}
						// Generate a random state string to prevent CSRF attacks
						state := generateState()

						// Store state and user ID in Firestore with a short expiration
						_, err := firestoreClient.Collection(stateCollection).Doc(state).Set(ctx, map[string]interface{}{
							"user_id":    userID,
							"created_at": time.Now(),
						})
						if err != nil {
							log.Printf("Failed to save state to firestore: %v", err)
							// Optionally reply to user about the error
							return
						}

						// Generate authorization URL
						url := googleOauthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
						if _, err = bot.ReplyMessage(
							&messaging_api.ReplyMessageRequest{
								ReplyToken: e.ReplyToken,
								Messages: []messaging_api.MessageInterface{
									&messaging_api.TextMessage{
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
								&messaging_api.TextMessage{
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
								&messaging_api.TextMessage{
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

					file, err := uploadToDrive(content.Body, "line-bot-upload-"+message.Id, userID)
					if err != nil {
						log.Printf("Failed to upload to drive: %v", err)
						if errors.Is(err, ErrOauth2TokenNotFound) {
							if _, err = bot.ReplyMessage(
								&messaging_api.ReplyMessageRequest{
									ReplyToken: e.ReplyToken,
									Messages: []messaging_api.MessageInterface{
										&messaging_api.TextMessage{
											Text: "Please connect your Google Drive account first.",
											QuickReply: &messaging_api.QuickReply{
												Items: []messaging_api.QuickReplyItem{
													{
														Action: &messaging_api.MessageAction{
															Label: "Connect Google Drive",
															Text:  "/connect_drive",
														},
													},
												},
											},
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
								&messaging_api.TextMessage{
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

					file, err := uploadToDrive(content.Body, "line-bot-upload-"+message.Id, userID)
					if err != nil {
						log.Printf("Failed to upload to drive: %v", err)
						if errors.Is(err, ErrOauth2TokenNotFound) {
							if _, err = bot.ReplyMessage(
								&messaging_api.ReplyMessageRequest{
									ReplyToken: e.ReplyToken,
									Messages: []messaging_api.MessageInterface{
										&messaging_api.TextMessage{
											Text: "Please connect your Google Drive account first.",
											QuickReply: &messaging_api.QuickReply{
												Items: []messaging_api.QuickReplyItem{
													{
														Action: &messaging_api.MessageAction{
															Label: "Connect Google Drive",
															Text:  "/connect_drive",
														},
													},
												},
											},
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
								&messaging_api.TextMessage{
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

					file, err := uploadToDrive(content.Body, "line-bot-upload-"+message.Id, userID)
					if err != nil {
						log.Printf("Failed to upload to drive: %v", err)
						if errors.Is(err, ErrOauth2TokenNotFound) {
							if _, err = bot.ReplyMessage(
								&messaging_api.ReplyMessageRequest{
									ReplyToken: e.ReplyToken,
									Messages: []messaging_api.MessageInterface{
										&messaging_api.TextMessage{
											Text: "Please connect your Google Drive account first.",
											QuickReply: &messaging_api.QuickReply{
												Items: []messaging_api.QuickReplyItem{
													{
														Action: &messaging_api.MessageAction{
															Label: "Connect Google Drive",
															Text:  "/connect_drive",
														},
													},
												},
											},
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
								&messaging_api.TextMessage{
									Text: "File uploaded to Google Drive: " + file.WebViewLink,
								},
							},
						},
					); err != nil {
						log.Print(err)
					}
				case webhook.MemberJoinedEvent:
					if s, ok := e.Source.(*webhook.GroupSource); ok {
						log.Printf("Member joined: %s\n", s.UserId)
					}
				case webhook.MemberLeftEvent:
					if s, ok := e.Source.(*webhook.GroupSource); ok {
						log.Printf("Member left: %s\n", s.UserId)
					}
				case webhook.FollowEvent:
					if s, ok := e.Source.(*webhook.UserSource); ok {
						log.Printf("Follow event: %s\n", s.UserId)
					}
				case webhook.BeaconEvent:
					if s, ok := e.Source.(*webhook.UserSource); ok {
						log.Printf("Beacon event: %s\n", s.UserId)
					}
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

func generateState() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}

func oauthCallbackHandler(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	state := r.FormValue("state")
	code := r.FormValue("code")

	// 1. Validate state and get user ID from Firestore
	doc, err := firestoreClient.Collection(stateCollection).Doc(state).Get(ctx)
	if err != nil {
		log.Printf("Invalid oauth google state: %s, error: %v", state, err)
		http.Error(w, "Invalid state parameter. Please try again.", http.StatusBadRequest)
		return
	}
	// Delete state after use to prevent replay attacks
	defer doc.Ref.Delete(ctx)

	var stateData struct {
		UserID string `firestore:"user_id"`
	}
	if err := doc.DataTo(&stateData); err != nil {
		log.Printf("Failed to parse state data: %v", err)
		http.Error(w, "Internal server error.", http.StatusInternalServerError)
		return
	}
	userID := stateData.UserID

	// 2. Exchange authorization code for a token
	token, err := googleOauthConfig.Exchange(ctx, code)
	if err != nil {
		log.Printf("Failed to exchange token: %v", err)
		http.Error(w, "Failed to exchange token.", http.StatusInternalServerError)
		return
	}

	// 3. Store the token in Firestore, using the userID as the document ID
	_, err = firestoreClient.Collection(tokenCollection).Doc(userID).Set(ctx, token)
	if err != nil {
		log.Printf("Failed to save token to firestore: %v", err)
		http.Error(w, "Failed to save token.", http.StatusInternalServerError)
		return
	}

	log.Printf("Successfully saved token for user %s", userID)
	fmt.Fprintf(w, "授權成功！您現在可以回到 LINE 傳送檔案了。")
}

func getGoogleDriveService(userID string) (*drive.Service, error) {
	doc, err := firestoreClient.Collection(tokenCollection).Doc(userID).Get(context.Background())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, ErrOauth2TokenNotFound
		}
		return nil, fmt.Errorf("failed to get token from firestore: %w", err)
	}

	var token oauth2.Token
	if err := doc.DataTo(&token); err != nil {
		return nil, fmt.Errorf("failed to parse token data: %w", err)
	}

	return drive.NewService(context.Background(), option.WithTokenSource(googleOauthConfig.TokenSource(context.Background(), &token)))
}

func uploadToDrive(content io.Reader, filename string, userID string) (*drive.File, error) {
	srv, err := getGoogleDriveService(userID)
	if err != nil {
		return nil, err
	}

	file := &drive.File{
		Name: filename,
	}

	return srv.Files.Create(file).Media(content).Do()
}
