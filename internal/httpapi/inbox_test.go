package httpapi

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/urmzd/open-agent-platform/pkg/inbox"
)

func messages(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	var list []map[string]any
	for _, m := range out["messages"].([]any) {
		list = append(list, m.(map[string]any))
	}
	return list
}

// refusals keeps the messages about a run being refused. Nearing a limit
// raises a quieter message of its own, which these checks are not about.
func refusals(list []map[string]any) []map[string]any {
	var out []map[string]any
	for _, m := range list {
		if m["severity"] == "critical" {
			out = append(out, m)
		}
	}
	return out
}

func part(t *testing.T, m map[string]any, kind string) map[string]any {
	t.Helper()
	for _, p := range m["parts"].([]any) {
		if p := p.(map[string]any); p["type"] == kind {
			return p
		}
	}
	t.Fatalf("no %s part in %v", kind, m)
	return nil
}

func actions(p map[string]any) []string {
	out := []string{}
	for _, a := range p["actions"].([]any) {
		out = append(out, a.(string))
	}
	return out
}

// A refused run lands in the inbox as one message that can be dealt with
// there. Who may do what follows the thing being done, an alert and its
// message are one state, and the record rebuilds what the inbox shows.
func TestInboxResolvesWhatItHolds(t *testing.T) {
	f := newFixture(t)
	admin := f.machine(t, "acme", everything...)
	// These three are the same caller with fewer rights each time.
	reader := f.machine(t, "acme", "oap:runs:read", "oap:alerts:read")
	acker := f.machine(t, "acme", "oap:runs:read", "oap:alerts:read", "oap:alerts:write")
	blind := f.machine(t, "acme", "oap:runs:read")
	globex := f.machine(t, "globex", everything...)

	f.do(t, nil, "POST", "/api/agents", admin, helperSpec)
	f.do(t, nil, "POST", "/api/projects", admin, `{"name":"Launch"}`)
	f.do(t, nil, "PUT", "/api/policies", admin, `{"scope":{"kind":"project","name":"Launch"},"period":"day","max_runs":1}`)
	run := func() int {
		res, _ := f.do(t, nil, "POST", "/api/runs", admin, `{"agent":"helper","text":"go","project":"Launch"}`)
		return res.StatusCode
	}
	if run() != http.StatusAccepted || run() != http.StatusTooManyRequests || run() != http.StatusTooManyRequests {
		t.Fatal("the limit did not refuse the second and third runs")
	}

	// Refused twice, said once.
	_, out := f.do(t, nil, "GET", "/api/inbox?kind=budget", admin, "")
	list := refusals(messages(t, out))
	if len(list) != 1 || out["open"].(float64) < 1 || out["next"] != nil {
		t.Fatalf("inbox = %v", out)
	}
	msg := list[0]
	id := msg["id"].(string)
	if msg["kind"] != "budget" || msg["state"] != "open" || msg["severity"] != "critical" || msg["unread"] != true {
		t.Fatalf("message = %v", msg)
	}
	alertID := msg["source"].(map[string]any)["alert"].(string)
	budget := part(t, msg, "budget")
	if got := actions(budget); !reflect.DeepEqual(got, []string{"raise_limit", "acknowledge"}) {
		t.Fatalf("an admin is offered %v on the budget part", got)
	}
	if b := budget["budget"].(map[string]any); b["scope_name"] != "Launch" || b["limit"] != "runs" || b["current"].(map[string]any)["max_runs"].(float64) != 1 {
		t.Fatalf("budget part = %v", b)
	}
	budgetPart, alertPart := budget["id"].(string), part(t, msg, "alert")["id"].(string)

	// What a caller may not see is not there for them, in any tenant.
	if _, out := f.do(t, nil, "GET", "/api/inbox", globex, ""); len(out["messages"].([]any)) != 0 {
		t.Fatal("another tenant sees the message")
	}
	for name, tok := range map[string]string{"another tenant": globex, "a caller who cannot read alerts": blind} {
		if res, _ := f.do(t, nil, "GET", "/api/inbox/"+id, tok, ""); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s read the message: %d", name, res.StatusCode)
		}
		if res, _ := f.do(t, nil, "POST", "/api/inbox/"+id+"/parts/"+alertPart+"/resolve", tok, `{"action":"acknowledge"}`); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s resolved a part: %d", name, res.StatusCode)
		}
	}
	if _, out := f.do(t, nil, "GET", "/api/inbox/count", blind, ""); out["open"].(float64) != 0 {
		t.Fatal("a caller who cannot read alerts counts one")
	}

	// A reader sees it and is offered nothing. Each action needs its own right.
	_, seen := f.do(t, nil, "GET", "/api/inbox/"+id, reader, "")
	if got := actions(part(t, seen["message"].(map[string]any), "budget")); len(got) != 0 {
		t.Fatalf("a reader is offered %v", got)
	}
	_, seen = f.do(t, nil, "GET", "/api/inbox/"+id, acker, "")
	if got := actions(part(t, seen["message"].(map[string]any), "budget")); !reflect.DeepEqual(got, []string{"acknowledge"}) {
		t.Fatalf("without billing rights the offer is %v", got)
	}
	for name, c := range map[string]struct{ tok, path, body string }{
		"a reader acknowledging":                 {reader, "/parts/" + alertPart + "/resolve", `{"action":"acknowledge"}`},
		"a reader dismissing":                    {reader, "/dismiss", `{}`},
		"a reader snoozing":                      {reader, "/snooze", `{"until":"2999-01-01T00:00:00Z"}`},
		"raising a limit without billing rights": {acker, "/parts/" + budgetPart + "/resolve", `{"action":"raise_limit","limits":{"max_runs":5}}`},
	} {
		if res, _ := f.do(t, nil, "POST", "/api/inbox/"+id+c.path, c.tok, c.body); res.StatusCode != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", name, res.StatusCode)
		}
	}
	for name, body := range map[string]string{
		"an action the part does not take": `{"action":"keep"}`,
		"raising a limit to nothing":       `{"action":"raise_limit"}`,
	} {
		if res, _ := f.do(t, nil, "POST", "/api/inbox/"+id+"/parts/"+budgetPart+"/resolve", admin, body); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, res.StatusCode)
		}
	}
	if res, _ := f.do(t, nil, "POST", "/api/inbox/"+id+"/parts/p99/resolve", admin, `{"action":"acknowledge"}`); res.StatusCode != http.StatusNotFound {
		t.Errorf("a part that is not there = %d", res.StatusCode)
	}
	// None of that changed anything.
	if _, still := f.do(t, nil, "GET", "/api/inbox/"+id, admin, ""); still["message"].(map[string]any)["state"] != "open" || len(still["history"].([]any)) != 1 {
		t.Fatalf("refused attempts left a mark: %v", still)
	}

	// Reading is the reader's own and settles nothing.
	if _, read := f.do(t, nil, "POST", "/api/inbox/"+id+"/read", admin, `{"read":true}`); read["unread"] != false || read["state"] != "open" {
		t.Fatalf("after reading: %v", read)
	}

	// Raising the limit in the inbox changes the policy, acknowledges the
	// alert, and resolves the message.
	res, done := f.do(t, nil, "POST", "/api/inbox/"+id+"/parts/"+budgetPart+"/resolve", admin, `{"action":"raise_limit","limits":{"max_runs":3}}`)
	if res.StatusCode != http.StatusOK || done["state"] != "resolved" {
		t.Fatalf("raise = %d %v", res.StatusCode, done)
	}
	if o := part(t, done, "budget")["outcome"].(map[string]any); o["action"] != "raise_limit" || o["by"] != "svc-acme" || o["note"] != "Now version 2" {
		t.Fatalf("outcome = %v", o)
	}
	if len(actions(part(t, done, "alert"))) != 0 {
		t.Fatal("a resolved message still offers actions")
	}
	_, alertsOut := f.do(t, nil, "GET", "/api/alerts", admin, "")
	for _, a := range alertsOut["alerts"].([]any) {
		if a := a.(map[string]any); a["id"] == alertID && a["acked_by"] != "svc-acme" {
			t.Fatalf("the alert is still open after its message was resolved: %v", a)
		}
	}
	if run() != http.StatusAccepted {
		t.Fatal("the raised limit did not let a run in")
	}

	// Doing it again does nothing and answers with the first outcome.
	res, again := f.do(t, nil, "POST", "/api/inbox/"+id+"/parts/"+budgetPart+"/resolve", admin, `{"action":"raise_limit","limits":{"max_runs":50}}`)
	if res.StatusCode != http.StatusOK || part(t, again, "budget")["outcome"].(map[string]any)["note"] != "Now version 2" {
		t.Fatalf("a second resolve = %d %v", res.StatusCode, again)
	}
	_, pols := f.do(t, nil, "GET", "/api/policies", admin, "")
	if p := pols["policies"].([]any)[0].(map[string]any); p["max_runs"].(float64) != 3 || p["version"].(float64) != 2 {
		t.Fatalf("the second resolve changed the policy: %v", p)
	}
	if res, _ := f.do(t, nil, "POST", "/api/inbox/"+id+"/dismiss", admin, `{}`); res.StatusCode != http.StatusOK {
		t.Fatalf("dismissing a resolved message = %d", res.StatusCode)
	}

	// The other direction: acknowledging the alert where alerts are listed
	// resolves its message.
	for run() == http.StatusAccepted {
	}
	_, out = f.do(t, nil, "GET", "/api/inbox?kind=budget", admin, "")
	list = refusals(messages(t, out))
	if len(list) != 1 || list[0]["id"] == id {
		t.Fatalf("a new refusal raised %v", list)
	}
	second := list[0]["id"].(string)
	secondAlert := list[0]["source"].(map[string]any)["alert"].(string)
	if res, _ := f.do(t, nil, "POST", "/api/alerts/"+secondAlert+"/ack", acker, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("ack = %d", res.StatusCode)
	}
	if _, out := f.do(t, nil, "GET", "/api/inbox?alert="+secondAlert, admin, ""); len(messages(t, out)) != 1 || messages(t, out)[0]["id"] != second {
		t.Fatalf("looking a message up by its alert = %v", out["messages"])
	}
	_, got := f.do(t, nil, "GET", "/api/inbox/"+second, admin, "")
	m := got["message"].(map[string]any)
	if m["state"] != "resolved" || part(t, m, "alert")["outcome"].(map[string]any)["action"] != "acknowledge" {
		t.Fatalf("after acknowledging the alert its message is %v", m)
	}
	if _, out := f.do(t, nil, "GET", "/api/inbox", admin, ""); len(refusals(messages(t, out))) != 0 {
		t.Fatalf("the open page still holds %v", out["messages"])
	}
	if _, out := f.do(t, nil, "GET", "/api/inbox?state=resolved", admin, ""); len(refusals(messages(t, out))) != 2 {
		t.Fatalf("the resolved page = %v", out["messages"])
	}
	if res, _ := f.do(t, nil, "GET", "/api/inbox?state=sideways", admin, ""); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a made-up state = %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "GET", "/api/inbox?before=not-a-cursor!", admin, ""); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a made-up cursor = %d", res.StatusCode)
	}

	// Everything above is in the record, and the record rebuilds the inbox.
	live, liveRead := f.p.Inbox.Snapshot("acme")
	rebuilt, rebuiltRead := inbox.Rebuild(f.p.Inbox.Changes("acme")).Snapshot("acme")
	if !reflect.DeepEqual(live, rebuilt) || !reflect.DeepEqual(liveRead, rebuiltRead) {
		t.Fatalf("rebuilt inbox differs\nlive %+v\nrebuilt %+v", live, rebuilt)
	}
}
