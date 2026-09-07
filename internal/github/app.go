package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

// NewApp loads the PEM private key from path.
func NewApp(id, installationID, keyPath string) (*App, error) {
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	key, err := ParseKey(raw)
	if err != nil {
		return nil, err
	}
	return &App{ID: id, InstallationID: installationID, key: key,
		HTTP: &http.Client{Timeout: 30 * time.Second}}, nil
}

// ParseKey accepts PKCS#1 (what GitHub issues) or PKCS#8.
func ParseKey(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("github: no PEM block in key file")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github: parse key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github: key is not RSA")
	}
	return rk, nil
}

// JWT returns an App JWT valid for about nine minutes.
func (a *App) JWT(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": a.ID,
	})
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// Token is a short-lived installation token.
type Token struct {
	Value     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// InstallationToken mints a token restricted to the given repository names (without owner).
func (a *App) InstallationToken(ctx context.Context, repos ...string) (*Token, error) {
	jwt, err := a.JWT(time.Now())
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if len(repos) > 0 {
		body["repositories"] = repos
	}
	var tok Token
	err = a.call(ctx, http.MethodPost, "/app/installations/"+a.InstallationID+"/access_tokens",
		"Bearer "+jwt, body, &tok)
	return &tok, err
}
