package web

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Access verifies the JWT that Cloudflare Access adds to every request it lets through
// (Cf-Access-Jwt-Assertion), so a misconfiguration at the edge does not expose the service.
type Access struct {
	Team string // team name: https://<team>.cloudflareaccess.com
	AUD  string // the Access application's audience tag
	HTTP *http.Client

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
	now     func() time.Time
}

func (a *Access) issuer() string { return "https://" + a.Team + ".cloudflareaccess.com" }

// Claims is the subset of the Access JWT the server uses.
type Claims struct {
	Email string `json:"email"`
	Sub   string `json:"sub"`
}

// Verify checks the token's signature, issuer, audience and validity period.
func (a *Access) Verify(ctx context.Context, token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}

	dec := base64.RawURLEncoding

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}

	hb, err := dec.DecodeString(parts[0])
	if err != nil || json.Unmarshal(hb, &header) != nil {
		return nil, errors.New("malformed header")
	}

	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unexpected algorithm %q", header.Alg)
	}

	key, err := a.key(ctx, header.Kid)
	if err != nil {
		return nil, err
	}

	sig, err := dec.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("malformed signature")
	}

	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return nil, errors.New("bad signature")
	}

	pb, err := dec.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("malformed payload")
	}

	var p struct {
		Claims
		Iss string          `json:"iss"`
		Aud json.RawMessage `json:"aud"`
		Exp int64           `json:"exp"`
		Nbf int64           `json:"nbf"`
	}
	if err := json.Unmarshal(pb, &p); err != nil {
		return nil, errors.New("malformed payload")
	}

	now := a.clock().Unix()
	switch {
	case p.Iss != a.issuer():
		return nil, fmt.Errorf("unexpected issuer %q", p.Iss)
	case !hasAudience(p.Aud, a.AUD):
		return nil, errors.New("unexpected audience")
	case p.Exp == 0 || now > p.Exp+30:
		return nil, errors.New("expired")
	case p.Nbf != 0 && now < p.Nbf-30:
		return nil, errors.New("not yet valid")
	}

	return &p.Claims, nil
}

func hasAudience(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}

	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}

	return false
}

func (a *Access) clock() time.Time {
	if a.now != nil {
		return a.now()
	}

	return time.Now()
}

// key returns the signing key for kid, refreshing the certs at most once a minute when unknown,
// and every hour otherwise (Cloudflare rotates them).
func (a *Access) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	k, ok := a.keys[kid]

	age := a.clock().Sub(a.fetched)
	if ok && age < time.Hour {
		return k, nil
	}

	if !ok && age < time.Minute && a.keys != nil {
		return nil, errors.New("unknown key")
	}

	if err := a.fetch(ctx); err != nil {
		if ok {
			return k, nil
		}

		return nil, err
	}

	if k, ok = a.keys[kid]; !ok {
		return nil, errors.New("unknown key")
	}

	return k, nil
}

func (a *Access) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.issuer()+"/cdn-cgi/access/certs", nil)
	if err != nil {
		return err
	}

	hc := a.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := hc.Do(req)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("access certs: HTTP %d", resp.StatusCode)
	}

	var body struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return err
	}

	keys := map[string]*rsa.PublicKey{}

	for _, k := range body.Keys {
		if k.Kty != "RSA" {
			continue
		}

		n, err1 := base64.RawURLEncoding.DecodeString(k.N)

		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}

		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}

	if len(keys) == 0 {
		return errors.New("access certs: no RSA keys")
	}

	a.keys, a.fetched = keys, a.clock()

	return nil
}
