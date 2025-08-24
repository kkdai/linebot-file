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
	"google.golang.org/api/googleapi"
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
	tokenCollection        = "user_tokens"
	richMenuConnect        = "richmenu-8360de4ffc27c9eba7849980675ae7f3"
	richMenuMain           = "richmenu-94cf1a33f7ddd92e65d40d5964070806"
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
				switch message := e.Message.(type) {
				case webhook.TextMessageContent:
					if message.Text == "/connect_drive" {
						// Generate a random state string to prevent CSRF attacks
						userID := e.Source.(webhook.UserSource).UserId
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
					} else if message.Text == "/recent_files" {
						userID := e.Source.(webhook.UserSource).UserId
						srv, err := getGoogleDriveService(userID)
						if err != nil {
							// Handle not connected error
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
							} else if isGoogleAuthError(err) {
								sendReconnectionPrompt(bot, e.ReplyToken)
							} else {
								log.Printf("Failed to get drive service: %v", err)
							}
							return
						}

						files, err := getRecentFiles(srv, 5)
						if err != nil {
							log.Printf("Failed to get recent files: %v", err)
							if isGoogleAuthError(err) {
								sendReconnectionPrompt(bot, e.ReplyToken)
							}
							// Optionally reply with an error message
							return
						}

						if len(files) == 0 {
							if _, err = bot.ReplyMessage(
								&messaging_api.ReplyMessageRequest{
									ReplyToken: e.ReplyToken,
									Messages: []messaging_api.MessageInterface{
										&messaging_api.TextMessage{
											Text: "You haven't uploaded any files yet.",
										},
									},
								},
							); err != nil {
								log.Print(err)
							}
							return
						}

						var bubbles []messaging_api.FlexBubble
						for _, file := range files {
							bubble := messaging_api.FlexBubble{
								Body: &messaging_api.FlexBox{
									Layout: "vertical",
									Contents: []messaging_api.FlexComponentInterface{
										&messaging_api.FlexText{
											Text:   "Recent Upload",
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
												Label: "Open in Drive",
												Uri:   file.WebViewLink,
											},
										},
									},
								},
							}
							bubbles = append(bubbles, bubble)
						}

						carousel := &messaging_api.FlexCarousel{
							Contents: bubbles,
						}

						if _, err = bot.ReplyMessage(
							&messaging_api.ReplyMessageRequest{
								ReplyToken: e.ReplyToken,
								Messages: []messaging_api.MessageInterface{
									&messaging_api.FlexMessage{
										AltText:  "Here are your recent files",
										Contents: carousel,
										QuickReply: &messaging_api.QuickReply{
											Items: []messaging_api.QuickReplyItem{
												{
													Action: &messaging_api.MessageAction{
														Label: "查詢最近檔案",
														Text:  "/recent_files",
													},
												},
												{
													Action: &messaging_api.MessageAction{
														Label: "中斷連線",
														Text:  "/disconnect_drive",
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
						return
					} else if message.Text == "/disconnect_drive" {
						userID := e.Source.(webhook.UserSource).UserId
						err := revokeGoogleToken(ctx, userID)
						var replyText string
						if err != nil {
							if errors.Is(err, ErrOauth2TokenNotFound) {
								replyText = "Your account is not connected to Google Drive."
							} else {
								replyText = "An error occurred while disconnecting. Please try again later."
								log.Printf("Failed to revoke token for user %s: %v", userID, err)
							}
						} else {
							replyText = "Successfully disconnected from Google Drive."
						}

						if _, err = bot.ReplyMessage(
							&messaging_api.ReplyMessageRequest{
								ReplyToken: e.ReplyToken,
								Messages: []messaging_api.MessageInterface{
									&messaging_api.TextMessage{
										Text: replyText,
									},
								},
							},
						); err != nil {
							log.Print(err)
						}
						return
					} else if message.Text == "/reconnect" {
						userID := e.Source.(webhook.UserSource).UserId

						// 1. Revoke existing token. We log errors but proceed anyway.
						err := revokeGoogleToken(ctx, userID)
						if err != nil && !errors.Is(err, ErrOauth2TokenNotFound) {
							log.Printf("Error during token revocation in /reconnect for user %s: %v", userID, err)
						}

						// 2. Start new connection flow (same as /connect_drive)
						state := generateState()
						_, err = firestoreClient.Collection(stateCollection).Doc(state).Set(ctx, map[string]interface{}{
							"user_id":    userID,
							"created_at": time.Now(),
						})
						if err != nil {
							log.Printf("Failed to save state to firestore for reconnect: %v", err)
							// Reply with an error message
							if _, err = bot.ReplyMessage(
								&messaging_api.ReplyMessageRequest{
									ReplyToken: e.ReplyToken,
									Messages: []messaging_api.MessageInterface{
										&messaging_api.TextMessage{
											Text: "An error occurred while trying to reconnect. Please try '/connect_drive' manually.",
										},
									},
								},
							); err != nil {
								log.Print(err)
							}
							return
						}

						url := googleOauthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
						if _, err = bot.ReplyMessage(
							&messaging_api.ReplyMessageRequest{
								ReplyToken: e.ReplyToken,
								Messages: []messaging_api.MessageInterface{
									&messaging_api.TextMessage{
										Text: "Please re-authorize this app to upload files to your Google Drive: " + url,
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
					handleMediaUpload(bot, blob, e.ReplyToken, e.Source.(webhook.UserSource).UserId, message.Id, "line-bot-upload-"+message.Id+".jpg")
				case webhook.VideoMessageContent:
					handleMediaUpload(bot, blob, e.ReplyToken, e.Source.(webhook.UserSource).UserId, message.Id, "line-bot-upload-"+message.Id+".mp4")
				case webhook.AudioMessageContent:
					handleMediaUpload(bot, blob, e.ReplyToken, e.Source.(webhook.UserSource).UserId, message.Id, "line-bot-upload-"+message.Id+".m4a")
				case webhook.FileMessageContent:
					handleMediaUpload(bot, blob, e.ReplyToken, e.Source.(webhook.UserSource).UserId, message.Id, message.FileName)
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
						log.Printf("Follow event for user: %s", s.UserId)
						if _, err := bot.LinkRichMenuIdToUser(s.UserId, richMenuConnect); err != nil {
							log.Printf("Failed to link rich menu for new user %s: %v", s.UserId, err)
						}
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

	// 4. Link the main rich menu to the user
	richMenuSwitcher, err := messaging_api.NewMessagingApiAPI(os.Getenv("ChannelAccessToken"))
	if err != nil {
		log.Printf("Failed to create messaging api client for rich menu linking: %v", err)
	} else {
		if _, err := richMenuSwitcher.LinkRichMenuIdToUser(userID, richMenuMain); err != nil {
			log.Printf("Failed to link rich menu for user %s: %v", userID, err)
		}
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

	// 1. Find or create the main folder "LINE Bot Uploads"
	mainFolderID, err := findOrCreateFolder(srv, "LINE Bot Uploads", "root")
	if err != nil {
		return nil, fmt.Errorf("failed to find or create main folder: %w", err)
	}

	// 2. Find or create the subfolder for the current month "YYYY-MM"
	monthFolderName := time.Now().Format("2006-01")
	monthFolderID, err := findOrCreateFolder(srv, monthFolderName, mainFolderID)
	if err != nil {
		return nil, fmt.Errorf("failed to find or create month subfolder: %w", err)
	}

	// 3. Upload the file to the month-specific subfolder
	file := &drive.File{
		Name:    filename,
		Parents: []string{monthFolderID},
	}

	return srv.Files.Create(file).Media(content).Do()
}

// findOrCreateFolder searches for a folder with a given name and parent.
// If not found, it creates the folder. It returns the folder ID.
func findOrCreateFolder(srv *drive.Service, name string, parentID string) (string, error) {
	query := fmt.Sprintf("mimeType='application/vnd.google-apps.folder' and trashed=false and name='%s' and '%s' in parents", name, parentID)
	r, err := srv.Files.List().Q(query).PageSize(1).Fields("files(id)").Do()
	if err != nil {
		return "", fmt.Errorf("failed to search for folder '%s': %w", name, err)
	}

	if len(r.Files) > 0 {
		// Folder found
		return r.Files[0].Id, nil
	}

	// Folder not found, create it
	folder := &drive.File{
		Name:     name,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{parentID},
	}

	createdFolder, err := srv.Files.Create(folder).Fields("id").Do()
	if err != nil {
		return "", fmt.Errorf("failed to create folder '%s': %w", name, err)
	}

	return createdFolder.Id, nil
}

func getRecentFiles(srv *drive.Service, count int64) ([]*drive.File, error) {
	// First, find the main folder. If it doesn't exist, there are no files to list.
	mainFolderID, err := findOrCreateFolder(srv, "LINE Bot Uploads", "root")
	if err != nil {
		// If findOrCreateFolder returns an error, we wrap it.
		return nil, fmt.Errorf("could not find or create the main upload folder: %w", err)
	}

	// Search for files within the main folder, ordering by creation date.
	query := fmt.Sprintf("'%s' in parents and trashed=false", mainFolderID)
	r, err := srv.Files.List().
		Q(query).
		PageSize(count).
		OrderBy("createdTime desc").
		Fields("files(id, name, webViewLink)").
		Do()

	if err != nil {
		return nil, fmt.Errorf("failed to retrieve files: %w", err)
	}

	return r.Files, nil
}

func revokeGoogleToken(ctx context.Context, userID string) error {
	// 1. Get token from Firestore
	docRef := firestoreClient.Collection(tokenCollection).Doc(userID)
	doc, err := docRef.Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return ErrOauth2TokenNotFound
		}
		return fmt.Errorf("failed to get token from firestore: %w", err)
	}

	var token oauth2.Token
	if err := doc.DataTo(&token); err != nil {
		return fmt.Errorf("failed to parse token data: %w", err)
	}

	// Token to revoke - prefer refresh token as it invalidates all derived access tokens
	tokenToRevoke := token.AccessToken
	if token.RefreshToken != "" {
		tokenToRevoke = token.RefreshToken
	}

	// 2. Revoke token with Google
	revokeURL := "https://oauth2.googleapis.com/revoke?token=" + tokenToRevoke
	resp, err := http.Post(revokeURL, "application/x-www-form-urlencoded", nil)
	if err != nil {
		return fmt.Errorf("failed to send revocation request to google: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		// Log the error but don't block deletion from our side
		log.Printf("Google revocation failed for user %s with status %d: %s", userID, resp.StatusCode, string(body))
	}

	// 3. Delete token from Firestore regardless of revocation status
	if _, err := docRef.Delete(ctx); err != nil {
		log.Printf("CRITICAL: Failed to delete token for user %s from Firestore after revocation attempt: %v", userID, err)
		return fmt.Errorf("failed to delete token from firestore: %w", err)
	}

	// 4. Link the connect rich menu back to the user
	richMenuSwitcher, err := messaging_api.NewMessagingApiAPI(os.Getenv("ChannelAccessToken"))
	if err != nil {
		log.Printf("Failed to create messaging api client for rich menu linking: %v", err)
	} else {
		if _, err := richMenuSwitcher.LinkRichMenuIdToUser(userID, richMenuConnect); err != nil {
			log.Printf("Failed to link rich menu for user %s: %v", userID, err)
		}
	}

	log.Printf("Successfully revoked and/or deleted token for user %s", userID)
	return nil
}

func handleMediaUpload(bot *messaging_api.MessagingApiAPI, blob *messaging_api.MessagingApiBlobAPI, replyToken, userID, messageID, fileName string) {
	content, err := blob.GetMessageContent(messageID)
	if err != nil {
		log.Printf("Failed to get message content: %v", err)
		return
	}
	defer content.Body.Close()

	file, err := uploadToDrive(content.Body, fileName, userID)
	if err != nil {
		log.Printf("Failed to upload to drive: %v", err)
		if errors.Is(err, ErrOauth2TokenNotFound) {
			sendConnectionPrompt(bot, replyToken)
		} else if isGoogleAuthError(err) {
			sendReconnectionPrompt(bot, replyToken)
		}
		// Optionally, handle other upload errors with a generic message
		return
	}

	sendUploadSuccessReply(bot, replyToken, file.WebViewLink)
}

func sendUploadSuccessReply(bot *messaging_api.MessagingApiAPI, replyToken, fileURL string) {
	if _, err := bot.ReplyMessage(
		&messaging_api.ReplyMessageRequest{
			ReplyToken: replyToken,
			Messages: []messaging_api.MessageInterface{
				&messaging_api.TextMessage{
					Text: "File uploaded to Google Drive: " + fileURL,
					QuickReply: &messaging_api.QuickReply{
						Items: []messaging_api.QuickReplyItem{
							{
								Action: &messaging_api.MessageAction{
									Label: "查詢最近檔案",
									Text:  "/recent_files",
								},
							},
							{
								Action: &messaging_api.MessageAction{
									Label: "中斷連線",
									Text:  "/disconnect_drive",
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

func sendConnectionPrompt(bot *messaging_api.MessagingApiAPI, replyToken string) {
	if _, err := bot.ReplyMessage(
		&messaging_api.ReplyMessageRequest{
			ReplyToken: replyToken,
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

// isGoogleAuthError checks if the error from a Google API call is due to
// an authentication/authorization issue (e.g., expired or revoked token).
func isGoogleAuthError(err error) bool {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		// 401 Unauthorized or 403 Forbidden are strong indicators of a token issue.
		return apiErr.Code == http.StatusUnauthorized || apiErr.Code == http.StatusForbidden
	}

	// The oauth2 library can return an error containing "invalid_grant"
	// when the refresh token is expired, revoked, or otherwise invalid.
	if err != nil {
		errorStr := err.Error()
		// Basic substring check to avoid importing "strings"
		for i := 0; i <= len(errorStr)-13; i++ {
			if errorStr[i:i+13] == "invalid_grant" {
				return true
			}
		}
	}

	return false
}

func sendReconnectionPrompt(bot *messaging_api.MessagingApiAPI, replyToken string) {
	message := "您的 Google Drive 授權似乎已失效。\n請執行 /reconnect 指令來重新連線。"
	if _, err := bot.ReplyMessage(
		&messaging_api.ReplyMessageRequest{
			ReplyToken: replyToken,
			Messages: []messaging_api.MessageInterface{
				&messaging_api.TextMessage{
					Text: message,
					QuickReply: &messaging_api.QuickReply{
						Items: []messaging_api.QuickReplyItem{
							{
								Action: &messaging_api.MessageAction{
									Label: "重新連線",
									Text:  "/reconnect",
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
