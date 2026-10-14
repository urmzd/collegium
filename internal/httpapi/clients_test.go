package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/clients"
	slackflow "github.com/urmzd/collegium/pkg/integrations/slack"
	"github.com/urmzd/mandatum/pkg/signature"
)

// The secrets a test writes. Nothing the server says or logs may contain them.
const (
	botToken      = "xoxb-THE-BOT-TOKEN-0001"
	signingSecret = "THE-SIGNING-SECRET-0001"
)

func slackBody(id, app, agent string) string {
	return `{"id":"` + id + `","name":"` + id + `","surface":"slack","identity":{"app_id":"` + app + `","team":"T01ABCDEF"},` +
		`"default_agent":"` + agent + `","credentials":{"token":{"value":"` + botToken + `"},"signing_secret":{"value":"` + signingSecret + `"}}}`
}

// syncBuffer is a log the server writes from many goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// clientFixture is a fixture whose Slack clients write to one fake.
func clientFixture(t *testing.T) (*fixture, *slackflow.Fake, *syncBuffer) {
	t.Helper()
	fake, logs := slackflow.NewFake(), &syncBuffer{}
	f := newFixtureWith(t, platform.Config{
		SlackAPI:       func(string) slackflow.API { return fake },
		ClientInterval: -1,
		Log:            slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	return f, fake, logs
}

// A secret is written and never read back. Every response of every client
// route, the routes beside them, and the server's own log are searched for it.
func TestClientSecretsNeverAppearInAResponseOrALog(t *testing.T) {
	f, fake, logs := clientFixture(t)
	admin := f.machine(t, "acme", everything...)
	f.do(t, nil, "POST", "/api/agents", admin, helperSpec)

	var said []string
	keep := func(res *http.Response, out map[string]any) (*http.Response, map[string]any) {
		raw, _ := json.Marshal(out)
		said = append(said, string(raw))
		return res, out
	}
	res, created := keep(f.do(t, nil, "POST", "/api/clients", admin, slackBody("docs", "A0DOCS", "helper")))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %v", res.StatusCode, created)
	}
	creds := created["credentials"].(map[string]any)
	for _, name := range []string{"token", "signing_secret"} {
		got := creds[name].(map[string]any)
		if got["set"] != true || len(got) != 1 {
			t.Fatalf("credentials.%s = %v, want only that it is set", name, got)
		}
	}
	if created["enabled"] != true || created["version"].(float64) != 1 || created["created_by"] != "svc-acme" {
		t.Fatalf("created = %v", created)
	}
	keep(f.do(t, nil, "GET", "/api/clients", admin, ""))
	keep(f.do(t, nil, "GET", "/api/clients/docs", admin, ""))
	// An update that sends no secrets keeps them, and still shows none.
	res, updated := keep(f.do(t, nil, "PUT", "/api/clients/docs", admin,
		`{"name":"Docs","surface":"slack","identity":{"app_id":"A0DOCS","team":"T01ABCDEF"},"default_agent":"helper","slack":{"sessions":true}}`))
	if res.StatusCode != http.StatusOK || updated["version"].(float64) != 2 || updated["name"] != "Docs" {
		t.Fatalf("update = %d %v", res.StatusCode, updated)
	}
	// Refusals echo what was wrong, never what was sent.
	keep(f.do(t, nil, "POST", "/api/clients", admin, slackBody("docs", "A0DOCS", "helper")))
	keep(f.do(t, nil, "POST", "/api/clients", admin, slackBody("Bad Id", "A0BAD", "helper")))
	keep(f.do(t, nil, "POST", "/api/clients", admin, strings.Replace(slackBody("x", "A0X", "helper"), `"value":"`+botToken+`"`, `"value":"`+botToken+`","ref":"env:COLLEGIUM_X"`, 1)))
	keep(f.do(t, nil, "PUT", "/api/policies", admin, `{"scope":{"kind":"client","name":"docs"},"period":"day","max_concurrent":2}`))
	keep(f.do(t, nil, "GET", "/api/policies", admin, ""))
	keep(f.do(t, nil, "GET", "/api/policies/history", admin, ""))

	// The client works: a request signed with its secret raises a run.
	body := `{"type":"event_callback","api_app_id":"A0DOCS","team_id":"T01ABCDEF","event_id":"Ev1","event":{"type":"app_mention","user":"U01","channel":"C01","ts":"1700000000.000100","text":"explain"}}`
	if code := postSlack(t, f, signingSecret, body); code != http.StatusOK {
		t.Fatalf("webhook answered %d", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(fake.Messages()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the client never answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, list := keep(f.do(t, nil, "GET", "/api/runs", admin, ""))
	first := list["runs"].([]any)[0].(map[string]any)
	if first["client"] != "docs" || first["origin"] != "slack" {
		t.Fatalf("run = %v, want raised by client docs", first)
	}
	keep(f.do(t, nil, "GET", "/api/runs/"+first["id"].(string), admin, ""))
	keep(f.do(t, nil, "GET", "/api/journal", admin, ""))
	keep(f.do(t, nil, "GET", "/api/usage", admin, ""))
	if res, _ := f.do(t, nil, "DELETE", "/api/clients/docs", admin, ""); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", res.StatusCode)
	}

	for _, text := range append(said, logs.String()) {
		for _, secret := range []string{botToken, signingSecret} {
			if strings.Contains(text, secret) {
				t.Fatalf("a secret appeared in: %.300s", text)
			}
		}
	}
	if !strings.Contains(logs.String(), "client created") {
		t.Fatal("the log was not captured, so it was not searched")
	}
}

func postSlack(t *testing.T, f *fixture, secret, body string) int {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + body))
	// A webhook is not a browser and carries no session: its signature is
	// its only credential.
	res, _ := f.do(t, nil, "POST", "/connectors/slack", "", body,
		signature.HeaderSlackTimestamp, ts, signature.HeaderSlackSignature, "v0="+hex.EncodeToString(mac.Sum(nil)))
	return res.StatusCode
}

// Another tenant's client does not exist: every route answers as it does for
// an id nobody has.
func TestAnotherTenantsClientIsNotFound(t *testing.T) {
	f, _, _ := clientFixture(t)
	acme, globex := f.machine(t, "acme", everything...), f.machine(t, "globex", everything...)
	if res, out := f.do(t, nil, "POST", "/api/clients", acme, slackBody("docs", "A0DOCS", "helper")); res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %v", res.StatusCode, out)
	}
	update := `{"name":"Mine now","surface":"slack","identity":{"team":"T01ABCDEF"},"default_agent":"helper"}`
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/clients/docs", ""},
		{"PUT", "/api/clients/docs", update},
		{"DELETE", "/api/clients/docs", ""},
		{"PUT", "/api/policies", `{"scope":{"kind":"client","name":"docs"},"period":"day","max_runs":1}`},
	} {
		theirs, _ := f.do(t, nil, tc.method, tc.path, globex, tc.body)
		nobodys, _ := f.do(t, nil, tc.method, strings.Replace(tc.path, "docs", "nobody", 1), globex, strings.Replace(tc.body, `"docs"`, `"nobody"`, 1))
		if theirs.StatusCode != http.StatusNotFound || nobodys.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s = %d for another tenant's client and %d for nobody's, want 404 for both", tc.method, tc.path, theirs.StatusCode, nobodys.StatusCode)
		}
	}
	_, list := f.do(t, nil, "GET", "/api/clients", globex, "")
	if got := list["clients"].([]any); len(got) != 0 {
		t.Fatalf("globex lists %v", got)
	}
	// The same id and the same Slack identity are free in the other tenant,
	// so creating one says nothing about who else has it.
	if res, out := f.do(t, nil, "POST", "/api/clients", globex, slackBody("docs", "A0DOCS", "helper")); res.StatusCode != http.StatusCreated {
		t.Fatalf("globex create = %d %v", res.StatusCode, out)
	}
	if _, mine := f.do(t, nil, "GET", "/api/clients/docs", acme, ""); mine["created_by"] != "svc-acme" || mine["name"] != "docs" {
		t.Fatalf("acme's client was changed: %v", mine)
	}
}

// Seeing clients and changing them are two permissions, and changing them is
// an admin's.
func TestClientRoutesEachNeedTheirPermission(t *testing.T) {
	f, _, _ := clientFixture(t)
	reader := f.machine(t, "acme", "collegium:clients:read")
	writer := f.machine(t, "acme", "collegium:clients:write")
	other := f.machine(t, "acme", "collegium:agents:write", "collegium:runs:write", "collegium:billing:write")
	body := slackBody("docs", "A0DOCS", "helper")
	for _, tc := range []struct {
		method, path, body, bearer string
		want                       int
	}{
		{"POST", "/api/clients", body, reader, http.StatusForbidden},
		{"POST", "/api/clients", body, other, http.StatusForbidden},
		{"POST", "/api/clients", body, "", http.StatusUnauthorized},
		{"POST", "/api/clients", body, writer, http.StatusCreated},
		{"GET", "/api/clients", "", writer, http.StatusForbidden},
		{"GET", "/api/clients", "", other, http.StatusForbidden},
		{"GET", "/api/clients", "", reader, http.StatusOK},
		{"GET", "/api/clients/docs", "", reader, http.StatusOK},
		{"PUT", "/api/clients/docs", body, reader, http.StatusForbidden},
		{"DELETE", "/api/clients/docs", "", reader, http.StatusForbidden},
		{"DELETE", "/api/clients/docs", "", writer, http.StatusNoContent},
	} {
		if res, out := f.do(t, nil, tc.method, tc.path, tc.bearer, tc.body); res.StatusCode != tc.want {
			t.Errorf("%s %s = %d %v, want %d", tc.method, tc.path, res.StatusCode, out, tc.want)
		}
	}
	// A viewer sees clients and an admin changes them; a viewer does not.
	viewer, admin := f.signIn(t, "collegium-viewers"), f.signIn(t, "collegium-admins")
	if res, _ := f.do(t, viewer, "GET", "/api/clients", "", ""); res.StatusCode != http.StatusOK {
		t.Errorf("viewer list = %d", res.StatusCode)
	}
	if res, _ := f.do(t, viewer, "POST", "/api/clients", "", body, "Origin", f.srv.URL); res.StatusCode != http.StatusForbidden {
		t.Errorf("viewer create = %d", res.StatusCode)
	}
	if res, out := f.do(t, admin, "POST", "/api/clients", "", body, "Origin", f.srv.URL); res.StatusCode != http.StatusCreated {
		t.Errorf("admin create = %d %v", res.StatusCode, out)
	}
}

// The cap on clients is the tenant's policy, enforced on create and shown on
// the list.
func TestTheClientCapIsEnforcedThroughTheAPI(t *testing.T) {
	f, _, _ := clientFixture(t)
	admin := f.machine(t, "acme", everything...)
	_, list := f.do(t, nil, "GET", "/api/clients", admin, "")
	if caps := list["caps"].(map[string]any); caps["max"].(float64) != clients.DefaultMax || caps["max_per_surface"].(float64) != clients.DefaultMaxPerSurface {
		t.Fatalf("default caps = %v", caps)
	}
	if res, out := f.do(t, nil, "PUT", "/api/policies", admin, `{"scope":{"kind":"tenant"},"period":"day","max_clients":2}`); res.StatusCode != http.StatusOK {
		t.Fatalf("set cap = %d %v", res.StatusCode, out)
	}
	for i, want := range []int{http.StatusCreated, http.StatusCreated, http.StatusTooManyRequests} {
		n := strconv.Itoa(i)
		res, out := f.do(t, nil, "POST", "/api/clients", admin, slackBody("app-"+n, "A0APP"+n, "helper"))
		if res.StatusCode != want {
			t.Fatalf("create %d = %d %v, want %d", i, res.StatusCode, out, want)
		}
	}
	_, list = f.do(t, nil, "GET", "/api/clients", admin, "")
	if n := len(list["clients"].([]any)); n != 2 || list["caps"].(map[string]any)["max"].(float64) != 2 {
		t.Fatalf("%d clients, caps %v", n, list["caps"])
	}
	// Only the tenant's policy counts clients, and never past the ceiling.
	for _, body := range []string{
		`{"scope":{"kind":"client","name":"app-0"},"period":"day","max_clients":1}`,
		`{"scope":{"kind":"tenant"},"period":"day","max_clients":101}`,
	} {
		if res, _ := f.do(t, nil, "PUT", "/api/policies", admin, body); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", body, res.StatusCode)
		}
	}
}

// A client's limits are a policy scoped to it, shown with the client and
// removed with it.
func TestAClientsLimitsAreAPolicyScopedToIt(t *testing.T) {
	f, _, _ := clientFixture(t)
	admin := f.machine(t, "acme", everything...)
	f.do(t, nil, "POST", "/api/clients", admin, slackBody("docs", "A0DOCS", "helper"))
	if res, out := f.do(t, nil, "PUT", "/api/policies", admin, `{"scope":{"kind":"client","name":"docs"},"period":"day","max_runs":50,"max_concurrent":2}`); res.StatusCode != http.StatusOK {
		t.Fatalf("set limits = %d %v", res.StatusCode, out)
	}
	_, got := f.do(t, nil, "GET", "/api/clients/docs", admin, "")
	limits, _ := got["limits"].(map[string]any)
	if limits == nil || limits["max_runs"].(float64) != 50 || limits["max_concurrent"].(float64) != 2 {
		t.Fatalf("limits = %v", got["limits"])
	}
	if res, _ := f.do(t, nil, "PUT", "/api/policies", admin, `{"scope":{"kind":"client","name":"docs"},"period":"day","max_storage":10}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a storage limit on a client = %d, want 400", res.StatusCode)
	}
	f.do(t, nil, "DELETE", "/api/clients/docs", admin, "")
	_, pols := f.do(t, nil, "GET", "/api/policies", admin, "")
	if left := pols["policies"].([]any); len(left) != 0 {
		t.Fatalf("a deleted client left its limits behind: %v", left)
	}
}

// A caller cannot say which tenant a client is in or who manages it, and
// cannot change a client the server's configuration owns.
func TestAClientBodyCannotClaimWhatTheServerDecides(t *testing.T) {
	f, _, _ := clientFixture(t)
	admin := f.machine(t, "acme", everything...)
	for _, extra := range []string{`"tenant":"globex"`, `"managed":"env"`, `"version":9`, `"created_by":"root"`} {
		body := strings.Replace(slackBody("docs", "A0DOCS", "helper"), `{"id"`, `{`+extra+`,"id"`, 1)
		if res, _ := f.do(t, nil, "POST", "/api/clients", admin, body); res.StatusCode != http.StatusBadRequest {
			t.Errorf("a body with %s = %d, want 400", extra, res.StatusCode)
		}
	}
	in := platform.ClientInput{
		Client: clients.Client{
			ID: "default-slack", Name: "Slack", Surface: clients.Slack, Enabled: true, Managed: clients.ManagedEnv,
			Identity: clients.Identity{Team: "T01ABCDEF"}, Agents: []string{clients.AnyAgent},
		},
		Token: clients.SecretInput{Value: botToken}, SigningSecret: clients.SecretInput{Value: signingSecret},
	}
	if _, err := f.p.CreateClient(context.Background(), "acme", "environment", in); err != nil {
		t.Fatal(err)
	}
	_, got := f.do(t, nil, "GET", "/api/clients/default-slack", admin, "")
	if got["managed"] != "env" {
		t.Fatalf("client = %v", got)
	}
	update := `{"name":"Mine","surface":"slack","identity":{"team":"T01ABCDEF"},"default_agent":"helper","enabled":false}`
	if res, _ := f.do(t, nil, "PUT", "/api/clients/default-slack", admin, update); res.StatusCode != http.StatusConflict {
		t.Errorf("update of a managed client = %d, want 409", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "DELETE", "/api/clients/default-slack", admin, ""); res.StatusCode != http.StatusConflict {
		t.Errorf("delete of a managed client = %d, want 409", res.StatusCode)
	}
}

// Through the API, a secret reference is refused unless the server gave that
// variable to the caller's tenant, and every refusal is the same 400 with the
// same body: unset, set, another tenant's, or the default client's.
func TestTheAPIRefusesASecretReferenceItsTenantWasNotGiven(t *testing.T) {
	env := map[string]string{
		"COLLEGIUM_SLACK_TOKEN": "xoxb-default", "COLLEGIUM_SLACK_SIGNING_SECRET": "default-signing",
		"COLLEGIUM_SLACK_TOKEN_ACME": botToken, "COLLEGIUM_SLACK_SECRET_ACME": signingSecret,
	}
	fake := slackflow.NewFake()
	f := newFixtureWith(t, platform.Config{
		SlackAPI: func(string) slackflow.API { return fake }, ClientInterval: -1,
		Env: func(name string) (string, bool) { v, ok := env[name]; return v, ok },
		SecretRefs: map[string][]string{
			"acme": {"COLLEGIUM_SLACK_TOKEN", "COLLEGIUM_SLACK_SIGNING_SECRET", "COLLEGIUM_SLACK_TOKEN_ACME", "COLLEGIUM_SLACK_SECRET_ACME"},
		},
	})
	acme, globex := f.machine(t, "acme", everything...), f.machine(t, "globex", everything...)
	withRefs := func(id, app, token, signing string) string {
		return `{"id":"` + id + `","name":"` + id + `","surface":"slack","identity":{"app_id":"` + app + `","team":"T01ABCDEF"},"default_agent":"helper",` +
			`"credentials":{"token":{"ref":"` + token + `"},"signing_secret":{"ref":"` + signing + `"}}}`
	}
	// The tenant the variables were given to may reference them.
	if res, out := f.do(t, nil, "POST", "/api/clients", acme, withRefs("mine", "A0MINE", "env:COLLEGIUM_SLACK_TOKEN_ACME", "env:COLLEGIUM_SLACK_SECRET_ACME")); res.StatusCode != http.StatusCreated {
		t.Fatalf("acme create = %d %v", res.StatusCode, out)
	}
	f.do(t, nil, "POST", "/api/agents", acme, helperSpec)
	body := `{"type":"event_callback","api_app_id":"A0MINE","team_id":"T01ABCDEF","event_id":"Ev1","event":{"type":"app_mention","user":"U01","channel":"C01","ts":"1700000000.000100","text":"explain"}}`
	if code := postSlack(t, f, signingSecret, body); code != http.StatusOK {
		t.Fatalf("a webhook signed with the referenced secret answered %d", code)
	}

	// Nobody else may, whatever the variable is.
	bodies := map[string]bool{}
	for name, ref := range map[string]string{
		"another tenant's variable":     "env:COLLEGIUM_SLACK_TOKEN_ACME",
		"the default client's variable": "env:COLLEGIUM_SLACK_TOKEN",
		"a variable that is not set":    "env:COLLEGIUM_NOT_SET",
		"a variable outside the prefix": "env:PATH",
	} {
		res, out := f.do(t, nil, "POST", "/api/clients", globex, withRefs("probe", "A0PROBE", ref, ref))
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d %v, want 400", name, res.StatusCode, out)
		}
		raw, _ := json.Marshal(out)
		bodies[string(raw)] = true
	}
	if len(bodies) != 1 {
		t.Fatalf("refusals can be told apart: %v", bodies)
	}
	// An update cannot move a client onto them either.
	if res, out := f.do(t, nil, "POST", "/api/clients", globex, slackBody("theirs", "A0THEIRS", "helper")); res.StatusCode != http.StatusCreated {
		t.Fatalf("globex create = %d %v", res.StatusCode, out)
	}
	update := strings.Replace(withRefs("theirs", "A0THEIRS", "env:COLLEGIUM_SLACK_TOKEN", "env:COLLEGIUM_SLACK_SIGNING_SECRET"), `"id":"theirs",`, "", 1)
	res, out := f.do(t, nil, "PUT", "/api/clients/theirs", globex, update)
	raw, _ := json.Marshal(out)
	if res.StatusCode != http.StatusBadRequest || !bodies[string(raw)] {
		t.Fatalf("update = %d %s, want the same refusal", res.StatusCode, raw)
	}
	_, kept := f.do(t, nil, "GET", "/api/clients/theirs", globex, "")
	if kept["version"].(float64) != 1 {
		t.Fatalf("a refused update changed the client: %v", kept)
	}
	_, list := f.do(t, nil, "GET", "/api/clients", globex, "")
	if n := len(list["clients"].([]any)); n != 1 {
		t.Fatalf("globex has %d clients, want only its own", n)
	}
}
