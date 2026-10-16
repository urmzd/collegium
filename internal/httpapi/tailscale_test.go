package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/urmzd/collegium/pkg/auth"
)

const serveOrigin = "https://collegium.example.ts.net"

func tailnetRequest(path, login string) *http.Request {
	r := httptest.NewRequest("GET", serveOrigin+path, nil)
	r.RemoteAddr = "127.0.0.1:12345"
	if login != "" {
		r.Header.Set("Tailscale-User-Login", login)
	}
	return r
}

func TestTailscaleIdentityAndPolicy(t *testing.T) {
	pol, err := auth.ParsePolicy("tailnet", `
role viewer { allow read on * }
role admin { allow * on * }
grant viewer to subject "tailscale:alice@example.com"
grant admin to group "collegium-admins"
grant admin to email domain "example.com"
`)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewTailscaleAuthn("127.0.0.1:8080", serveOrigin, "acme", pol)
	if err != nil {
		t.Fatal(err)
	}
	r := tailnetRequest("/api/me", "alice@example.com")
	r.Header.Set("Tailscale-User-Name", "=?utf-8?q?Alice_B=C3=BCller?=")
	r.Header.Set("Tailscale-User-Groups", "collegium-admins")
	r.Header.Set("X-Tenant", "evil")
	who, err := a.Authenticate(r)
	if err != nil || who.Subject != "tailscale:alice@example.com" || who.Tenant != "acme" || who.Name != "Alice Büller" || !who.Can(auth.RunsRead) || who.Can(auth.RunsWrite) {
		t.Fatalf("identity: %+v, %v", who, err)
	}
	if d := who.Policy().Explain(who, auth.RunsRead); !d.Allowed || d.Policy.Grant == nil {
		t.Fatalf("missing grant explanation: %v", d)
	}
	stranger, err := a.Authenticate(tailnetRequest("/api/me", "stranger@example.com"))
	if err != nil || len(stranger.Granted()) != 0 {
		t.Fatalf("unmapped user: %+v, %v", stranger, err)
	}
}

func TestTailscaleRefusesUntrustedRequests(t *testing.T) {
	a, err := NewTailscaleAuthn("127.0.0.1:8080", serveOrigin, "acme", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*http.Request){
		"missing identity":   func(r *http.Request) { r.Header.Del("Tailscale-User-Login") },
		"remote peer":        func(r *http.Request) { r.RemoteAddr = "100.64.0.1:1234" },
		"forwarded peer":     func(r *http.Request) { r.RemoteAddr = "192.168.1.1:1234"; r.Header.Set("X-Forwarded-For", "127.0.0.1") },
		"malformed peer":     func(r *http.Request) { r.RemoteAddr = "localhost" },
		"wrong host":         func(r *http.Request) { r.Host = "attacker.example" },
		"bearer":             func(r *http.Request) { r.Header.Set("Authorization", "Bearer fake") },
		"duplicate identity": func(r *http.Request) { r.Header.Add("Tailscale-User-Login", "admin@example.com") },
		"combined identity":  func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "alice@example.com,admin@example.com") },
	} {
		t.Run(name, func(t *testing.T) {
			r := tailnetRequest("/api/me", "alice@example.com")
			mutate(r)
			if _, err := a.Authenticate(r); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestTailscaleRoutes(t *testing.T) {
	pol, err := auth.ParsePolicy("tailnet", "role viewer { allow read on * }\ngrant viewer to subject \"tailscale:alice@example.com\"")
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewTailscaleAuthn("127.0.0.1:8080", serveOrigin, "acme", pol)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	s := &Server{P: f.p, Authn: a, Log: slog.New(slog.DiscardHandler)}
	h := s.Handler()
	for _, tc := range []struct {
		path, login string
		status      int
		location    string
	}{
		{"/api/me", "alice@example.com", 200, ""},
		{"/api/me", "", 401, ""},
		{"/api/runs", "stranger@example.com", 403, ""},
		{"/auth/login?return_to=/runs", "alice@example.com", 302, "/runs"},
		{"/auth/login?return_to=//evil.example", "alice@example.com", 302, "/"},
		{"/auth/login", "", 401, ""},
		{"/auth/callback?code=fake", "alice@example.com", 400, ""},
	} {
		t.Run(tc.path+tc.login, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, tailnetRequest(tc.path, tc.login))
			if w.Code != tc.status || w.Header().Get("Location") != tc.location {
				t.Fatalf("status %d location %q: %s", w.Code, w.Header().Get("Location"), w.Body.String())
			}
		})
	}
}

func TestTailscaleConfiguration(t *testing.T) {
	for _, tc := range []struct{ addr, origin, tenant string }{
		{"0.0.0.0:8080", serveOrigin, "acme"},
		{"100.64.0.1:8080", serveOrigin, "acme"},
		{"127.0.0.1:8080", "http://collegium.example.ts.net", "acme"},
		{"127.0.0.1:8080", serveOrigin + "/path", "acme"},
		{"127.0.0.1:8080", serveOrigin, ""},
	} {
		if _, err := NewTailscaleAuthn(tc.addr, tc.origin, tc.tenant, nil); err == nil {
			t.Errorf("accepted %+v", tc)
		}
	}
	if _, err := NewTailscaleAuthn("[::1]:8080", serveOrigin, "acme", nil); err != nil {
		t.Fatal(err)
	}
}
