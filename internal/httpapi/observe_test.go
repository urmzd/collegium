package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

var everything = []string{
	"oap:agents:read", "oap:agents:write", "oap:runs:read", "oap:runs:write",
	"oap:schedules:read", "oap:schedules:write", "oap:alerts:read", "oap:alerts:write",
	"oap:knowledge:read", "oap:knowledge:write", "oap:evals:read", "oap:evals:write",
}

func TestObservabilityIsConfinedToATenant(t *testing.T) {
	f := newFixture(t)
	acme, globex := f.machine(t, "acme", everything...), f.machine(t, "globex", everything...)

	f.do(t, nil, "POST", "/api/agents", acme, helperSpec)
	_, run := f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"hi"}`)
	id := run["id"].(string)
	if res, _ := f.do(t, nil, "POST", "/api/alert-rules", acme, `{"name":"any","kind":"run_failed","severity":"critical"}`); res.StatusCode != http.StatusCreated {
		t.Fatalf("create rule = %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "POST", "/api/knowledge", acme, `{"name":"handbook"}`); res.StatusCode != http.StatusCreated {
		t.Fatalf("create source = %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "POST", "/api/knowledge/handbook/documents", acme, `{"title":"Releases","text":"The release bot tags every release."}`); res.StatusCode != http.StatusCreated {
		t.Fatalf("add document = %d", res.StatusCode)
	}

	// The run's own tenant can see it all, once the run has had time to end.
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, out := f.do(t, nil, "GET", "/api/journal?kind=lifecycle.completed", acme, "")
		if len(out["entries"].([]any)) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("journal never showed the run completing: %v", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, trace := f.do(t, nil, "GET", "/api/runs/"+id+"/trace", acme, "")
	if len(trace["spans"].([]any)) == 0 {
		t.Fatalf("trace has no spans: %v", trace)
	}
	_, stats := f.do(t, nil, "GET", "/api/analytics?window=1h", acme, "")
	if stats["total"].(map[string]any)["runs"].(float64) != 1 {
		t.Fatalf("analytics = %v", stats["total"])
	}
	_, hits := f.do(t, nil, "GET", "/api/knowledge/search?q=release+bot", acme, "")
	if len(hits["hits"].([]any)) != 1 {
		t.Fatalf("search = %v", hits)
	}

	// The other tenant sees none of it.
	for path, key := range map[string]string{
		"/api/journal":                    "entries",
		"/api/alert-rules":                "rules",
		"/api/alerts":                     "alerts",
		"/api/knowledge":                  "sources",
		"/api/knowledge/search?q=release": "hits",
	} {
		_, out := f.do(t, nil, "GET", path, globex, "")
		if got := out[key].([]any); len(got) != 0 {
			t.Errorf("other tenant GET %s returned %v", path, got)
		}
	}
	_, other := f.do(t, nil, "GET", "/api/analytics?window=1h", globex, "")
	if other["total"].(map[string]any)["runs"].(float64) != 0 {
		t.Errorf("other tenant's analytics counted the run: %v", other["total"])
	}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/runs/" + id + "/trace"},
		{"GET", "/api/knowledge/handbook/documents"},
		{"GET", "/api/knowledge/search?q=release&source=handbook"},
		{"POST", "/api/knowledge/handbook/documents"},
		{"DELETE", "/api/knowledge/handbook"},
		{"DELETE", "/api/alert-rules/any"},
		{"POST", "/api/alerts/al-1/ack"},
	} {
		body := ""
		if tc.method == "POST" {
			body = `{"title":"x","text":"y"}`
		}
		if res, _ := f.do(t, nil, tc.method, tc.path, globex, body); res.StatusCode != http.StatusNotFound {
			t.Errorf("other tenant %s %s = %d, want 404", tc.method, tc.path, res.StatusCode)
		}
	}
}

func TestObservabilityPermissions(t *testing.T) {
	f := newFixture(t)
	viewer := f.signIn(t, "oap-viewers")
	for _, tc := range []struct{ path, body string }{
		{"/api/alert-rules", `{"name":"any","kind":"run_failed","severity":"critical"}`},
		{"/api/knowledge", `{"name":"handbook"}`},
		{"/api/schedules/nightly/pause", ``},
		{"/api/schedules/nightly/run", ``},
	} {
		if res, _ := f.do(t, viewer, "POST", tc.path, "", tc.body); res.StatusCode != http.StatusForbidden {
			t.Errorf("viewer POST %s = %d, want 403", tc.path, res.StatusCode)
		}
	}
	for _, path := range []string{"/api/alerts", "/api/alert-rules", "/api/knowledge", "/api/analytics", "/api/journal"} {
		if res, _ := f.do(t, viewer, "GET", path, "", ""); res.StatusCode != http.StatusOK {
			t.Errorf("viewer GET %s = %d, want 200", path, res.StatusCode)
		}
	}
	if res, _ := f.do(t, viewer, "GET", "/api/analytics?window=9999h", "", ""); res.StatusCode != http.StatusBadRequest {
		t.Errorf("an unbounded window = %d, want 400", res.StatusCode)
	}
}

func TestScheduleLifecycle(t *testing.T) {
	f := newFixture(t)
	tok := f.machine(t, "acme", everything...)
	f.do(t, nil, "POST", "/api/agents", tok, helperSpec)
	res, sc := f.do(t, nil, "POST", "/api/schedules", tok, `{"name":"nightly","expression":"0 2 * * *","agent":"helper","text":"report"}`)
	if res.StatusCode != http.StatusCreated || len(sc["next"].([]any)) != 3 || sc["paused"] != false {
		t.Fatalf("create = %d %v", res.StatusCode, sc)
	}

	_, paused := f.do(t, nil, "POST", "/api/schedules/nightly/pause", tok, "")
	if paused["paused"] != true || len(paused["next"].([]any)) != 0 {
		t.Fatalf("a paused schedule still shows upcoming runs: %v", paused)
	}

	// Run now works while paused, and the run is tied back to the schedule.
	res, run := f.do(t, nil, "POST", "/api/schedules/nightly/run", tok, "")
	if res.StatusCode != http.StatusAccepted || run["schedule"] != "nightly" || run["origin"] != "manual" {
		t.Fatalf("run now = %d %v", res.StatusCode, run)
	}
	_, list := f.do(t, nil, "GET", "/api/schedules", tok, "")
	first := list["schedules"].([]any)[0].(map[string]any)
	if len(first["recent"].([]any)) != 1 {
		t.Fatalf("the schedule does not list its run: %v", first)
	}

	_, resumed := f.do(t, nil, "POST", "/api/schedules/nightly/resume", tok, "")
	if resumed["paused"] != false || len(resumed["next"].([]any)) != 3 {
		t.Fatalf("resume = %v", resumed)
	}
}

// lastRun polls a suite until its newest run has ended.
func (f *fixture) lastRun(t *testing.T, tok, suite string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, out := f.do(t, nil, "GET", "/api/evals/"+suite+"/runs", tok, "")
		if runs, _ := out["runs"].([]any); len(runs) > 0 {
			if run := runs[0].(map[string]any); run["status"] != "running" {
				return run
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("eval %s never finished: %v", suite, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAgentEval(t *testing.T) {
	f := newFixture(t)
	tok := f.machine(t, "acme", everything...)
	f.do(t, nil, "POST", "/api/agents", tok, helperSpec)
	f.do(t, nil, "POST", "/api/alert-rules", tok, `{"name":"quality","kind":"eval_below","agent":"helper","threshold":100,"severity":"warning"}`)

	// The offline provider names itself in its answer, so one case can pass
	// and one can fail without a model.
	suite := `{"name":"smoke","target":"agent","subject":"helper","cases":[
		{"id":"says-offline","input":"hello","checks":[{"kind":"contains","value":"offline provider"}]},
		{"id":"claims-a-model","input":"hello","checks":[{"kind":"contains","value":"I am a large language model"}]}
	]}`
	if res, out := f.do(t, nil, "POST", "/api/evals", tok, suite); res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %v", res.StatusCode, out)
	}
	res, started := f.do(t, nil, "POST", "/api/evals/smoke/run", tok, "")
	if res.StatusCode != http.StatusAccepted || started["rev"].(float64) != 1 {
		t.Fatalf("run = %d %v", res.StatusCode, started)
	}

	run := f.lastRun(t, tok, "smoke")
	if run["status"] != "failed" || run["passed"].(float64) != 1 || run["pass_rate"].(float64) != 50 {
		t.Fatalf("run = %v", run)
	}
	results := run["results"].([]any)
	first, second := results[0].(map[string]any), results[1].(map[string]any)
	if first["passed"] != true || second["passed"] != false || first["run_id"] == "" || first["output"] == "" {
		t.Fatalf("results = %v", results)
	}

	// The case's run is a real run, tagged as coming from an eval.
	_, agentRun := f.do(t, nil, "GET", "/api/runs/"+first["run_id"].(string), tok, "")
	if agentRun["origin"] != "eval" {
		t.Fatalf("the case's run has origin %v", agentRun["origin"])
	}
	// Falling under the threshold raised the alert.
	_, alerts := f.do(t, nil, "GET", "/api/alerts", tok, "")
	if list := alerts["alerts"].([]any); len(list) != 1 || list[0].(map[string]any)["suite"] != "smoke" {
		t.Fatalf("alerts = %v", alerts)
	}
}

func TestKnowledgeEval(t *testing.T) {
	f := newFixture(t)
	tok := f.machine(t, "acme", everything...)
	f.do(t, nil, "POST", "/api/knowledge", tok, `{"name":"handbook"}`)
	f.do(t, nil, "POST", "/api/knowledge/handbook/documents", tok, `{"title":"Releases","text":"The release bot tags every release."}`)
	f.do(t, nil, "POST", "/api/knowledge/handbook/documents", tok, `{"title":"On call","text":"The on-call engineer acknowledges a page in five minutes."}`)

	suite := `{"name":"retrieval","target":"knowledge","subject":"handbook","k":1,"cases":[
		{"id":"release","input":"who tags a release","expect":["Releases"]},
		{"id":"paging","input":"how fast must a page be acknowledged","expect":["On call"]},
		{"id":"wrong","input":"who tags a release","expect":["On call"]}
	]}`
	if res, out := f.do(t, nil, "POST", "/api/evals", tok, suite); res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %v", res.StatusCode, out)
	}
	f.do(t, nil, "POST", "/api/evals/retrieval/run", tok, "")
	run := f.lastRun(t, tok, "retrieval")
	if run["passed"].(float64) != 2 || run["total"].(float64) != 3 {
		t.Fatalf("run = %v", run)
	}
	m := run["metrics"].(map[string]any)
	if got := m["hit_rate"].(float64); got < 0.66 || got > 0.67 {
		t.Fatalf("hit rate = %v, want 2 of 3", got)
	}
	first := run["results"].([]any)[0].(map[string]any)
	if first["rank"].(float64) != 1 || len(first["hits"].([]any)) != 1 {
		t.Fatalf("first case = %v", first)
	}
}

func TestEvalsAreConfinedAndGuarded(t *testing.T) {
	f := newFixture(t)
	acme, globex := f.machine(t, "acme", everything...), f.machine(t, "globex", everything...)
	f.do(t, nil, "POST", "/api/agents", acme, helperSpec)
	suite := `{"name":"smoke","target":"agent","subject":"helper","cases":[{"id":"c","input":"hi","checks":[{"kind":"contains","value":"x"}]}]}`
	f.do(t, nil, "POST", "/api/evals", acme, suite)

	if _, out := f.do(t, nil, "GET", "/api/evals", globex, ""); len(out["suites"].([]any)) != 0 {
		t.Errorf("other tenant listed suites: %v", out)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/evals/smoke/runs", ""},
		{"POST", "/api/evals/smoke/run", ""},
		{"DELETE", "/api/evals/smoke", ""},
		{"PUT", "/api/evals/smoke", suite},
		// The other tenant has no agent called helper to point a suite at.
		{"POST", "/api/evals", suite},
	} {
		if res, _ := f.do(t, nil, tc.method, tc.path, globex, tc.body); res.StatusCode != http.StatusNotFound {
			t.Errorf("other tenant %s %s = %d, want 404", tc.method, tc.path, res.StatusCode)
		}
	}
	viewer := f.signIn(t, "oap-viewers")
	if res, _ := f.do(t, viewer, "POST", "/api/evals/smoke/run", "", ""); res.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer ran an eval: %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "GET", "/api/agents/helper/history", globex, ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("other tenant read the agent's history: %d", res.StatusCode)
	}
	_, hist := f.do(t, nil, "GET", "/api/agents/helper/history", acme, "")
	if len(hist["revisions"].([]any)) != 1 {
		t.Errorf("history = %v", hist)
	}
}

func TestConversationsBranchAndPick(t *testing.T) {
	f := newFixture(t)
	tok := f.machine(t, "acme", everything...)
	f.do(t, nil, "POST", "/api/agents", tok, helperSpec)
	f.do(t, nil, "POST", "/api/agents", tok, `{"name":"releaser","description":"Cuts releases and tags versions.","model":"offline","provider":"offline","system_prompt":"x"}`)

	// With no baseline, a message nothing matches cannot be auto-picked.
	if res, _ := f.do(t, nil, "POST", "/api/runs", tok, `{"text":"what is the weather"}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("auto with no baseline = %d, want 400", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "PUT", "/api/settings", tok, `{"baseline":"ghost"}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a baseline that is not an agent = %d, want 400", res.StatusCode)
	}
	f.do(t, nil, "PUT", "/api/settings", tok, `{"baseline":"helper"}`)

	// The first message starts a thread and is auto-picked by description.
	_, first := f.do(t, nil, "POST", "/api/runs", tok, `{"agent":"auto","text":"tag the next release"}`)
	if first["agent"] != "releaser" || first["picked"] == "" || first["thread"] != first["id"] {
		t.Fatalf("first = %v", first)
	}
	_, weather := f.do(t, nil, "POST", "/api/runs", tok, `{"text":"what is the weather"}`)
	if weather["agent"] != "helper" {
		t.Fatalf("nothing matched, so the baseline should answer: %v", weather)
	}
	id := first["id"].(string)

	// A follow-up by a different agent stays in the thread, and the record
	// shows what the person typed, not the replayed conversation.
	_, second := f.do(t, nil, "POST", "/api/runs", tok, `{"agent":"helper","text":"and write the changelog","after":"`+id+`"}`)
	if second["thread"] != id || second["prev"] != id || second["text"] != "and write the changelog" {
		t.Fatalf("second = %v", second)
	}
	// Answering the same point again is a branch.
	_, branch := f.do(t, nil, "POST", "/api/runs", tok, `{"agent":"releaser","text":"and write the changelog","after":"`+id+`"}`)
	if branch["prev"] != id || branch["id"] == second["id"] {
		t.Fatalf("branch = %v", branch)
	}

	_, thread := f.do(t, nil, "GET", "/api/threads/"+id, tok, "")
	if len(thread["turns"].([]any)) != 3 {
		t.Fatalf("thread = %v", thread)
	}
	_, threads := f.do(t, nil, "GET", "/api/threads", tok, "")
	var summary map[string]any
	for _, s := range threads["threads"].([]any) {
		if s.(map[string]any)["id"] == id {
			summary = s.(map[string]any)
		}
	}
	if summary["turns"].(float64) != 3 || summary["branches"].(float64) != 1 || summary["title"] != "tag the next release" {
		t.Fatalf("summary = %v", summary)
	}

	// A project files a conversation, and a follow-up keeps it.
	if res, _ := f.do(t, nil, "POST", "/api/runs", tok, `{"agent":"helper","text":"hi","project":"Ghost"}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown project = %d, want 400", res.StatusCode)
	}
	if res, out := f.do(t, nil, "PUT", "/api/settings", tok, `{"baseline":"helper","history":"none","history_limit":5000,"projects":[{"name":"Launch"}]}`); res.StatusCode != http.StatusOK {
		t.Fatalf("settings = %d %v", res.StatusCode, out)
	}
	for _, bad := range []string{
		`{"history":"everything"}`, `{"history_limit":5}`, `{"projects":[{"name":"a"},{"name":"A"}]}`, `{"projects":[{"name":"<b>"}]}`,
	} {
		if res, _ := f.do(t, nil, "PUT", "/api/settings", tok, bad); res.StatusCode != http.StatusBadRequest {
			t.Errorf("settings %s = %d, want 400", bad, res.StatusCode)
		}
	}
	_, filed := f.do(t, nil, "POST", "/api/runs", tok, `{"agent":"helper","text":"kickoff","project":"Launch"}`)
	_, next := f.do(t, nil, "POST", "/api/runs", tok, `{"agent":"helper","text":"then what","project":"Other","after":"`+filed["id"].(string)+`"}`)
	if filed["project"] != "Launch" || next["project"] != "Launch" {
		t.Fatalf("project: first %v, follow-up %v", filed["project"], next["project"])
	}

	_, choice := f.do(t, nil, "GET", "/api/pick?q=cut+a+release", tok, "")
	if choice["agent"] != "releaser" {
		t.Fatalf("pick = %v", choice)
	}

	// Another tenant cannot read the thread or continue it.
	globex := f.machine(t, "globex", everything...)
	if res, _ := f.do(t, nil, "GET", "/api/threads/"+id, globex, ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("other tenant read the thread: %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "POST", "/api/runs", globex, `{"agent":"helper","text":"hi","after":"`+id+`"}`); res.StatusCode != http.StatusNotFound {
		t.Errorf("other tenant continued the thread: %d", res.StatusCode)
	}
}

func TestPreferencesAreTheCallersOwn(t *testing.T) {
	f := newFixture(t)
	ada := f.signIn(t, "oap-viewers")
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 32)

	res, out := f.do(t, ada, "PUT", "/api/preferences", "", `{"display_name":"Ada","theme":"light","accent":"teal"}`)
	if res.StatusCode != http.StatusOK || out["theme"] != "light" {
		t.Fatalf("set = %d %v", res.StatusCode, out)
	}
	if res, _ := f.do(t, ada, "PUT", "/api/preferences", "", `{"display_name":"Ada","theme":"neon","accent":"teal"}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a bad theme = %d", res.StatusCode)
	}
	if res, _ := f.do(t, ada, "PUT", "/api/preferences/photo", "", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an svg was accepted as a photo: %d", res.StatusCode)
	}
	if res, _ := f.do(t, ada, "PUT", "/api/preferences/photo", "", png); res.StatusCode != http.StatusOK {
		t.Fatalf("a png was refused: %d", res.StatusCode)
	}

	req, _ := http.NewRequest("GET", f.srv.URL+"/api/preferences/photo", nil)
	got, err := ada.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = got.Body.Close()
	if got.Header.Get("Content-Type") != "image/png" || got.Header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(got.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("photo headers = %v", got.Header)
	}

	// A machine in the same tenant is a different caller and sees its own.
	svc := f.machine(t, "acme", everything...)
	if _, other := f.do(t, nil, "GET", "/api/preferences", svc, ""); other["display_name"] != "" || other["has_photo"] != false {
		t.Fatalf("another caller sees them: %v", other)
	}
	if res, _ := f.do(t, nil, "GET", "/api/preferences/photo", svc, ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("another caller read the photo: %d", res.StatusCode)
	}
}

func TestAgentMemory(t *testing.T) {
	f := newFixture(t)
	acme, globex := f.machine(t, "acme", everything...), f.machine(t, "globex", everything...)
	f.do(t, nil, "POST", "/api/agents", acme, helperSpec)

	res, note := f.do(t, nil, "POST", "/api/agents/helper/memory", acme, `{"text":"Releases go out on Tuesdays."}`)
	if res.StatusCode != http.StatusCreated || note["source"] != "person" {
		t.Fatalf("add = %d %v", res.StatusCode, note)
	}
	if res, _ := f.do(t, nil, "POST", "/api/agents/helper/memory", acme, `{"text":"   "}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an empty note = %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "POST", "/api/agents/ghost/memory", acme, `{"text":"x"}`); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a note for an agent that does not exist = %d", res.StatusCode)
	}
	// Another tenant has no such agent, so no such memory.
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/agents/helper/memory", ""},
		{"POST", "/api/agents/helper/memory", `{"text":"x"}`},
		{"DELETE", "/api/agents/helper/memory/" + note["id"].(string), ""},
	} {
		if res, _ := f.do(t, nil, tc.method, tc.path, globex, tc.body); res.StatusCode != http.StatusNotFound {
			t.Errorf("other tenant %s %s = %d, want 404", tc.method, tc.path, res.StatusCode)
		}
	}
	if res, out := f.do(t, nil, "POST", "/api/agents/helper/memory", acme, `{"text":"Reach Ada at ada@example.com."}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a note with an email address = %d %v", res.StatusCode, out)
	}
	if _, out := f.do(t, nil, "GET", "/api/memory/pending", globex, ""); len(out["proposals"].([]any)) != 0 {
		t.Errorf("other tenant sees proposals: %v", out)
	}
	if res, _ := f.do(t, nil, "POST", "/api/agents/helper/memory/approve", globex, `{"ids":["m1"]}`); res.StatusCode != http.StatusNotFound {
		t.Errorf("other tenant approved memory: %d", res.StatusCode)
	}
	reader := f.machine(t, "acme", "oap:agents:read")
	if res, _ := f.do(t, nil, "POST", "/api/agents/helper/memory/approve", reader, `{"ids":["m1"]}`); res.StatusCode != http.StatusForbidden {
		t.Errorf("a reader approved memory: %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "POST", "/api/agents/helper/memory", reader, `{"text":"x"}`); res.StatusCode != http.StatusForbidden {
		t.Errorf("a reader wrote memory: %d", res.StatusCode)
	}
	_, list := f.do(t, nil, "GET", "/api/agents/helper/memory", acme, "")
	if len(list["notes"].([]any)) != 1 {
		t.Fatalf("notes = %v", list)
	}
	if res, _ := f.do(t, nil, "DELETE", "/api/agents/helper/memory/"+note["id"].(string), acme, ""); res.StatusCode != http.StatusNoContent {
		t.Fatalf("remove = %d", res.StatusCode)
	}
}
