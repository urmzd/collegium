package platform_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/urmzd/mandatum/pkg/agentrt"
	"github.com/urmzd/mandatum/pkg/agentspec"
	"github.com/urmzd/mandatum/pkg/runtime"
	"github.com/urmzd/mandatum/pkg/signature"
	saigetypes "github.com/urmzd/saige/agent/types"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/clients"
	"github.com/urmzd/collegium/pkg/integrations/github"
	"github.com/urmzd/collegium/pkg/integrations/slack"
	"github.com/urmzd/collegium/pkg/policy"
	"github.com/urmzd/collegium/pkg/runs"
)

// fakes hands each client the fake Slack or GitHub its token names, the way
// the real APIs are told apart by the token they are called with.
type fakes struct {
	mu     sync.Mutex
	slack  map[string]*slack.Fake
	github map[string]*github.Fake
}

func (f *fakes) slackFor(token string) *slack.Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.slack == nil {
		f.slack = map[string]*slack.Fake{}
	}
	if f.slack[token] == nil {
		f.slack[token] = slack.NewFake()
	}
	return f.slack[token]
}

func (f *fakes) githubFor(token string) *github.Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.github == nil {
		f.github = map[string]*github.Fake{}
	}
	if f.github[token] == nil {
		f.github[token] = github.NewFake()
	}
	return f.github[token]
}

func (f *fakes) config() platform.Config {
	return platform.Config{
		SlackAPI:       func(token string) slack.API { return f.slackFor(token) },
		GitHubAPI:      func(token string) github.API { return f.githubFor(token) },
		ClientInterval: -1,
	}
}

// slackClient is a Slack client as a test writes one: the token names its
// fake, and the secret is what its requests are signed with.
func slackClient(id, app, team, agent string) platform.ClientInput {
	return platform.ClientInput{
		Client: clients.Client{
			ID: id, Name: id, Surface: clients.Slack, Enabled: true,
			Identity: clients.Identity{AppID: app, Team: team}, DefaultAgent: agent,
			Slack: &clients.SlackOptions{Sessions: true},
		},
		Token:         clients.SecretInput{Value: "token-" + id},
		SigningSecret: clients.SecretInput{Value: "secret-" + id},
	}
}

func create(t *testing.T, p *platform.Platform, tenantName string, in platform.ClientInput) clients.Client {
	t.Helper()
	c, err := p.CreateClient(context.Background(), tenantName, "test", in)
	if err != nil {
		t.Fatalf("create client %s: %v", in.ID, err)
	}
	// The platform starts its triggers a moment after Run is called.
	eventually(t, "every client is ingesting", func() bool {
		w := httptest.NewRecorder()
		p.IngressHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		return w.Code == http.StatusOK
	})
	return c
}

// signedSlack signs a body with a secret the way Slack does and serves it.
func signedSlack(h http.Handler, secret, body string) int {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + body))
	r := httptest.NewRequest(http.MethodPost, "/connectors/slack", strings.NewReader(body))
	r.Header.Set(signature.HeaderSlackTimestamp, ts)
	r.Header.Set(signature.HeaderSlackSignature, "v0="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

// mentionOf is an app_mention delivered to one Slack app in one team.
func mentionOf(app, team, event, channel, thread string) string {
	return `{"type":"event_callback","api_app_id":"` + app + `","team_id":"` + team + `","event_id":"` + event + `","event":{"type":"app_mention",` +
		`"user":"U0HUMAN1","text":"<@U08BOTID> go","ts":"` + thread + `","channel":"` + channel + `"}}`
}

func said(f *slack.Fake, text string) bool {
	for _, m := range f.Messages() {
		if strings.Contains(m.Text, text) {
			return true
		}
	}
	return false
}

func agent(t *testing.T, p *platform.Platform, tenantName, name string) {
	t.Helper()
	spec := agentspec.AgentSpec{Name: name, Model: "offline", Provider: "offline", SystemPrompt: "x"}
	if _, err := p.Agents.Create(context.Background(), tenantName, spec, "test"); err != nil {
		t.Fatal(err)
	}
}

func runOf(p *platform.Platform, tenantName, agentName string) (runs.Run, bool) {
	for _, r := range p.Runs.List(tenantName, 50) {
		if r.Agent == agentName {
			return r, true
		}
	}
	return runs.Run{}, false
}

const team = "T01ABCDEF"

// slackTurn is a turn of an agent in a tenant, answering in a Slack channel.
func slackTurn(tenantName, agentName string) runtime.Request {
	return runtime.Request{
		Tenant: tenantName, RunID: tenantName + "-turn", Source: "slack://" + team + "/C01",
		Revision: agentrt.Revision{Spec: agentrt.Spec{Name: agentName}},
	}
}

// Two Slack apps in one workspace are two clients, each answering as its own
// agent through its own token, and each run records the client that raised it.
func TestTwoSlackClientsInOneTenantRouteToDifferentAgents(t *testing.T) {
	f := &fakes{}
	c := &cast{says: map[string][][]saigetypes.Delta{"docs": {say("Docs here.")}, "ops": {say("Ops here.")}}}
	p := stage(t, c, f.config())
	create(t, p, tenant, slackClient("docs-app", "A0DOCS", team, "docs"))
	create(t, p, tenant, slackClient("ops-app", "A0OPS", team, "ops"))
	ingress := p.IngressHandler()

	if code := signedSlack(ingress, "secret-docs-app", mentionOf("A0DOCS", team, "Ev1", "C01", "1700000000.000100")); code != http.StatusOK {
		t.Fatalf("docs mention answered %d", code)
	}
	if code := signedSlack(ingress, "secret-ops-app", mentionOf("A0OPS", team, "Ev2", "C01", "1700000000.000200")); code != http.StatusOK {
		t.Fatalf("ops mention answered %d", code)
	}
	docs, ops := f.slackFor("token-docs-app"), f.slackFor("token-ops-app")
	eventually(t, "each app answered as its own agent", func() bool { return said(docs, "Docs here.") && said(ops, "Ops here.") })
	if said(docs, "Ops here.") || said(ops, "Docs here.") {
		t.Fatal("an answer went out through the other client's token")
	}
	for agentName, want := range map[string]string{"docs": "docs-app", "ops": "ops-app"} {
		r, ok := runOf(p, tenant, agentName)
		if !ok || r.Client != want || r.Origin != "slack" {
			t.Fatalf("run of %s = %+v, want raised by client %s", agentName, r, want)
		}
	}
}

// A client reaches its default agent and the agents in its allow-list, and no
// others, however the request names them.
func TestAClientRoutesOnlyToTheAgentsItIsAllowed(t *testing.T) {
	f := &fakes{}
	c := &cast{says: map[string][][]saigetypes.Delta{"docs": {say("Docs here.")}, "ops": {say("Ops here.")}, "secret": {say("No.")}}}
	p := stage(t, c, f.config())
	in := slackClient("docs-app", "A0DOCS", team, "docs")
	in.Agents = []string{"ops"}
	create(t, p, tenant, in)
	ingress := p.IngressHandler()

	named := func(event, name string) string {
		return `{"type":"event_callback","api_app_id":"A0DOCS","team_id":"` + team + `","event_id":"` + event + `","event":{"type":"app_mention",` +
			`"user":"U0HUMAN1","text":"<@U08BOTID> @` + name + ` go","ts":"1700000000.000` + event[2:] + `00","channel":"C01"}}`
	}
	if code := signedSlack(ingress, "secret-docs-app", named("Ev1", "ops")); code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	// An agent outside the allow-list is not a name the client knows, so the
	// mention falls to the default agent rather than reaching it.
	if code := signedSlack(ingress, "secret-docs-app", named("Ev2", "secret")); code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	out := f.slackFor("token-docs-app")
	eventually(t, "the allowed agents answered", func() bool { return said(out, "Ops here.") && said(out, "Docs here.") })
	if _, ran := runOf(p, tenant, "secret"); ran {
		t.Fatal("a client raised a run of an agent outside its allow-list")
	}
	// A client with no default and no allow-list routes to nothing.
	bare := slackClient("bare", "A0BARE", team, "")
	create(t, p, tenant, bare)
	before := len(p.Runs.List(tenant, 50))
	if code := signedSlack(ingress, "secret-bare", mentionOf("A0BARE", team, "Ev3", "C01", "1700000000.000300")); code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	time.Sleep(100 * time.Millisecond)
	if after := len(p.Runs.List(tenant, 50)); after != before {
		t.Fatalf("a client that allows no agent raised %d runs", after-before)
	}
}

// Two tenants each with a Slack client, in the same Slack team on purpose:
// neither can reach the other's runs, tools, stop button or deliveries.
func TestTenantsWithSlackClientsCannotReachEachOther(t *testing.T) {
	f := &fakes{}
	c := &cast{
		says:  map[string][][]saigetypes.Delta{"lead": {say("Acme answer.")}},
		gates: map[string]chan struct{}{"lead": make(chan struct{})},
	}
	p := stage(t, c, f.config())
	agent(t, p, "globex", "helper")
	create(t, p, tenant, slackClient("acme-app", "A0ACME", team, "lead"))
	create(t, p, "globex", slackClient("globex-app", "A0GLOBEX", team, "helper"))
	ingress := p.IngressHandler()
	acme, globex := f.slackFor("token-acme-app"), f.slackFor("token-globex-app")

	const thread = "1700000000.000100"
	if code := signedSlack(ingress, "secret-acme-app", mentionOf("A0ACME", team, "Ev1", "C01", thread)); code != http.StatusOK {
		t.Fatalf("acme mention answered %d", code)
	}
	var run runs.Run
	eventually(t, "acme's run is in flight", func() bool {
		r, ok := runOf(p, tenant, "lead")
		run = r
		return ok && r.Status == runs.Running
	})

	// Runs: the other tenant has none, and cannot see this one by id.
	if got := p.Runs.List("globex", 50); len(got) != 0 {
		t.Fatalf("globex sees runs: %+v", got)
	}
	if _, err := p.Runs.Get("globex", run.ID); !errors.Is(err, runs.ErrNotFound) {
		t.Fatalf("globex reached acme's run: %v", err)
	}

	// Stop button: globex's app presses stop on the very session acme's run
	// is answering in. Its own secret verifies it, and it stops nothing.
	stop := func(app, event string) string {
		return `{"type":"event_callback","api_app_id":"` + app + `","team_id":"` + team + `","event_id":"` + event + `","event":{"type":"agent_session_stopped",` +
			`"user":"U0HUMAN2","channel":"C01","thread_ts":"` + thread + `","streaming_message_ts":[]}}`
	}
	if code := signedSlack(ingress, "secret-globex-app", stop("A0GLOBEX", "Ev2")); code != http.StatusOK {
		t.Fatalf("globex stop answered %d", code)
	}
	if r, _ := p.Runs.Get(tenant, run.ID); r.Status != runs.Running {
		t.Fatalf("another tenant's client stopped the run: %s", r.Status)
	}

	// Tools: a turn in globex gets globex's client, and what it posts goes out
	// through globex's token even when it names acme's channel.
	tools, err := p.ConnectorTools(slackTurn("globex", "helper"))
	if err != nil {
		t.Fatal(err)
	}
	post, err := tools.Lookup("slack_post_message")
	if err != nil {
		t.Fatalf("globex has no slack tool: %v", err)
	}
	if _, err := post.Execute(context.Background(), map[string]any{"text": "from globex"}); err != nil {
		t.Fatal(err)
	}
	if !said(globex, "from globex") || said(acme, "from globex") {
		t.Fatal("a tenant's tool wrote through another tenant's client")
	}
	// A tenant with no client has no tools at all.
	none, err := p.ConnectorTools(slackTurn("initech", "helper"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := none.Lookup("slack_post_message"); err == nil {
		t.Fatal("a tenant with no client was given a slack tool")
	}

	// Deliveries: acme's answer goes out through acme's client alone.
	c.release("lead")
	eventually(t, "acme's run answered", func() bool { return said(acme, "Acme answer.") })
	if said(globex, "Acme answer.") {
		t.Fatal("acme's answer was delivered through globex's client")
	}
	// The same event id from the other tenant is not joined to acme's run.
	if code := signedSlack(ingress, "secret-globex-app", mentionOf("A0GLOBEX", team, "Ev1", "C01", thread)); code != http.StatusOK {
		t.Fatalf("globex mention answered %d", code)
	}
	time.Sleep(100 * time.Millisecond)
	if got := p.Runs.List("globex", 50); len(got) != 0 {
		t.Fatalf("a reused event id gave globex a run: %+v", got)
	}

	// The API is tenant-bound too: a client in another tenant does not exist.
	if _, err := p.Clients.Get("globex", "acme-app"); !errors.Is(err, clients.ErrNotFound) {
		t.Fatalf("globex read acme's client: %v", err)
	}
	if err := p.DeleteClient(context.Background(), "globex", "acme-app", "mallory"); !errors.Is(err, clients.ErrNotFound) {
		t.Fatalf("globex deleted acme's client: %v", err)
	}
}

// The signature is checked with the secret of the client a request claims to
// be for. Knowing one client's secret is worth nothing against another.
func TestARequestSignedByOneClientClaimingAnotherIsRefused(t *testing.T) {
	f := &fakes{}
	c := &cast{says: map[string][][]saigetypes.Delta{"lead": {say("Hello.")}}}
	p := stage(t, c, f.config())
	agent(t, p, "globex", "helper")
	create(t, p, tenant, slackClient("acme-app", "A0ACME", team, "lead"))
	create(t, p, "globex", slackClient("globex-app", "A0GLOBEX", "T02OTHER", "helper"))
	ingress := p.IngressHandler()

	for name, body := range map[string]string{
		"another client's app":  mentionOf("A0ACME", team, "Ev1", "C01", "1700000000.000100"),
		"another client's team": mentionOf("A0GLOBEX", team, "Ev2", "C01", "1700000000.000200"),
	} {
		code := signedSlack(ingress, "secret-globex-app", body)
		if name == "another client's app" && code != http.StatusUnauthorized {
			t.Fatalf("%s, signed with globex's secret, answered %d, want 401", name, code)
		}
		// An app in a team no client serves is nobody's.
		if name == "another client's team" && code != http.StatusNotFound {
			t.Fatalf("%s answered %d, want 404", name, code)
		}
	}
	// An identity no client has, however it is signed.
	if code := signedSlack(ingress, "secret-acme-app", mentionOf("A0NOBODY", "T09NOBODY", "Ev3", "C01", "1700000000.000300")); code != http.StatusNotFound {
		t.Fatalf("an unknown client answered %d, want 404", code)
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(p.Runs.List(tenant, 50)) + len(p.Runs.List("globex", 50)); got != 0 {
		t.Fatalf("%d runs were raised by refused requests", got)
	}
	// The same body under its own client's secret is accepted.
	if code := signedSlack(ingress, "secret-acme-app", mentionOf("A0ACME", team, "Ev1", "C01", "1700000000.000100")); code != http.StatusOK {
		t.Fatalf("a rightly signed request answered %d", code)
	}
	eventually(t, "the run answered", func() bool { return said(f.slackFor("token-acme-app"), "Hello.") })
}

// A disabled client answers exactly as a client that does not exist, and
// comes back when it is enabled again.
func TestADisabledClientIsNotFound(t *testing.T) {
	f := &fakes{}
	c := &cast{says: map[string][][]saigetypes.Delta{"lead": {say("Back.")}}}
	p := stage(t, c, f.config())
	in := slackClient("acme-app", "A0ACME", team, "lead")
	create(t, p, tenant, in)
	ingress := p.IngressHandler()
	body := mentionOf("A0ACME", team, "Ev1", "C01", "1700000000.000100")

	off := in
	off.Enabled = false
	off.Token, off.SigningSecret = clients.SecretInput{}, clients.SecretInput{}
	saved, err := p.UpdateClient(context.Background(), tenant, "acme-app", "test", off)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Enabled || saved.Version != 2 {
		t.Fatalf("client = %+v, want disabled at version 2", saved)
	}
	unknown := signedSlack(ingress, "secret-acme-app", mentionOf("A0NOBODY", "T09NOBODY", "Ev9", "C01", "1700000000.000900"))
	if code := signedSlack(ingress, "secret-acme-app", body); code != http.StatusNotFound || code != unknown {
		t.Fatalf("a disabled client answered %d, an unknown one %d, want 404 for both", code, unknown)
	}
	if got := len(p.Runs.List(tenant, 50)); got != 0 {
		t.Fatalf("a disabled client raised %d runs", got)
	}
	tools, err := p.ConnectorTools(slackTurn(tenant, "lead"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Lookup("slack_post_message"); err == nil {
		t.Fatal("a disabled client still lends its tools")
	}

	// Enabled again with no secrets given: the ones it had are kept.
	on := off
	on.Enabled = true
	if _, err := p.UpdateClient(context.Background(), tenant, "acme-app", "test", on); err != nil {
		t.Fatal(err)
	}
	if code := signedSlack(ingress, "secret-acme-app", body); code != http.StatusOK {
		t.Fatalf("an enabled client answered %d", code)
	}
	eventually(t, "the run answered", func() bool { return said(f.slackFor("token-acme-app"), "Back.") })
}

// The number of clients a workspace may have is a limit like any other: a
// default, lowered or raised by the tenant's policy, and counted per surface.
func TestTheNumberOfClientsIsCapped(t *testing.T) {
	f := &fakes{}
	p := stage(t, &cast{says: map[string][][]saigetypes.Delta{"lead": nil}}, f.config())
	if _, err := p.Policies.Put(tenant, policy.Policy{
		Scope: policy.Scope{Kind: policy.Tenant}, Period: policy.Day, Action: policy.Block,
		MaxClients: 3, MaxClientsPerSurface: 2,
	}, "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	create(t, p, tenant, slackClient("one", "A0ONE", team, "lead"))
	create(t, p, tenant, slackClient("two", "A0TWO", team, "lead"))
	if _, err := p.CreateClient(context.Background(), tenant, "test", slackClient("three", "A0THREE", team, "lead")); !errors.Is(err, clients.ErrLimit) {
		t.Fatalf("a third slack client under a limit of 2 per surface: %v", err)
	}
	gh := func(id, owner string) platform.ClientInput {
		return platform.ClientInput{
			Client: clients.Client{
				ID: id, Name: id, Surface: clients.GitHub, Enabled: true,
				Identity: clients.Identity{Owners: []string{owner}}, DefaultAgent: "lead",
			},
			Token: clients.SecretInput{Value: "token-" + id}, SigningSecret: clients.SecretInput{Value: "secret-" + id},
		}
	}
	create(t, p, tenant, gh("gh-one", "acme"))
	if _, err := p.CreateClient(context.Background(), tenant, "test", gh("gh-two", "acme-labs")); !errors.Is(err, clients.ErrLimit) {
		t.Fatalf("a fourth client under a limit of 3: %v", err)
	}
	// Another tenant's clients are not counted against this one.
	agent(t, p, "globex", "helper")
	create(t, p, "globex", slackClient("theirs", "A0THEIRS", "T02OTHER", "helper"))
	// Deleting one makes room, and takes the client's secrets with it.
	if err := p.DeleteClient(context.Background(), tenant, "one", "test"); err != nil {
		t.Fatal(err)
	}
	create(t, p, tenant, gh("gh-two", "acme-labs"))

	// With no policy the defaults apply.
	if caps := p.ClientCaps("globex"); caps != clients.DefaultCaps() {
		t.Fatalf("caps with no policy = %+v", caps)
	}
}

// Two racers for the last place: one gets it.
func TestTheLastClientPlaceIsTakenOnce(t *testing.T) {
	f := &fakes{}
	p := stage(t, &cast{says: map[string][][]saigetypes.Delta{"lead": nil}}, f.config())
	if _, err := p.Policies.Put(tenant, policy.Policy{
		Scope: policy.Scope{Kind: policy.Tenant}, Period: policy.Day, Action: policy.Block, MaxClients: 4, MaxClientsPerSurface: 4,
	}, "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	made := 0
	for i := range 12 {
		wg.Go(func() {
			id := "app-" + strconv.Itoa(i)
			_, err := p.CreateClient(context.Background(), tenant, "test", slackClient(id, "A0APP"+strconv.Itoa(i), team, "lead"))
			if err == nil {
				mu.Lock()
				made++
				mu.Unlock()
			} else if !errors.Is(err, clients.ErrLimit) {
				t.Errorf("create %s: %v", id, err)
			}
		})
	}
	wg.Wait()
	if made != 4 || len(p.Clients.List(tenant)) != 4 {
		t.Fatalf("%d creates succeeded and %d clients exist under a limit of 4", made, len(p.Clients.List(tenant)))
	}
}

// A client's limits are a policy scoped to it: they count only the runs it
// raised, and a second client in the same workspace is not held by them.
func TestAClientsRunLimitCountsOnlyItsOwnRuns(t *testing.T) {
	f := &fakes{}
	c := &cast{
		says:  map[string][][]saigetypes.Delta{"lead": {say("One."), say("Two."), say("Three.")}},
		gates: map[string]chan struct{}{"lead": make(chan struct{})},
	}
	p := stage(t, c, f.config())
	create(t, p, tenant, slackClient("narrow", "A0NARROW", team, "lead"))
	create(t, p, tenant, slackClient("wide", "A0WIDE", team, "lead"))
	if _, err := p.Policies.Put(tenant, policy.Policy{
		Scope: policy.Scope{Kind: policy.Client, Name: "narrow"}, Period: policy.Day, Action: policy.Block, MaxConcurrent: 1,
	}, "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	// A client's policy cannot claim more than the workspace allows.
	if _, err := p.Policies.Put(tenant, policy.Policy{
		Scope: policy.Scope{Kind: policy.Tenant}, Period: policy.Day, Action: policy.Block, MaxConcurrent: 5,
	}, "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Policies.Put(tenant, policy.Policy{
		Scope: policy.Scope{Kind: policy.Client, Name: "wide"}, Period: policy.Day, Action: policy.Block, MaxConcurrent: 9,
	}, "ada", time.Now()); !errors.Is(err, policy.ErrInvalid) {
		t.Fatalf("a client's policy loosened the workspace's: %v", err)
	}
	ingress := p.IngressHandler()

	if code := signedSlack(ingress, "secret-narrow", mentionOf("A0NARROW", team, "Ev1", "C01", "1700000000.000100")); code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	eventually(t, "the first run is in flight", func() bool {
		r, ok := runOf(p, tenant, "lead")
		return ok && r.Status == runs.Running
	})
	// The client's second run at once is refused. Slack is still told 200:
	// the request was genuine, and retrying it would not help.
	if code := signedSlack(ingress, "secret-narrow", mentionOf("A0NARROW", team, "Ev2", "C01", "1700000000.000200")); code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	eventually(t, "the refusal is in the ledger", func() bool {
		for _, n := range p.Usage.Notices(tenant, 10) {
			if n.Client == "narrow" && n.Scope != nil && n.Scope.Kind == policy.Client && n.Limit == policy.LimitConcurrent {
				return true
			}
		}
		return false
	})
	// The other client is not held by a limit that is not its own.
	if code := signedSlack(ingress, "secret-wide", mentionOf("A0WIDE", team, "Ev3", "C01", "1700000000.000300")); code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	eventually(t, "the other client's run was admitted", func() bool {
		n := 0
		for _, r := range p.Runs.List(tenant, 50) {
			if r.Client == "wide" {
				n++
			}
		}
		return n == 1
	})
	if n := len(p.Runs.List(tenant, 50)); n != 2 {
		t.Fatalf("%d runs exist, want the first of narrow and the one of wide", n)
	}
	if u := p.Usage.Usage(tenant, policy.Scope{Kind: policy.Client, Name: "narrow"}, time.Time{}); u.Concurrent != 1 {
		t.Fatalf("narrow has %d in flight, want 1", u.Concurrent)
	}
}

// Two GitHub installations in one workspace: a delivery goes to the one that
// serves its account, verified by that one's secret, and is answered through
// that one's token.
func TestTwoGitHubClientsAreToldApartByTheAccountTheyServe(t *testing.T) {
	f := &fakes{}
	c := &cast{says: map[string][][]saigetypes.Delta{"reviewer": {say("Reviewed.")}, "triage": {say("Triaged.")}}}
	p := stage(t, c, f.config())
	gh := func(id, owner, installation, agentName string) platform.ClientInput {
		return platform.ClientInput{
			Client: clients.Client{
				ID: id, Name: id, Surface: clients.GitHub, Enabled: true,
				Identity:     clients.Identity{Owners: []string{owner}, Installation: installation},
				DefaultAgent: agentName, Bots: map[string]string{agentName: agentName},
			},
			Token: clients.SecretInput{Value: "token-" + id}, SigningSecret: clients.SecretInput{Value: "secret-" + id},
		}
	}
	create(t, p, tenant, gh("acme-gh", "acme", "101", "reviewer"))
	create(t, p, tenant, gh("labs-gh", "acme-labs", "202", "triage"))
	ingress := p.IngressHandler()

	post := func(secret, delivery, owner, installation, who string) int {
		body := `{"action":"created","installation":{"id":` + installation + `},"repository":{"name":"repo","owner":{"login":"` + owner + `"}},` +
			`"sender":{"login":"human","type":"User"},"issue":{"number":7},"comment":{"id":91,"body":"@` + who + ` look"}}`
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(body))
		r := httptest.NewRequest(http.MethodPost, "/connectors/github", strings.NewReader(body))
		r.Header.Set(signature.HeaderGitHubSignature, "sha256="+hex.EncodeToString(mac.Sum(nil)))
		r.Header.Set("X-GitHub-Event", "issue_comment")
		r.Header.Set("X-GitHub-Delivery", delivery)
		w := httptest.NewRecorder()
		ingress.ServeHTTP(w, r)
		return w.Code
	}
	// One installation's secret does not open the other's account.
	if code := post("secret-acme-gh", "d0", "acme-labs", "202", "triage"); code != http.StatusUnauthorized {
		t.Fatalf("acme's secret on labs' account answered %d, want 401", code)
	}
	if code := post("secret-acme-gh", "d1", "acme", "101", "reviewer"); code != http.StatusOK {
		t.Fatalf("acme delivery answered %d", code)
	}
	if code := post("secret-labs-gh", "d2", "acme-labs", "202", "triage"); code != http.StatusOK {
		t.Fatalf("labs delivery answered %d", code)
	}
	acme, labs := f.githubFor("token-acme-gh"), f.githubFor("token-labs-gh")
	eventually(t, "each installation answered", func() bool { return len(acme.Comments()) == 1 && len(labs.Comments()) == 1 })
	if !strings.Contains(acme.Comments()[0].Body, "Reviewed.") || !strings.Contains(labs.Comments()[0].Body, "Triaged.") {
		t.Fatalf("answers crossed: %q and %q", acme.Comments()[0].Body, labs.Comments()[0].Body)
	}
	if r, _ := runOf(p, tenant, "triage"); r.Client != "labs-gh" {
		t.Fatalf("run of triage raised by %q, want labs-gh", r.Client)
	}
}

// A client's secrets may live in the server's environment, named by a
// reference. The reference may only name a variable set aside for that.
func TestSecretsByReferenceComeFromTheEnvironmentAndNowhereElse(t *testing.T) {
	f := &fakes{}
	cfg := f.config()
	cfg.Env = func(name string) (string, bool) {
		v, ok := map[string]string{"COLLEGIUM_SLACK_TOKEN_DOCS": "token-docs", "COLLEGIUM_SLACK_SECRET_DOCS": "secret-docs", "ANTHROPIC_API_KEY": "sk-model"}[name]
		return v, ok
	}
	c := &cast{says: map[string][][]saigetypes.Delta{"lead": {say("By reference.")}}}
	p := stage(t, c, cfg)
	in := slackClient("docs", "A0DOCS", team, "lead")
	in.Token = clients.SecretInput{Ref: "env:ANTHROPIC_API_KEY"}
	if _, err := p.CreateClient(context.Background(), tenant, "test", in); !errors.Is(err, clients.ErrInvalid) {
		t.Fatalf("a reference to the model key was accepted: %v", err)
	}
	in.Token = clients.SecretInput{Ref: "env:COLLEGIUM_NOT_SET"}
	if _, err := p.CreateClient(context.Background(), tenant, "test", in); !errors.Is(err, clients.ErrInvalid) {
		t.Fatalf("a reference to an unset variable was accepted: %v", err)
	}
	in.Token = clients.SecretInput{Ref: "env:COLLEGIUM_SLACK_TOKEN_DOCS"}
	in.SigningSecret = clients.SecretInput{Ref: "env:COLLEGIUM_SLACK_SECRET_DOCS"}
	saved := create(t, p, tenant, in)
	if saved.Credentials.Token != (clients.SecretRef{Set: true, Ref: "env:COLLEGIUM_SLACK_TOKEN_DOCS"}) {
		t.Fatalf("credentials = %+v", saved.Credentials)
	}
	if code := signedSlack(p.IngressHandler(), "secret-docs", mentionOf("A0DOCS", team, "Ev1", "C01", "1700000000.000100")); code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	eventually(t, "the run answered through the referenced token", func() bool { return said(f.slackFor("token-docs"), "By reference.") })
}

// Two Slack apps answering in the same thread each hold their own sessions:
// one app's stop button stops only the run that app raised.
func TestASlackStopReachesOnlyTheRunsOfItsOwnClient(t *testing.T) {
	f := &fakes{}
	c := &cast{
		says:  map[string][][]saigetypes.Delta{"lead": {say("Late.")}, "ops": {say("Also late.")}},
		gates: map[string]chan struct{}{"lead": make(chan struct{}), "ops": make(chan struct{})},
	}
	p := stage(t, c, f.config())
	create(t, p, tenant, slackClient("lead-app", "A0LEAD", team, "lead"))
	create(t, p, tenant, slackClient("ops-app", "A0OPS", team, "ops"))
	ingress := p.IngressHandler()
	const thread = "1700000000.000100"
	for app, secret := range map[string]string{"A0LEAD": "secret-lead-app", "A0OPS": "secret-ops-app"} {
		body := `{"type":"event_callback","api_app_id":"` + app + `","team_id":"` + team + `","event_id":"Ev-` + app + `","event":{"type":"app_mention",` +
			`"user":"U0HUMAN1","text":"<@U08BOTID> go","ts":"1700000090.001200","thread_ts":"` + thread + `","channel":"C01"}}`
		if code := signedSlack(ingress, secret, body); code != http.StatusOK {
			t.Fatalf("%s mention answered %d", app, code)
		}
	}
	running := func(agentName string) bool {
		r, ok := runOf(p, tenant, agentName)
		return ok && r.Status == runs.Running
	}
	eventually(t, "both runs are in flight", func() bool { return running("lead") && running("ops") })
	eventually(t, "both sessions show working", func() bool {
		return len(f.slackFor("token-lead-app").Statuses()) > 0 && len(f.slackFor("token-ops-app").Statuses()) > 0
	})

	stop := `{"type":"event_callback","api_app_id":"A0OPS","team_id":"` + team + `","event_id":"EvStop","event":{"type":"agent_session_stopped",` +
		`"user":"U0HUMAN2","channel":"C01","thread_ts":"` + thread + `","streaming_message_ts":[]}}`
	if code := signedSlack(ingress, "secret-ops-app", stop); code != http.StatusOK {
		t.Fatalf("stop answered %d", code)
	}
	ops, _ := runOf(p, tenant, "ops")
	if ops.Status != runs.Stopped || ops.Cause != "Stopped by slack:U0HUMAN2" {
		t.Fatalf("ops run = %s (%q), want stopped from slack", ops.Status, ops.Cause)
	}
	if !running("lead") {
		t.Fatal("one app's stop button stopped a run another app raised")
	}
	if said(f.slackFor("token-lead-app"), "Stopped by") {
		t.Fatal("the stop was confirmed through the other app")
	}
}
