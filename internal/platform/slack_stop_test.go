package platform_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	saigetypes "github.com/urmzd/saige/agent/types"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/integrations/slack"
	"github.com/urmzd/collegium/pkg/runs"
	"github.com/urmzd/mandatum/pkg/registry"
	"github.com/urmzd/mandatum/pkg/signature"
)

const slackSecret = "8f742231b10e8888abcd99yyyzzz85a5"

// slackPost signs a body the way Slack does and serves it on the ingress.
func slackPost(t *testing.T, h http.Handler, body string) int {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(slackSecret))
	mac.Write([]byte("v0:" + ts + ":" + body))
	r := httptest.NewRequest(http.MethodPost, "/connectors/slack", strings.NewReader(body))
	r.Header.Set(signature.HeaderSlackTimestamp, ts)
	r.Header.Set(signature.HeaderSlackSignature, "v0="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never happened: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Pressing stop on a Slack session stops the run answering in it, says so in
// the thread, takes the session off "Working", and keeps the answer the model
// was still producing off the surface.
func TestStoppingASlackSessionStopsItsRun(t *testing.T) {
	fake := slack.NewFake()
	peer, err := slack.New(slack.Config{
		API: fake, Verifier: signature.NewSlack(slackSecret), Team: "T01ABCDEF",
		DefaultAgent: "lead", Sessions: true, Interval: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &cast{
		says:  map[string][][]saigetypes.Delta{"lead": {say("Too late.")}},
		gates: map[string]chan struct{}{"lead": make(chan struct{})},
	}
	p := stage(t, c, platform.Config{Connections: func(registry.Store) ([]platform.Connection, error) {
		return []platform.Connection{{Tenant: tenant, Peer: peer}}, nil
	}})
	ingress := p.IngressHandler()
	eventually(t, "the slack trigger is ingesting", peer.Ingesting)

	const thread = "1700000000.000100"
	mention := `{"type":"event_callback","team_id":"T01ABCDEF","event_id":"Ev1","event":{"type":"app_mention",` +
		`"user":"U0HUMAN1","text":"<@U08BOTID> start","ts":"1700000090.001200","thread_ts":"` + thread + `","channel":"C02CHANNEL"}}`
	if code := slackPost(t, ingress, mention); code != http.StatusOK {
		t.Fatalf("mention answered %d", code)
	}

	var run runs.Run
	eventually(t, "the mention's run is in flight", func() bool {
		for _, r := range p.Runs.List(tenant, 10) {
			if r.Agent == "lead" && r.Status == runs.Running {
				run = r
				return true
			}
		}
		return false
	})
	eventually(t, "slack shows the session working", func() bool {
		s := fake.Statuses()
		return len(s) > 0 && s[len(s)-1].Status == slack.StatusProcessing
	})

	stop := func(team string) string {
		return `{"type":"event_callback","team_id":"` + team + `","event_id":"Ev2","event":{"type":"agent_session_stopped",` +
			`"user":"U0HUMAN2","channel":"C02CHANNEL","thread_ts":"` + thread + `","streaming_message_ts":[]}}`
	}
	// Another workspace's stop is acknowledged and reaches nothing.
	if code := slackPost(t, ingress, stop("T09OTHER")); code != http.StatusOK {
		t.Fatalf("a foreign stop answered %d", code)
	}
	if r, _ := p.Runs.Get(tenant, run.ID); r.Status != runs.Running {
		t.Fatalf("another workspace stopped the run: %s", r.Status)
	}

	if code := slackPost(t, ingress, stop("T01ABCDEF")); code != http.StatusOK {
		t.Fatalf("stop answered %d", code)
	}
	got, _ := p.Runs.Get(tenant, run.ID)
	if got.Status != runs.Stopped || got.Cause != "Stopped by slack:U0HUMAN2" {
		t.Fatalf("run = %s (%q), want stopped by the slack user", got.Status, got.Cause)
	}
	statuses := fake.Statuses()
	if last := statuses[len(statuses)-1]; last.Status != slack.StatusActive || last.ThreadTS != thread {
		t.Fatalf("session = %+v, want active in the thread", last)
	}
	confirmed := false
	for _, m := range fake.Messages() {
		confirmed = confirmed || (m.ThreadTS == thread && m.Text == "Stopped by <@U0HUMAN2>.")
	}
	if !confirmed {
		t.Fatalf("nobody was told the run stopped: %+v", fake.Messages())
	}

	// Slack redelivers; a second stop is a no-op, not a second confirmation.
	if code := slackPost(t, ingress, stop("T01ABCDEF")); code != http.StatusOK {
		t.Fatalf("a repeated stop answered %d", code)
	}

	// The held model call finishes now. None of it reaches Slack.
	c.release("lead")
	time.Sleep(300 * time.Millisecond)
	confirmations := 0
	for _, m := range fake.Messages() {
		if strings.Contains(m.Text, "Too late.") {
			t.Fatalf("a stopped run's answer was delivered: %q", m.Text)
		}
		if strings.HasPrefix(m.Text, "Stopped by") {
			confirmations++
		}
	}
	if confirmations != 1 {
		t.Fatalf("%d stop confirmations, want 1", confirmations)
	}
	statuses = fake.Statuses()
	if last := statuses[len(statuses)-1]; last.Status != slack.StatusActive {
		t.Fatalf("session went back to %s after the stop", last.Status)
	}
}
