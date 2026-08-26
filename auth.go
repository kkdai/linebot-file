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
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// generateState returns a random, URL-safe CSRF state token.
func generateState() string {
	return rand.Text()
}

// authCodeURL records a fresh OAuth state for userID and returns the Google
// consent URL to send them to.
func authCodeURL(ctx context.Context, userID string) (string, error) {
	state := generateState()
	_, err := firestoreClient.Collection(stateCollection).Doc(state).Set(ctx, map[string]any{
		"user_id":    userID,
		"created_at": time.Now(),
	})
	if err != nil {
		return "", fmt.Errorf("failed to save oauth state: %w", err)
	}
	return googleOauthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce), nil
}

// oauthCallbackHandler completes the OAuth flow Google redirects back into.
func oauthCallbackHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	state := r.FormValue("state")
	code := r.FormValue("code")
	if state == "" || code == "" {
		http.Error(w, "Missing state or code parameter.", http.StatusBadRequest)
		return
	}

	// 1. Validate state and recover the LINE user it belongs to.
	doc, err := firestoreClient.Collection(stateCollection).Doc(state).Get(ctx)
	if err != nil {
		log.Printf("Invalid oauth state: %v", err)
		http.Error(w, "Invalid state parameter. Please try again.", http.StatusBadRequest)
		return
	}
	// Consume the state so it cannot be replayed. The request context may
	// already be done by the time this runs, so detach it.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := doc.Ref.Delete(cleanupCtx); err != nil {
			log.Printf("Failed to delete used oauth state: %v", err)
		}
	}()

	var stateData struct {
		UserID    string    `firestore:"user_id"`
		CreatedAt time.Time `firestore:"created_at"`
	}
	if err := doc.DataTo(&stateData); err != nil {
		log.Printf("Failed to parse state data: %v", err)
		http.Error(w, "Internal server error.", http.StatusInternalServerError)
		return
	}

	if time.Since(stateData.CreatedAt) > stateTTL {
		log.Printf("Expired oauth state for user %s", stateData.UserID)
		http.Error(w, "授權連結已過期，請回到 LINE 重新執行 "+cmdConnect+"。", http.StatusBadRequest)
		return
	}
	userID := stateData.UserID

	// 2. Exchange the authorization code for a token.
	token, err := googleOauthConfig.Exchange(ctx, code)
	if err != nil {
		log.Printf("Failed to exchange token: %v", err)
		http.Error(w, "Failed to exchange token.", http.StatusInternalServerError)
		return
	}

	// 3. Store the token under the LINE user ID.
	if err := saveToken(ctx, userID, token); err != nil {
		log.Printf("Failed to save token to firestore: %v", err)
		http.Error(w, "Failed to save token.", http.StatusInternalServerError)
		return
	}

	// 4. Switch the user over to the connected rich menu.
	linkRichMenu(userID, richMenuMain)

	log.Printf("Successfully saved token for user %s", userID)
	fmt.Fprint(w, "授權成功！您現在可以回到 LINE 傳送檔案了。")
}

func saveToken(ctx context.Context, userID string, token *oauth2.Token) error {
	_, err := firestoreClient.Collection(tokenCollection).Doc(userID).Set(ctx, token)
	return err
}

// loadToken reads a user's stored token, reporting ErrOauth2TokenNotFound
// when they have not connected their Drive account.
func loadToken(ctx context.Context, userID string) (*oauth2.Token, error) {
	doc, err := firestoreClient.Collection(tokenCollection).Doc(userID).Get(ctx)
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
	return &token, nil
}

// persistingTokenSource writes refreshed access tokens back to Firestore.
// Without it every request re-refreshes the token against Google, because the
// refreshed value is discarded when the request ends.
type persistingTokenSource struct {
	ctx          context.Context
	src          oauth2.TokenSource
	userID       string
	lastAccess   string
	refreshToken string
}

func (s *persistingTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	if token.AccessToken == s.lastAccess {
		return token, nil
	}
	s.lastAccess = token.AccessToken

	// A refresh response does not always repeat the refresh token; keep the
	// one we already have so the user does not have to reauthorize.
	stored := *token
	if stored.RefreshToken == "" {
		stored.RefreshToken = s.refreshToken
	}

	// Persisting is best effort: failing only costs one extra refresh later.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 5*time.Second)
	defer cancel()
	if err := saveToken(writeCtx, s.userID, &stored); err != nil {
		log.Printf("Failed to persist refreshed token for user %s: %v", s.userID, err)
	}
	return token, nil
}

// getGoogleDriveService builds a Drive client acting on behalf of userID.
func getGoogleDriveService(ctx context.Context, userID string) (*drive.Service, error) {
	token, err := loadToken(ctx, userID)
	if err != nil {
		return nil, err
	}

	src := &persistingTokenSource{
		ctx:          ctx,
		src:          googleOauthConfig.TokenSource(ctx, token),
		userID:       userID,
		lastAccess:   token.AccessToken,
		refreshToken: token.RefreshToken,
	}
	return drive.NewService(ctx, option.WithTokenSource(oauth2.ReuseTokenSource(token, src)))
}

// revokeGoogleToken revokes the user's grant at Google and forgets the token.
func revokeGoogleToken(ctx context.Context, userID string) error {
	docRef := firestoreClient.Collection(tokenCollection).Doc(userID)
	token, err := loadToken(ctx, userID)
	if err != nil {
		return err
	}

	// Revoking the refresh token also invalidates every access token derived
	// from it, so prefer it when we have one.
	tokenToRevoke := token.AccessToken
	if token.RefreshToken != "" {
		tokenToRevoke = token.RefreshToken
	}

	if err := revokeAtGoogle(ctx, userID, tokenToRevoke); err != nil {
		// Log and still drop our copy: leaving it behind would let the bot
		// keep trying to use a grant the user asked us to forget.
		log.Printf("Revocation request failed for user %s: %v", userID, err)
	}

	if _, err := docRef.Delete(ctx); err != nil {
		log.Printf("CRITICAL: Failed to delete token for user %s: %v", userID, err)
		return fmt.Errorf("failed to delete token from firestore: %w", err)
	}

	linkRichMenu(userID, richMenuConnect)
	log.Printf("Successfully revoked and/or deleted token for user %s", userID)
	return nil
}

func revokeAtGoogle(ctx context.Context, userID, token string) error {
	endpoint := "https://oauth2.googleapis.com/revoke?token=" + url.QueryEscape(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to build revocation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send revocation request to google: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return fmt.Errorf("google returned status %d for user %s: %s", resp.StatusCode, userID, body)
	}
	return nil
}

// isGoogleAuthError reports whether err means the user's grant is no longer
// usable, so we should ask them to reconnect rather than retry.
func isGoogleAuthError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code == http.StatusUnauthorized || apiErr.Code == http.StatusForbidden
	}

	// The oauth2 package surfaces a refresh token that has expired or been
	// revoked as an "invalid_grant" error.
	return strings.Contains(err.Error(), "invalid_grant")
}
