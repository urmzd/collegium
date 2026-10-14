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

// Pressing stop on a Slack session interrupts the model call of the run
// answering in it, says so in the thread and takes the session off "Working".
// The message keeps what the agent had said, marked stopped, and the session
// ends active however late the run's own events arrive. A follow-up mention
// in the thread is answered.
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
		says:  map[string][][]saigetypes.Delta{"lead": {say("Picked up again.")}},
		talks: map[string]talk{"lead": {after: 0, text: "Partial answer so far"}},
	}
	p := stage(t, c, platform.Config{Connections: func(registry.Store) ([]platform.Connection, error) {
		return []platform.Connection{{Tenant: tenant, Peer: peer}}, nil
	}})
	ingress := p.IngressHandler()
	eventually(t, "the slack trigger is ingesting", peer.Ingesting)

	const thread = "1700000000.000100"
	mention := func(event, ts string) string {
		return `{"type":"event_callback","team_id":"T01ABCDEF","event_id":"` + event + `","event":{"type":"app_mention",` +
			`"user":"U0HUMAN1","text":"<@U08BOTID> start","ts":"` + ts + `","thread_ts":"` + thread + `","channel":"C02CHANNEL"}}`
	}
	if code := slackPost(t, ingress, mention("Ev1", "1700000090.001200")); code != http.StatusOK {
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
	answer := func() (slack.FakeMessage, bool) {
		for _, m := range fake.Messages() {
			if strings.Contains(m.Text, "Partial answer so far") {
				return m, true
			}
		}
		return slack.FakeMessage{}, false
	}
	eventually(t, "the partial answer is on the surface", func() bool { _, ok := answer(); return ok })
	eventually(t, "slack shows the session working", func() bool {
		s := fake.Statuses()
		return len(s) > 0 && s[len(s)-1].Status == slack.StatusProcessing
	})

	stop := func(team, event string) string {
		return `{"type":"event_callback","team_id":"` + team + `","event_id":"` + event + `","event":{"type":"agent_session_stopped",` +
			`"user":"U0HUMAN2","channel":"C02CHANNEL","thread_ts":"` + thread + `","streaming_message_ts":[]}}`
	}
	// Another workspace's stop is acknowledged and reaches nothing.
	if code := slackPost(t, ingress, stop("T09OTHER", "Ev2")); code != http.StatusOK {
		t.Fatalf("a foreign stop answered %d", code)
	}
	if r, _ := p.Runs.Get(tenant, run.ID); r.Status != runs.Running || r.Stopping || c.interrupted("lead") {
		t.Fatalf("another workspace stopped the run: %s, stopping %v", r.Status, r.Stopping)
	}

	pressed := len(fake.Statuses())
	if code := slackPost(t, ingress, stop("T01ABCDEF", "Ev2")); code != http.StatusOK {
		t.Fatalf("stop answered %d", code)
	}
	// The model call was cancelled, not left to finish unheard.
	eventually(t, "the model call was interrupted", func() bool { return c.interrupted("lead") })
	got := await(t, p, run.ID, runs.Stopped)
	if got.Cause != "Stopped by slack:U0HUMAN2" || got.Answer != "Partial answer so far" {
		t.Fatalf("run = %q (%q), want the partial answer stopped by the slack user", got.Answer, got.Cause)
	}
	// What the agent had said stays on the surface, marked stopped.
	eventually(t, "the run's message says it stopped", func() bool {
		m, ok := answer()
		return ok && strings.Contains(m.Text, "stopped") && strings.Contains(m.Text, "Stopped by slack:U0HUMAN2")
	})
	confirmed := false
	for _, m := range fake.Messages() {
		confirmed = confirmed || (m.ThreadTS == thread && m.Text == "Stopped by <@U0HUMAN2>.")
	}
	if !confirmed {
		t.Fatalf("nobody was told the run stopped: %+v", fake.Messages())
	}

	// Slack redelivers; a second stop is a no-op, not a second confirmation.
	if code := slackPost(t, ingress, stop("T01ABCDEF", "Ev2")); code != http.StatusOK {
		t.Fatalf("a repeated stop answered %d", code)
	}
	time.Sleep(200 * time.Millisecond)
	confirmations := 0
	for _, m := range fake.Messages() {
		if strings.HasPrefix(m.Text, "Stopped by") {
			confirmations++
		}
	}
	if confirmations != 1 {
		t.Fatalf("%d stop confirmations, want 1", confirmations)
	}
	// From the press on, the session is never set back to working.
	statuses := fake.Statuses()
	for _, s := range statuses[pressed:] {
		if s.Status != slack.StatusActive {
			t.Fatalf("after the stop the session went to %s: %+v", s.Status, statuses[pressed:])
		}
	}
	if last := statuses[len(statuses)-1]; last.Status != slack.StatusActive || last.ThreadTS != thread {
		t.Fatalf("session = %+v, want active in the thread", last)
	}

	// The thread is still a conversation: a follow-up mention is answered.
	// Slack mentions carry no history in collegium today, so the follow-up is
	// a run of its own that is not sent the stopped turn.
	if code := slackPost(t, ingress, mention("Ev3", "1700000095.001300")); code != http.StatusOK {
		t.Fatalf("follow-up answered %d", code)
	}
	eventually(t, "the follow-up is answered", func() bool {
		for _, m := range fake.Messages() {
			if strings.Contains(m.Text, "Picked up again.") {
				return true
			}
		}
		return false
	})
	eventually(t, "the session ends active", func() bool {
		s := fake.Statuses()
		return s[len(s)-1].Status == slack.StatusActive
	})
}
