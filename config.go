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
	"errors"
	"net/http"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
	"golang.org/x/oauth2"
)

const (
	// Firestore collections.
	stateCollection = "oauth_states"
	tokenCollection = "user_tokens"

	// Rich menu IDs used when RICH_MENU_CONNECT / RICH_MENU_MAIN are unset.
	defaultRichMenuConnect = "richmenu-8360de4ffc27c9eba7849980675ae7f3"
	defaultRichMenuMain    = "richmenu-94cf1a33f7ddd92e65d40d5964070806"

	// rootFolderName is the Drive folder every upload is filed under; each
	// upload then lands in a YYYY-MM subfolder of it.
	rootFolderName = "LINE Bot Uploads"
	folderMimeType = "application/vnd.google-apps.folder"

	// stateTTL bounds how long an OAuth state stays usable. Without it the
	// CSRF window would be unbounded.
	stateTTL = 10 * time.Minute

	// webhookTimeout caps the work done for one webhook delivery. LINE gives
	// us a limited window before it considers the delivery failed.
	webhookTimeout = 55 * time.Second

	// revokeTimeout bounds the direct call to Google's revocation endpoint.
	revokeTimeout = 10 * time.Second

	// lineAPITimeout bounds the small LINE Messaging API calls: replies,
	// pushes and rich menu links. The SDK's methods take no context, so a
	// timeout on their HTTP client is the only thing that can unblock them.
	lineAPITimeout = 10 * time.Second

	// lineBlobTimeout bounds downloading a message's content. Videos and
	// large files need far longer than the plain API calls.
	lineBlobTimeout = 5 * time.Minute

	// eventTTL is how long a handled webhook event ID is remembered, so a
	// LINE redelivery does not upload the same file twice.
	eventTTL = 10 * time.Minute

	recentFileCount  = 5
	maxSearchResults = 10

	// maxDrivePageSize is the largest page the Drive API will return. Used
	// when paging through results we need in full.
	maxDrivePageSize = 1000
)

// ErrOauth2TokenNotFound means the user has never connected their Drive
// account, or has since disconnected it.
var ErrOauth2TokenNotFound = errors.New("oauth2 token not found")

var (
	googleOauthConfig *oauth2.Config
	firestoreClient   *firestore.Client
	bot               *messaging_api.MessagingApiAPI
	blob              *messaging_api.MessagingApiBlobAPI
	channelSecret     string
	richMenuConnect   string
	richMenuMain      string

	// httpClient is used for the direct calls to Google that do not go
	// through a generated client. The default client has no timeout.
	httpClient = &http.Client{Timeout: revokeTimeout}
)
