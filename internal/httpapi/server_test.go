package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/auth/authtest"
	"github.com/urmzd/collegium/pkg/call"
)

const (
	clientID = "web-client"
	audience = "api://collegium"
)

type fixture struct {
	srv *httptest.Server
	iss *authtest.Issuer
	p   *platform.Platform
}

// newFixture runs a platform behind real OIDC verification against an
// in-process issuer. Two tenants exist, selected by the "org" claim.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, platform.Config{})
}

// newFixtureWith is newFixture over a platform a test has configured further.
func newFixtureWith(t *testing.T, cfg platform.Config) *fixture {
	t.Helper()
	cfg.Workspace, cfg.Limits = t.TempDir(), call.Limits{MaxDepth: 2, MaxFanOut: 4}
	p, err := platform.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	iss := authtest.New(t)
	v, err := auth.NewVerifier(ctx, auth.OIDCConfig{
		Issuer: iss.URL, ClientID: clientID, Audience: audience, TenantClaim: "org",
		GroupRoles: map[string]auth.Role{"collegium-admins": auth.Admin, "collegium-viewers": auth.Viewer},
	})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	authn, err := NewAuthn(v, clientID, "secret", srv.URL, []string{"openid", "groups"})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", (&Server{P: p, Authn: authn, Log: slog.New(slog.DiscardHandler)}).Handler())
	t.Cleanup(func() {
		srv.Close()
		cancel()
		if err := <-done; err != nil {
			t.Errorf("platform shutdown: %v", err)
		}
	})
	return &fixture{srv: srv, iss: iss, p: p}
}

func (f *fixture) machine(t *testing.T, org string, scopes ...string) string {
	t.Helper()
	return f.iss.Sign(t, "svc-"+org, audience, map[string]any{"org": org, "scp": scopes})
}

func (f *fixture) do(t *testing.T, c *http.Client, method, path, bearer, body string, hdr ...string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if c == nil {
		c = http.DefaultClient
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res, out
}

const helperSpec = `{"name":"helper","model":"offline","provider":"offline","system_prompt":"Help."}`

func TestUnauthenticatedIsRefused(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/api/me", "/api/agents", "/api/runs", "/api/schedules", "/api/events"} {
		if res, _ := f.do(t, nil, "GET", path, "", ""); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, res.StatusCode)
		}
	}
	if res, _ := f.do(t, nil, "GET", "/api/me", "not-a-token", ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("garbage bearer = %d, want 401", res.StatusCode)
	}
}

func TestMachineIsHeldToItsScopes(t *testing.T) {
	f := newFixture(t)
	reader := f.machine(t, "acme", "collegium:agents:read", "collegium:runs:read")

	if res, _ := f.do(t, nil, "GET", "/api/agents", reader, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("read = %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "POST", "/api/agents", reader, helperSpec); res.StatusCode != http.StatusForbidden {
		t.Fatalf("write with a read scope = %d, want 403", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "POST", "/api/runs", reader, `{"agent":"helper","text":"hi"}`); res.StatusCode != http.StatusForbidden {
		t.Fatalf("run with a read scope = %d, want 403", res.StatusCode)
	}
}

func TestTenantsAreIsolated(t *testing.T) {
	f := newFixture(t)
	all := []string{"collegium:agents:read", "collegium:agents:write", "collegium:runs:read", "collegium:runs:write", "collegium:schedules:read", "collegium:schedules:write"}
	acme, globex := f.machine(t, "acme", all...), f.machine(t, "globex", all...)

	if res, _ := f.do(t, nil, "POST", "/api/agents", acme, helperSpec); res.StatusCode != http.StatusCreated {
		t.Fatalf("create agent = %d", res.StatusCode)
	}
	res, run := f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"hi"}`)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("create run = %d %v", res.StatusCode, run)
	}
	if run["tenant"] != "acme" {
		t.Fatalf("run tenant = %v", run["tenant"])
	}
	id := run["id"].(string)
	if res, _ := f.do(t, nil, "POST", "/api/schedules", acme, `{"name":"nightly","expression":"0 2 * * *","agent":"helper","text":"report"}`); res.StatusCode != http.StatusCreated {
		t.Fatalf("create schedule = %d", res.StatusCode)
	}

	// The other tenant sees none of it, and cannot name its way in.
	if _, out := f.do(t, nil, "GET", "/api/agents", globex, ""); len(out["agents"].([]any)) != 0 {
		t.Errorf("other tenant listed agents: %v", out)
	}
	if _, out := f.do(t, nil, "GET", "/api/runs", globex, ""); len(out["runs"].([]any)) != 0 {
		t.Errorf("other tenant listed runs: %v", out)
	}
	if _, out := f.do(t, nil, "GET", "/api/schedules", globex, ""); len(out["schedules"].([]any)) != 0 {
		t.Errorf("other tenant listed schedules: %v", out)
	}
	for _, path := range []string{"/api/runs/" + id, "/api/runs/" + id + "/tree"} {
		if res, _ := f.do(t, nil, "GET", path, globex, ""); res.StatusCode != http.StatusNotFound {
			t.Errorf("other tenant GET %s = %d, want 404", path, res.StatusCode)
		}
	}
	if res, _ := f.do(t, nil, "DELETE", "/api/schedules/nightly", globex, ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("other tenant deleted the schedule: %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "POST", "/api/runs", globex, `{"agent":"helper","text":"hi"}`); res.StatusCode != http.StatusNotFound {
		t.Errorf("other tenant ran the agent: %d", res.StatusCode)
	}
	// A body cannot choose a tenant either.
	if res, _ := f.do(t, nil, "POST", "/api/runs", globex, `{"agent":"helper","text":"hi","tenant":"acme"}`); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a tenant field in the body = %d, want 400", res.StatusCode)
	}
}

func TestEventsStreamARun(t *testing.T) {
	f := newFixture(t)
	tok := f.machine(t, "acme", "collegium:agents:write", "collegium:runs:read", "collegium:runs:write")
	f.do(t, nil, "POST", "/api/agents", tok, helperSpec)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.srv.URL+"/api/events?topic=agent:helper", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}

	f.do(t, nil, "POST", "/api/runs", tok, `{"agent":"helper","text":"hi"}`)

	var kinds []string
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e struct {
			Kind   string `json:"kind"`
			Tenant string `json:"tenant"`
		}
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			t.Fatalf("bad event %q: %v", data, err)
		}
		if e.Tenant != "acme" {
			t.Fatalf("stream carried tenant %q", e.Tenant)
		}
		kinds = append(kinds, e.Kind)
		if e.Kind == "lifecycle.completed" {
			break
		}
	}
	if len(kinds) == 0 || kinds[len(kinds)-1] != "lifecycle.completed" {
		t.Fatalf("stream did not reach completion: %v", kinds)
	}
}

// signIn drives the whole browser flow against the fake issuer and returns a
// client holding the session cookie.
func (f *fixture) signIn(t *testing.T, groups ...string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	res, err := c.Get(f.srv.URL + "/auth/login?return_to=/runs")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	to, err := url.Parse(res.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(to.String(), f.iss.URL+"/authorize") {
		t.Fatalf("login redirected to %q", res.Header.Get("Location"))
	}
	q := to.Query()
	if q.Get("prompt") != "" || q.Get("max_age") != "" {
		t.Fatalf("SSO must allow the provider's existing session: %v", q)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("authorization request is missing PKCE, nonce or state: %v", q)
	}

	f.iss.OnToken = func(form map[string]string) map[string]any {
		if form["code"] != "the-code" || form["code_verifier"] == "" {
			return map[string]any{"error": "invalid_grant"}
		}
		return map[string]any{
			"access_token": "opaque", "token_type": "Bearer",
			"id_token": f.iss.Sign(t, "00u-ada", clientID, map[string]any{
				"nonce": q.Get("nonce"), "name": "Ada", "org": "acme", "groups": groups,
			}),
		}
	}
	res, err = c.Get(f.srv.URL + "/auth/callback?code=the-code&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/runs" {
		t.Fatalf("callback = %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	return c
}

func TestBrowserSignIn(t *testing.T) {
	f := newFixture(t)
	c := f.signIn(t, "collegium-viewers")

	res, me := f.do(t, c, "GET", "/api/me", "", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me = %d", res.StatusCode)
	}
	who := me["principal"].(map[string]any)
	if who["subject"] != "00u-ada" || who["tenant"] != "acme" || who["kind"] != "user" {
		t.Fatalf("principal = %v", who)
	}
	// A viewer reads and cannot write.
	if res, _ := f.do(t, c, "POST", "/api/agents", "", helperSpec); res.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer created an agent: %d", res.StatusCode)
	}

	u, _ := url.Parse(f.srv.URL)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == "collegium_session" && len(ck.Value) < 40 {
			t.Fatalf("session id is too short to be unguessable: %d chars", len(ck.Value))
		}
	}

	if res, _ := f.do(t, c, "POST", "/auth/logout", "", ""); res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d", res.StatusCode)
	}
	if res, _ := f.do(t, c, "GET", "/api/me", "", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session survived logout: %d", res.StatusCode)
	}
}

func TestSessionCookieIsHTTPOnly(t *testing.T) {
	f := newFixture(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(f.srv.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	for _, ck := range res.Cookies() {
		if !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode || ck.Path != "/" {
			t.Errorf("cookie %s: HttpOnly=%v SameSite=%v Path=%q", ck.Name, ck.HttpOnly, ck.SameSite, ck.Path)
		}
	}
}

func TestCallbackRejectsAForeignState(t *testing.T) {
	f := newFixture(t)
	// A callback with no matching login cookie is a login started elsewhere.
	res, err := http.Get(f.srv.URL + "/auth/callback?code=x&state=attacker")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", res.StatusCode)
	}
}

func TestCrossOriginWritesAreRefused(t *testing.T) {
	f := newFixture(t)
	c := f.signIn(t, "collegium-admins")

	res, _ := f.do(t, c, "POST", "/api/agents", "", helperSpec, "Sec-Fetch-Site", "cross-site", "Origin", "https://evil.test")
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site write = %d, want 403", res.StatusCode)
	}
	res, _ = f.do(t, c, "POST", "/api/agents", "", helperSpec, "Sec-Fetch-Site", "same-origin")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("same-origin write = %d, want 201", res.StatusCode)
	}
}

func TestSafeReturn(t *testing.T) {
	for in, want := range map[string]string{
		"/runs":             "/runs",
		"/runs?x=1":         "/runs?x=1",
		"":                  "/",
		"https://evil.test": "/",
		"//evil.test":       "/",
		"/\\evil.test":      "/",
		"runs":              "/",
	} {
		if got := safeReturn(in); got != want {
			t.Errorf("safeReturn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestErrorsDoNotLeak(t *testing.T) {
	f := newFixture(t)
	tok := f.machine(t, "acme", "collegium:runs:write")
	res, out := f.do(t, nil, "POST", "/api/runs", tok, `{"agent":"ghost","text":"hi"}`)
	if res.StatusCode != http.StatusNotFound || out["error"] != "not found" {
		t.Fatalf("got %d %v", res.StatusCode, out)
	}
	res, _ = f.do(t, nil, "POST", "/api/runs", tok, strings.Repeat("x", maxBody+10))
	if res.StatusCode != http.StatusRequestEntityTooLarge && res.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized body = %d", res.StatusCode)
	}
	_, _ = io.Copy(io.Discard, res.Body)
}
