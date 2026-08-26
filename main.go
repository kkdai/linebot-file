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

// Command linebot-file is a LINE bot that backs up chat media to the sender's
// own Google Drive.
package main

import (
	"cmp"
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
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
	if googleOauthConfig.ClientID == "" || googleOauthConfig.ClientSecret == "" {
		log.Fatal("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET environment variables must be set.")
	}

	channelSecret = os.Getenv("ChannelSecret")
	accessToken := os.Getenv("ChannelAccessToken")
	if channelSecret == "" || accessToken == "" {
		log.Fatal("ChannelSecret and ChannelAccessToken environment variables must be set.")
	}

	richMenuConnect = cmp.Or(os.Getenv("RICH_MENU_CONNECT"), defaultRichMenuConnect)
	richMenuMain = cmp.Or(os.Getenv("RICH_MENU_MAIN"), defaultRichMenuMain)

	// The SDK defaults both clients to http.DefaultClient, which has no
	// timeout, and its generated methods take no context — so a stalled LINE
	// endpoint would block the handler indefinitely. Inject clients that time
	// out instead. Do not reach for the SDK's WithContext here: it mutates the
	// shared client, so concurrent handlers would race on it.
	if bot, err = messaging_api.NewMessagingApiAPI(accessToken,
		messaging_api.WithHTTPClient(&http.Client{Timeout: lineAPITimeout}),
	); err != nil {
		log.Fatalf("Failed to create LINE messaging client: %v", err)
	}
	if blob, err = messaging_api.NewMessagingApiBlobAPI(accessToken,
		messaging_api.WithBlobHTTPClient(&http.Client{Timeout: lineBlobTimeout}),
	); err != nil {
		log.Fatalf("Failed to create LINE blob client: %v", err)
	}

	mux := http.NewServeMux()
	// "/{$}" matches the root path exactly rather than every path below it.
	mux.HandleFunc("POST /{$}", webhookHandler)
	mux.HandleFunc("GET /oauth/callback", oauthCallbackHandler)
	// Not "/healthz": Cloud Run's frontend reserves that exact path and
	// answers it with its own 404, so the request never reaches us.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	port := cmp.Or(os.Getenv("PORT"), "8080")
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("Listening on http://localhost:%s/", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
