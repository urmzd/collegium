package main

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

	"github.com/urmzd/mandatum/pkg/agentspec"
	"github.com/urmzd/mandatum/pkg/signature"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/clients"
	"github.com/urmzd/collegium/pkg/integrations/github"
	"github.com/urmzd/collegium/pkg/integrations/slack"
)

func envOf(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

// With nothing set there are no clients, so nothing is exposed.
func TestNoEnvironmentConfiguresNoClient(t *testing.T) {
	got, err := defaultClients(envOf(nil))
	if err != nil || len(got) != 0 {
		t.Fatalf("%d clients, %v", len(got), err)
	}
	p, err := platform.New(platform.Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := seedClients(context.Background(), p, "default", envOf(nil)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/connectors/slack", "/connectors/slack/commands", "/connectors/github"} {
		w := httptest.NewRecorder()
		p.IngressHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s with no clients = %d, want 404", path, w.Code)
		}
	}
}

// A half-configured or malformed environment stops the server from starting,
// as it did when the environment was the only configuration.
func TestAMalformedEnvironmentIsAnError(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"a slack token with no secret":  {EnvSlackToken: "t"},
		"slack with no team":            {EnvSlackToken: "t", EnvSlackSecret: "s"},
		"slack sessions not a boolean":  {EnvSlackToken: "t", EnvSlackSecret: "s", EnvSlackTeam: "T01", EnvSlackSessions: "perhaps"},
		"slack commands not json":       {EnvSlackToken: "t", EnvSlackSecret: "s", EnvSlackTeam: "T01", EnvSlackCommands: "create-pr"},
		"a github secret with no token": {EnvGitHubSecret: "s"},
		"github with no owners":         {EnvGitHubToken: "t", EnvGitHubSecret: "s"},
		"github labels not json":        {EnvGitHubToken: "t", EnvGitHubSecret: "s", EnvGitHubOwners: "acme", EnvGitHubAutoLabels: "{"},
	} {
		if _, err := defaultClients(envOf(vars)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The variables that configured the one installation per surface now yield
// exactly one default client per surface, which works as that installation
// did: any agent in the tenant, the same options, the secrets left where they
// were.
func TestTheLegacyEnvironmentYieldsOneWorkingDefaultClientPerSurface(t *testing.T) {
	vars := map[string]string{
		EnvSlackToken: "xoxb-legacy", EnvSlackSecret: "legacy-slack-secret", EnvSlackTeam: "T01ABCDEF",
		EnvSlackDefault: "helper", EnvSlackSessions: "true", EnvSlackBots: "U08BOT=helper",
		EnvSlackCommands: `[{"name":"create-pr","description":"Open a pull request","argument_hint":"[title]"}]`,
		EnvGitHubToken:   "ghs-legacy", EnvGitHubSecret: "legacy-github-secret-of-16-bytes", EnvGitHubOwners: "acme, acme-labs",
		EnvGitHubBots: "helper[bot]=helper", EnvGitHubChecks: "false", EnvGitHubReadRepositories: "acme/handbook",
		EnvGitHubAutoLabels: `{"acme/handbook":{"agent:auto":"helper"}}`,
	}
	defaults, err := defaultClients(envOf(vars))
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults) != 2 {
		t.Fatalf("%d default clients, want one per surface", len(defaults))
	}

	refs, err := secretRefs("", "trial", envOf(vars))
	if err != nil {
		t.Fatal(err)
	}
	slackFake, githubFake := slack.NewFake(), github.NewFake()
	tokens := map[string]bool{}
	p, err := platform.New(platform.Config{
		Workspace: t.TempDir(), ClientInterval: -1,
		Env:        func(name string) (string, bool) { v, ok := vars[name]; return v, ok },
		SecretRefs: refs,
		SlackAPI:   func(token string) slack.API { tokens[token] = true; return slackFake },
		GitHubAPI:  func(token string) github.API { tokens[token] = true; return githubFake },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("platform shutdown: %v", err)
		}
	})
	if _, err := p.Agents.Create(ctx, "trial", agentspec.AgentSpec{Name: "helper", Provider: "offline", Model: "offline", SystemPrompt: "Answer."}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := seedClients(ctx, p, "trial", envOf(vars)); err != nil {
		t.Fatal(err)
	}
	if !tokens["xoxb-legacy"] || !tokens["ghs-legacy"] {
		t.Fatalf("the connectors were built with %v", tokens)
	}

	all := p.Clients.List("trial")
	if len(all) != 2 || len(p.Clients.List("default")) != 0 {
		t.Fatalf("clients = %+v", all)
	}
	for _, c := range all {
		if c.Managed != clients.ManagedEnv || !c.Enabled || len(c.Agents) != 1 || c.Agents[0] != clients.AnyAgent {
			t.Errorf("%s = %+v, want an enabled, env-managed client for any agent", c.ID, c)
		}
		// The record names where the secret is and holds none.
		if !strings.HasPrefix(c.Credentials.Token.Ref, "env:COLLEGIUM_") || !strings.HasPrefix(c.Credentials.SigningSecret.Ref, "env:COLLEGIUM_") {
			t.Errorf("%s credentials = %+v", c.ID, c.Credentials)
		}
	}
	sl, _ := p.Clients.Get("trial", defaultSlackClient)
	if sl.Identity.Team != "T01ABCDEF" || sl.DefaultAgent != "helper" || !sl.Slack.Sessions || len(sl.Slack.Commands) != 1 || sl.Bots["U08BOT"] != "helper" {
		t.Fatalf("slack client = %+v", sl)
	}
	gh, _ := p.Clients.Get("trial", defaultGitHubClient)
	if len(gh.Identity.Owners) != 2 || gh.GitHub.AutoLabels["acme/handbook"]["agent:auto"] != "helper" || gh.GitHub.ReadRepositories[0] != "acme/handbook" {
		t.Fatalf("github client = %+v", gh)
	}

	ingress := p.IngressHandler()
	ready := func() bool {
		w := httptest.NewRecorder()
		ingress.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		return w.Code == http.StatusOK
	}
	wait := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("never happened: %s", what)
			}
		}
	}
	wait("the default clients are ingesting", ready)

	// The legacy payloads carry no app id, and are still routed and verified.
	slackBody := `{"type":"event_callback","event_id":"slack-1","team_id":"T01ABCDEF","event":{"type":"app_mention","user":"U01","channel":"C01","ts":"1700000000.000100","text":"explain"}}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(vars[EnvSlackSecret]))
	mac.Write([]byte("v0:" + ts + ":" + slackBody))
	r := httptest.NewRequest(http.MethodPost, "/connectors/slack", strings.NewReader(slackBody))
	r.Header.Set(signature.HeaderSlackTimestamp, ts)
	r.Header.Set(signature.HeaderSlackSignature, "v0="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	ingress.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("slack webhook = %d %s", w.Code, w.Body)
	}

	githubBody := `{"action":"created","repository":{"name":"repo","owner":{"login":"acme"}},"sender":{"login":"human","type":"User"},"issue":{"number":7},"comment":{"id":91,"body":"@helper explain"}}`
	mac = hmac.New(sha256.New, []byte(vars[EnvGitHubSecret]))
	mac.Write([]byte(githubBody))
	r = httptest.NewRequest(http.MethodPost, "/connectors/github", strings.NewReader(githubBody))
	r.Header.Set(signature.HeaderGitHubSignature, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	r.Header.Set("X-GitHub-Event", "issue_comment")
	r.Header.Set("X-GitHub-Delivery", "github-1")
	w = httptest.NewRecorder()
	ingress.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("github webhook = %d %s", w.Code, w.Body)
	}

	wait("both surfaces were answered", func() bool { return len(slackFake.Messages()) >= 1 && len(githubFake.Comments()) == 1 })
	origins := map[string]string{}
	for _, run := range p.Runs.List("trial", 10) {
		origins[run.Origin] = run.Client
	}
	if origins["slack"] != defaultSlackClient || origins["github"] != defaultGitHubClient {
		t.Fatalf("runs by origin = %v", origins)
	}
	// Seeding twice, as a restart with a durable store would, is refused
	// rather than doubled.
	if err := seedClients(ctx, p, "trial", envOf(vars)); err == nil {
		t.Fatal("the default clients were created twice")
	}
}

// Which variables a tenant may reference is the operator's to say, on the
// command line. The server's own tenant is also given the variables behind
// its default clients, and no other tenant is.
func TestSecretReferencesAreGivenToTenantsByTheOperator(t *testing.T) {
	env := envOf(map[string]string{EnvSlackToken: "t", EnvSlackSecret: "s"})
	got, err := secretRefs("acme=COLLEGIUM_SLACK_TOKEN_DOCS, COLLEGIUM_SLACK_SECRET_DOCS; globex=COLLEGIUM_GH_GLOBEX;", "trial", env)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || len(got["acme"]) != 2 || got["globex"][0] != "COLLEGIUM_GH_GLOBEX" {
		t.Fatalf("refs = %v", got)
	}
	if len(got["trial"]) != 2 || got["trial"][0] != EnvSlackToken || got["trial"][1] != EnvSlackSecret {
		t.Fatalf("the server's tenant = %v, want its default slack client's variables and no github ones", got["trial"])
	}
	for _, other := range []string{"acme", "globex"} {
		for _, name := range got[other] {
			if name == EnvSlackToken || name == EnvSlackSecret {
				t.Fatalf("%s was given the default client's variables", other)
			}
		}
	}
	// With nothing configured, nobody may reference anything.
	if got, err := secretRefs("", "trial", envOf(nil)); err != nil || len(got) != 0 {
		t.Fatalf("refs with nothing set = %v, %v", got, err)
	}
	for _, raw := range []string{"acme", "=COLLEGIUM_X", "acme=", "acme=ANTHROPIC_API_KEY", "acme=COLLEGIUM_OIDC_CLIENT_SECRET", "acme=collegium_x"} {
		if _, err := secretRefs(raw, "trial", envOf(nil)); err == nil {
			t.Errorf("-secret-refs %q was accepted", raw)
		}
	}
}
