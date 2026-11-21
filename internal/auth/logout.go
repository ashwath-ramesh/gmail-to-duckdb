package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const googleRevokeURL = "https://oauth2.googleapis.com/revoke"

// ErrNotSignedIn means that no token file exists for the database.
var ErrNotSignedIn = errors.New("not signed in: no token file")

// Logout revokes the saved Google grant, then removes the token file.
// revoked is false when Google said that the token was already invalid; the
// file is removed in that case too. On any other failure the file stays, so
// the user can try again.
func Logout(ctx context.Context, tokenPath string) (revoked bool, err error) {
	return logout(ctx, tokenPath, googleRevokeURL, &http.Client{Timeout: tokenRequestTimeout})
}

func logout(ctx context.Context, tokenPath, revokeURL string, hc *http.Client) (bool, error) {
	if _, err := os.Lstat(tokenPath); os.IsNotExist(err) {
		return false, ErrNotSignedIn
	}
	tok, _, err := loadToken(tokenPath)
	if errors.Is(err, errNeedLogin) {
		// The file holds no usable token, so there is nothing to revoke.
		return false, removeToken(tokenPath)
	}
	if err != nil {
		return false, err
	}
	secret := tok.RefreshToken
	if secret == "" {
		secret = tok.AccessToken
	}
	revoked, err := revoke(ctx, hc, revokeURL, secret)
	if err != nil {
		return false, err
	}
	return revoked, removeToken(tokenPath)
}

// revoke asks Google to end the grant. The token goes in the POST body, not
// the URL, so that it does not appear in logs.
func revoke(ctx context.Context, hc *http.Client, revokeURL, token string) (bool, error) {
	form := url.Values{"token": {token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return false, fmt.Errorf("revoke Google access: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	var reply struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &reply)
	if resp.StatusCode == http.StatusBadRequest && reply.Error == "invalid_token" {
		return false, nil
	}
	if reply.Error != "" {
		return false, fmt.Errorf("revoke Google access: http %d: %s", resp.StatusCode, reply.Error)
	}
	return false, fmt.Errorf("revoke Google access: http %d", resp.StatusCode)
}

func removeToken(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove token file: %w", err)
	}
	return nil
}
