package mockidp_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/auth/mockidp"
)

const (
	audience = "api://collegium"
	redirect = "http://127.0.0.1:9/auth/callback"
)

func config(mut func(*mockidp.Config)) mockidp.Config {
	cfg := mockidp.Config{
		Enabled: true, Addr: "127.0.0.1:0",
		ClientID: mockidp.DevClientID, ClientSecret: mockidp.DevClientSecret,
		RedirectURIs: []string{redirect}, Audience: audience, Tenant: "acme",
		People: mockidp.DefaultPeople(), Machines: mockidp.DefaultMachines(),
		Log: slog.New(slog.DiscardHandler),
	}
	if mut != nil {
		mut(&cfg)
	}
	return cfg
}

func start(t *testing.T, mut func(*mockidp.Config)) *mockidp.Server {
	t.Helper()
	s, err := mockidp.Start(config(mut))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// verifier is the platform's real verifier, configured the way cmd/collegium
// configures it for the mock, with the built-in policy and default groups.
func verifier(t *testing.T, s *mockidp.Server) *auth.Verifier {
	t.Helper()
	v, err := auth.NewVerifier(context.Background(), auth.OIDCConfig{
		Issuer: s.Issuer(), ClientID: mockidp.DevClientID, Audience: audience, TenantClaim: "tenant",
		GroupRoles: map[string]auth.Role{
			"collegium-admins": auth.Admin, "collegium-operators": auth.Operator, "collegium-viewers": auth.Viewer,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func oauthConfig(v *auth.Verifier) *oauth2.Config {
	return &oauth2.Config{
		ClientID: mockidp.DevClientID, ClientSecret: mockidp.DevClientSecret,
		Endpoint: v.Provider().Endpoint(), RedirectURL: redirect,
		Scopes: []string{"openid", "profile", "email", "groups"},
	}
}

var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// pick runs the browser's half of sign-in: open the page, choose a persona,
// and return the code the provider redirects back with.
func pick(t *testing.T, authURL, subject string) (code string) {
	t.Helper()
	res, err := http.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "Development identity provider") {
		t.Fatalf("page %d:\n%s", res.StatusCode, body)
	}
	u, _ := url.Parse(authURL)
	form := u.Query()
	form.Set("persona", subject)
	res, err = noRedirects.PostForm(u.Scheme+"://"+u.Host+u.Path, form)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("pick: status %d", res.StatusCode)
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), redirect+"?") {
		t.Fatalf("redirected to %q", res.Header.Get("Location"))
	}
	if loc.Query().Get("state") != u.Query().Get("state") {
		t.Fatal("state was not returned")
	}
	return loc.Query().Get("code")
}

// signIn runs the authorization code flow with PKCE and a nonce, as
// internal/httpapi does, and verifies the ID token with the real verifier.
func signIn(t *testing.T, v *auth.Verifier, subject string) (auth.Principal, *oauth2.Token) {
	t.Helper()
	oc := oauthConfig(v)
	nonce, pkce := "nonce-"+subject, oauth2.GenerateVerifier()
	code := pick(t, oc.AuthCodeURL("state-1", oauth2.SetAuthURLParam("nonce", nonce), oauth2.S256ChallengeOption(pkce)), subject)
	tok, err := oc.Exchange(context.Background(), code, oauth2.VerifierOption(pkce))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := tok.Extra("id_token").(string)
	p, err := v.IDToken(context.Background(), raw, nonce)
	if err != nil {
		t.Fatal(err)
	}
	return p, tok
}

func TestAuthorizationCodeFlowThroughTheVerifier(t *testing.T) {
	s := start(t, nil)
	v := verifier(t, s)
	p, tok := signIn(t, v, "mock-operator")
	if p.Subject != "mock-operator" || p.Kind != auth.User || p.Tenant != "acme" || p.Name != "Otto Operator" || p.Email != "otto@example.test" {
		t.Fatalf("got %+v", p)
	}
	if !slices.Equal(p.Roles, []auth.Role{auth.Operator}) {
		t.Fatalf("roles %v", p.Roles)
	}
	// The access token is for the API, and is a person's.
	q, err := v.Bearer(context.Background(), tok.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if q.Kind != auth.User || !slices.Equal(q.Roles, p.Roles) {
		t.Fatalf("bearer %+v", q)
	}
}

func TestDefaultPersonasUnderTheDefaultPolicy(t *testing.T) {
	s := start(t, nil)
	v := verifier(t, s)
	viewer := []auth.Permission{auth.AgentsRead, auth.RunsRead, auth.SchedulesRead, auth.AlertsRead, auth.KnowledgeRead, auth.EvalsRead, auth.BillingRead, auth.ArtifactsRead, auth.ClientsRead}
	operator := []auth.Permission{auth.AgentsRead, auth.RunsRead, auth.RunsWrite, auth.SchedulesRead, auth.SchedulesWrite, auth.AlertsRead, auth.AlertsWrite, auth.KnowledgeRead, auth.EvalsRead, auth.EvalsWrite, auth.BillingRead, auth.ArtifactsRead, auth.ArtifactsWrite, auth.ClientsRead}
	for _, tc := range []struct {
		subject string
		want    []auth.Permission
	}{
		{"mock-viewer", viewer},
		{"mock-operator", operator},
		{"mock-admin", auth.Permissions()},
		{"mock-nobody", nil},
	} {
		t.Run(tc.subject, func(t *testing.T) {
			p, _ := signIn(t, v, tc.subject)
			if got := p.Granted(); !slices.Equal(got, tc.want) {
				t.Fatalf("granted %v, want %v", got, tc.want)
			}
		})
	}
	if len(mockidp.DefaultPeople()) != 4 {
		t.Fatal("a default persona has no case here")
	}
}

func TestMachineTokenThroughBearer(t *testing.T) {
	s := start(t, nil)
	v := verifier(t, s)
	cc := clientcredentials.Config{ClientID: "mock-ci", ClientSecret: "mock-ci-secret", TokenURL: v.Provider().Endpoint().TokenURL}
	tok, err := cc.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, err := v.Bearer(context.Background(), tok.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != auth.Machine || p.Subject != "mock-ci" || p.Tenant != "acme" || len(p.Roles) != 0 {
		t.Fatalf("got %+v", p)
	}
	if !slices.Equal(p.Granted(), []auth.Permission{auth.RunsRead, auth.RunsWrite}) {
		t.Fatalf("granted %v", p.Granted())
	}

	cc.Scopes = []string{"collegium:runs:read"}
	tok, err = cc.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p, _ = v.Bearer(context.Background(), tok.AccessToken); !slices.Equal(p.Granted(), []auth.Permission{auth.RunsRead}) {
		t.Fatalf("narrowed: granted %v", p.Granted())
	}

	for name, c := range map[string]clientcredentials.Config{
		"scope it was not given": {ClientID: "mock-ci", ClientSecret: "mock-ci-secret", Scopes: []string{"collegium:agents:write"}},
		"wrong secret":           {ClientID: "mock-ci", ClientSecret: "nope"},
		"web client":             {ClientID: mockidp.DevClientID, ClientSecret: mockidp.DevClientSecret},
	} {
		c.TokenURL = cc.TokenURL
		if _, err := c.Token(context.Background()); err == nil {
			t.Errorf("%s: a token was issued", name)
		}
	}
}

func TestVerifierRejections(t *testing.T) {
	s := start(t, nil)
	v := verifier(t, s)
	ctx := context.Background()

	_, tok := signIn(t, v, "mock-admin")
	idToken, _ := tok.Extra("id_token").(string)
	t.Run("wrong nonce", func(t *testing.T) {
		if _, err := v.IDToken(ctx, idToken, "some-other-nonce"); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("wrong audience: an id token as a bearer", func(t *testing.T) {
		if _, err := v.Bearer(ctx, idToken); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("wrong audience: an access token as an id token", func(t *testing.T) {
		if _, err := v.IDToken(ctx, tok.AccessToken, "nonce-mock-admin"); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		old := start(t, func(c *mockidp.Config) { c.Now = func() time.Time { return time.Now().Add(-3 * time.Hour) } })
		ov := verifier(t, old)
		cc := clientcredentials.Config{ClientID: "mock-ci", ClientSecret: "mock-ci-secret", TokenURL: ov.Provider().Endpoint().TokenURL}
		tok, err := cc.Token(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ov.Bearer(ctx, tok.AccessToken); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("signed by a different key", func(t *testing.T) {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "forged"))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := jwt.Signed(signer).Claims(map[string]any{
			"iss": s.Issuer(), "sub": "mock-admin", "aud": audience, "tenant": "acme",
			"groups": []string{"collegium-admins"}, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		}).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := v.Bearer(ctx, raw); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestCodeExchangeRefusals(t *testing.T) {
	s := start(t, nil)
	v := verifier(t, s)
	oc := oauthConfig(v)
	ctx := context.Background()
	newCode := func(pkce string) string {
		return pick(t, oc.AuthCodeURL("s", oauth2.SetAuthURLParam("nonce", "n"), oauth2.S256ChallengeOption(pkce)), "mock-viewer")
	}

	pkce := oauth2.GenerateVerifier()
	if _, err := oc.Exchange(ctx, newCode(pkce), oauth2.VerifierOption(oauth2.GenerateVerifier())); err == nil {
		t.Error("a code was exchanged with the wrong PKCE verifier")
	}
	code := newCode(pkce)
	if _, err := oc.Exchange(ctx, code, oauth2.VerifierOption(pkce)); err != nil {
		t.Fatal(err)
	}
	if _, err := oc.Exchange(ctx, code, oauth2.VerifierOption(pkce)); err == nil {
		t.Error("a code was exchanged twice")
	}
	wrongSecret := *oc
	wrongSecret.ClientSecret = "nope"
	if _, err := wrongSecret.Exchange(ctx, newCode(pkce), oauth2.VerifierOption(pkce)); err == nil {
		t.Error("a code was exchanged with the wrong client secret")
	}
}

func TestAuthorizeRefusesBadRequests(t *testing.T) {
	s := start(t, nil)
	v := verifier(t, s)
	good := oauthConfig(v).AuthCodeURL("s", oauth2.SetAuthURLParam("nonce", "n"), oauth2.S256ChallengeOption("v"))
	for name, mut := range map[string]func(url.Values){
		"unknown client":          func(q url.Values) { q.Set("client_id", "other") },
		"unregistered redirect":   func(q url.Values) { q.Set("redirect_uri", "http://127.0.0.1:9/elsewhere") },
		"no nonce":                func(q url.Values) { q.Del("nonce") },
		"no pkce":                 func(q url.Values) { q.Del("code_challenge") },
		"plain pkce":              func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"not openid":              func(q url.Values) { q.Set("scope", "profile") },
		"implicit response type":  func(q url.Values) { q.Set("response_type", "token") },
		"no state":                func(q url.Values) { q.Del("state") },
		"persona that is not one": func(q url.Values) { q.Set("persona", "root") },
	} {
		t.Run(name, func(t *testing.T) {
			u, _ := url.Parse(good)
			q := u.Query()
			q.Set("persona", "mock-admin")
			mut(q)
			res, err := noRedirects.PostForm(u.Scheme+"://"+u.Host+u.Path, q)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", res.StatusCode)
			}
		})
	}
}

func TestStartRefusals(t *testing.T) {
	for name, mut := range map[string]func(*mockidp.Config){
		"not enabled":               func(c *mockidp.Config) { c.Enabled = false },
		"all interfaces":            func(c *mockidp.Config) { c.Addr = "0.0.0.0:0" },
		"no host":                   func(c *mockidp.Config) { c.Addr = ":0" },
		"a public name":             func(c *mockidp.Config) { c.Addr = "example.com:0" },
		"ipv6 loopback":             func(c *mockidp.Config) { c.Addr = "[::1]:0" },
		"private address":           func(c *mockidp.Config) { c.Addr = "10.0.0.1:0" },
		"non-loopback redirect":     func(c *mockidp.Config) { c.RedirectURIs = []string{"https://collegium.example.com/auth/callback"} },
		"redirect with credentials": func(c *mockidp.Config) { c.RedirectURIs = []string{"http://u:p@127.0.0.1:9/cb"} },
		"no audience":               func(c *mockidp.Config) { c.Audience = "" },
		"no client secret":          func(c *mockidp.Config) { c.ClientSecret = "" },
		"no personas":               func(c *mockidp.Config) { c.People, c.Machines = nil, nil },
		"duplicate subject": func(c *mockidp.Config) {
			c.People = append(c.People, mockidp.Person{Subject: "mock-admin", Name: "Twin"})
		},
		"persona overrides aud": func(c *mockidp.Config) {
			c.People = []mockidp.Person{{Subject: "x", Claims: map[string]any{"aud": audience}}}
		},
		"persona overrides its tenant claim": func(c *mockidp.Config) {
			c.People = []mockidp.Person{{Subject: "x", Claims: map[string]any{"tenant": "other"}}}
		},
		"machine without a secret": func(c *mockidp.Config) {
			c.Machines = []mockidp.Machine{{ClientID: "m"}}
		},
		"machine reusing the web client id": func(c *mockidp.Config) {
			c.Machines = []mockidp.Machine{{ClientID: mockidp.DevClientID, Secret: "s"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := mockidp.Start(config(mut))
			if err == nil {
				_ = s.Close()
				t.Fatal("started")
			}
		})
	}
	if _, err := mockidp.Start(mockidp.Config{}); !errors.Is(err, mockidp.ErrDisabled) {
		t.Fatalf("zero config: %v", err)
	}
}

func TestIssuerIsLoopbackHTTP(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "localhost:0"} {
		s := start(t, func(c *mockidp.Config) { c.Addr = addr })
		u, err := url.Parse(s.Issuer())
		if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.Port() == "0" {
			t.Fatalf("issuer %q", s.Issuer())
		}
		verifier(t, s) // the verifier accepts it only because it is loopback
	}
}

func TestStartLogsAWarning(t *testing.T) {
	var b strings.Builder
	start(t, func(c *mockidp.Config) { c.Log = slog.New(slog.NewTextHandler(&b, nil)) })
	if !strings.Contains(b.String(), "level=WARN") || !strings.Contains(b.String(), "MOCK IDENTITY PROVIDER") {
		t.Fatalf("log: %s", b.String())
	}
}

func TestPageEscapesPersonas(t *testing.T) {
	s := start(t, func(c *mockidp.Config) {
		c.People = []mockidp.Person{{Subject: "x", Name: `<script>alert(1)</script>`}}
	})
	v := verifier(t, s)
	res, err := http.Get(oauthConfig(v).AuthCodeURL("s", oauth2.SetAuthURLParam("nonce", "n"), oauth2.S256ChallengeOption("v")))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if strings.Contains(string(body), "<script>") {
		t.Fatal("a persona's name was not escaped")
	}
}
