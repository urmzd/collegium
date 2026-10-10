// Package authtest is an in-process OpenID Connect issuer for tests. It serves
// discovery and keys and signs whatever claims a test asks for, so the real
// verification path runs against real signatures.
package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Issuer is a fake identity provider.
type Issuer struct {
	URL    string
	key    *rsa.PrivateKey
	signer jose.Signer

	// OnToken, when set, answers the token endpoint. It receives the form
	// the client posted and returns the JSON body to send back.
	OnToken func(form map[string]string) map[string]any
}

// New starts an issuer that stops when the test ends.
func New(t testing.TB) *Issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"),
	)
	if err != nil {
		t.Fatal(err)
	}
	iss := &Issuer{key: key, signer: signer}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	iss.URL = srv.URL

	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                iss.URL,
			"authorization_endpoint":                iss.URL + "/authorize",
			"token_endpoint":                        iss.URL + "/token",
			"jwks_uri":                              iss.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig",
		}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if iss.OnToken == nil {
			http.Error(w, "no token handler", http.StatusInternalServerError)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		form := map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		writeJSON(w, iss.OnToken(form))
	})
	return iss
}

// Sign issues a token for audience aud carrying extra claims. Standard claims
// are filled in and can be overridden through extra.
func (i *Issuer) Sign(t testing.TB, subject, aud string, extra map[string]any) string {
	t.Helper()
	now := time.Now()
	claims := map[string]any{
		"iss": i.URL,
		"sub": subject,
		"aud": aud,
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
	for k, v := range extra {
		claims[k] = v
	}
	raw, err := jwt.Signed(i.signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// SignWith issues a token signed by a key the issuer does not publish.
func (i *Issuer) SignWith(t testing.TB, subject, aud string) string {
	t.Helper()
	other := New(t)
	other.URL = i.URL
	return other.Sign(t, subject, aud, nil)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
