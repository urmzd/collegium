package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/auth"
	githubflow "github.com/urmzd/collegium/pkg/integrations/github"
	slackflow "github.com/urmzd/collegium/pkg/integrations/slack"
	"github.com/urmzd/mandatum/pkg/agentspec"
	"github.com/urmzd/mandatum/pkg/registry"
	"github.com/urmzd/mandatum/pkg/signature"
)

func TestSignedIntegrationsRunThroughThePlatformAndAppearInItsConsoleAPI(t *testing.T) {
	for _, surface := range []string{"github", "slack"} {
		t.Run(surface, func(t *testing.T) {
			const secret = "collegium-integration-secret"
			var peer platform.Peer
			var replies func() int
			var body, path string
			if surface == "github" {
				api := githubflow.NewFake()
				c, err := githubflow.New(githubflow.Config{API: api, Verifier: signature.NewGitHub(secret), Owners: []string{"acme"}, Bots: map[string]string{"helper": "helper"}, Interval: -1})
				if err != nil {
					t.Fatal(err)
				}
				peer = c
				replies = func() int { return len(api.Comments()) }
				path = "/connectors/github"
				body = `{"tenant":"evil","action":"created","repository":{"name":"repo","owner":{"login":"acme"}},"sender":{"login":"human","type":"User"},"issue":{"number":7},"comment":{"id":91,"body":"@helper explain"}}`
			} else {
				api := slackflow.NewFake()
				c, err := slackflow.New(slackflow.Config{API: api, Verifier: signature.NewSlack(secret), Team: "T01", DefaultAgent: "helper", Interval: -1, Sessions: true})
				if err != nil {
					t.Fatal(err)
				}
				peer = c
				replies = func() int { return len(api.Messages()) }
				path = "/connectors/slack"
				body = `{"tenant":"evil","type":"event_callback","event_id":"slack-1","team_id":"T01","event":{"type":"app_mention","user":"U01","channel":"C01","ts":"1700000000.000100","text":"explain"}}`
			}
			p, err := platform.New(platform.Config{Workspace: t.TempDir(), Connections: func(registry.Store) ([]platform.Connection, error) {
				return []platform.Connection{{Tenant: "acme", Peer: peer}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Agents.Create(context.Background(), "acme", agentspec.AgentSpec{Name: "helper", Provider: "offline", Model: "offline", SystemPrompt: "Answer questions."}, "test"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			done := make(chan error, 1)
			go func() { done <- p.Run(ctx) }()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			wait := func(ready func() bool) {
				t.Helper()
				for !ready() {
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(time.Millisecond):
					}
				}
			}
			wait(peer.Ingesting)
			s := &Server{P: p, Authn: &Authn{Dev: &auth.Principal{Subject: "test", Tenant: "acme", Kind: auth.User, Roles: []auth.Role{auth.Admin}}}}
			h := s.Handler()
			for _, path := range []string{"/api/me", "/api/runs", "/auth/login", "/"} {
				w := httptest.NewRecorder()
				p.IngressHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
				if w.Code != http.StatusNotFound {
					t.Fatalf("forwarded ingress exposes %s: %d", path, w.Code)
				}
			}
			// Even a dev-auth console must reject unsigned external webhooks.
			bad := httptest.NewRecorder()
			h.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
			if bad.Code < 400 {
				t.Fatalf("unsigned request accepted: %d", bad.Code)
			}
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			mac := hmac.New(sha256.New, []byte(secret))
			if surface == "github" {
				mac.Write([]byte(body))
				r.Header.Set(signature.HeaderGitHubSignature, "sha256="+hex.EncodeToString(mac.Sum(nil)))
				r.Header.Set("X-GitHub-Event", "issue_comment")
				r.Header.Set("X-GitHub-Delivery", "github-1")
			} else {
				ts := strconv.FormatInt(time.Now().Unix(), 10)
				mac.Write([]byte("v0:" + ts + ":" + body))
				r.Header.Set(signature.HeaderSlackTimestamp, ts)
				r.Header.Set(signature.HeaderSlackSignature, "v0="+hex.EncodeToString(mac.Sum(nil)))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("webhook: %d %s", w.Code, w.Body.String())
			}
			wait(func() bool { return replies() == 1 })
			w = httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/runs", nil))
			if w.Code != 200 || !strings.Contains(w.Body.String(), "helper") {
				t.Fatalf("console API: %d %s", w.Code, w.Body.String())
			}
			s.Authn = &Authn{Dev: &auth.Principal{Subject: "other", Tenant: "evil", Kind: auth.User, Roles: []auth.Role{auth.Admin}}}
			w = httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/runs", nil))
			if strings.Contains(w.Body.String(), "helper") {
				t.Fatal("external payload selected the tenant")
			}
		})
	}
}
