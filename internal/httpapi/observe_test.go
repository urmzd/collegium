package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

var everything = []string{
	"collegium:agents:read", "collegium:agents:write", "collegium:runs:read", "collegium:runs:write",
	"collegium:schedules:read", "collegium:schedules:write", "collegium:alerts:read", "collegium:alerts:write",
	"collegium:knowledge:read", "collegium:knowledge:write", "collegium:evals:read", "collegium:evals:write",
	"collegium:billing:read", "collegium:billing:write", "collegium:artifacts:read", "collegium:artifacts:write",
	"collegium:clients:read", "collegium:clients:write",
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
	viewer := f.signIn(t, "collegium-viewers")
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
	viewer := f.signIn(t, "collegium-viewers")
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
	ada := f.signIn(t, "collegium-viewers")
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
	reader := f.machine(t, "acme", "collegium:agents:read")
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

func TestProjects(t *testing.T) {
	f := newFixture(t)
	acme, globex := f.machine(t, "acme", everything...), f.machine(t, "globex", everything...)
	f.do(t, nil, "POST", "/api/agents", acme, helperSpec)

	if res, out := f.do(t, nil, "POST", "/api/projects", acme, `{"name":"Launch","description":"Q4"}`); res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %v", res.StatusCode, out)
	}
	if res, _ := f.do(t, nil, "POST", "/api/projects", acme, `{"name":"launch"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("a duplicate, differing only in case = %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "POST", "/api/projects", acme, `{"name":"<b>"}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a bad name = %d", res.StatusCode)
	}

	// Several conversations, a schedule and a shared note, all in the project.
	_, one := f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"kickoff","project":"Launch"}`)
	f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"risks","project":"Launch"}`)
	res, sc := f.do(t, nil, "POST", "/api/schedules", acme, `{"name":"standup","expression":"0 9 * * *","agent":"helper","text":"status","project":"Launch"}`)
	if res.StatusCode != http.StatusCreated || sc["project"] != "Launch" {
		t.Fatalf("schedule = %d %v", res.StatusCode, sc)
	}
	if res, _ := f.do(t, nil, "POST", "/api/schedules", acme, `{"name":"x","expression":"0 9 * * *","agent":"helper","text":"s","project":"Ghost"}`); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a schedule in a project that does not exist = %d", res.StatusCode)
	}
	// A run the schedule raises is filed under its project.
	_, fired := f.do(t, nil, "POST", "/api/schedules/standup/run", acme, "")
	if fired["project"] != "Launch" {
		t.Fatalf("the schedule's run has project %v", fired["project"])
	}
	if res, note := f.do(t, nil, "POST", "/api/projects/Launch/memory", acme, `{"text":"The launch date is the first Tuesday of November."}`); res.StatusCode != http.StatusCreated || note["status"] != "approved" {
		t.Fatalf("note = %d %v", res.StatusCode, note)
	}

	_, list := f.do(t, nil, "GET", "/api/projects", acme, "")
	p := list["projects"].([]any)[0].(map[string]any)
	if p["conversations"].(float64) != 3 || p["schedules"].(float64) != 1 || p["notes"].(float64) != 1 {
		t.Fatalf("project = %v", p)
	}

	// The next turn in the project is sent what the project knows. A turn
	// outside it is not.
	_, inside := f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"when is it","after":"`+one["id"].(string)+`"}`)
	_, outside := f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"when is it"}`)
	sent := func(id any) string {
		r, err := f.p.Runs.Get("acme", id.(string))
		if err != nil {
			t.Fatal(err)
		}
		return r.Tag().Text
	}
	if !strings.Contains(sent(inside["id"]), "first Tuesday of November") {
		t.Fatalf("a turn in the project was not sent its shared memory: %q", sent(inside["id"]))
	}
	if strings.Contains(sent(outside["id"]), "November") {
		t.Fatal("a turn outside the project was sent its memory")
	}

	// Another tenant has no such project.
	if _, other := f.do(t, nil, "GET", "/api/projects", globex, ""); len(other["projects"].([]any)) != 0 {
		t.Errorf("other tenant lists projects: %v", other)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/projects/Launch/memory", ""},
		{"POST", "/api/projects/Launch/memory", `{"text":"x"}`},
		{"POST", "/api/projects/Launch/memory/approve", `{"ids":["m1"]}`},
		{"DELETE", "/api/projects/Launch", ""},
	} {
		if res, _ := f.do(t, nil, tc.method, tc.path, globex, tc.body); res.StatusCode != http.StatusNotFound {
			t.Errorf("other tenant %s %s = %d, want 404", tc.method, tc.path, res.StatusCode)
		}
	}
	viewer := f.signIn(t, "collegium-viewers")
	if res, _ := f.do(t, viewer, "POST", "/api/projects", "", `{"name":"Mine"}`); res.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer made a project: %d", res.StatusCode)
	}

	// Deleting a project drops its shared memory and keeps its conversations.
	if res, _ := f.do(t, nil, "DELETE", "/api/projects/Launch", acme, ""); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", res.StatusCode)
	}
	if got := f.p.Memory.List("acme", "project:Launch"); len(got) != 0 {
		t.Fatalf("the project's memory outlived it: %v", got)
	}
	if res, _ := f.do(t, nil, "GET", "/api/threads/"+one["id"].(string), acme, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("a conversation was lost with its project: %d", res.StatusCode)
	}
}

func TestThreadsPage(t *testing.T) {
	f := newFixture(t)
	acme := f.machine(t, "acme", everything...)
	f.do(t, nil, "POST", "/api/agents", acme, helperSpec)
	for _, text := range []string{"one", "two", "three"} {
		f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"`+text+`"}`)
		time.Sleep(5 * time.Millisecond)
	}

	// With no parameters the old shape still works, and next is present.
	_, all := f.do(t, nil, "GET", "/api/threads", acme, "")
	if len(all["threads"].([]any)) != 3 {
		t.Fatalf("threads = %v", all)
	}
	if next, present := all["next"]; !present || next != nil {
		t.Fatalf("next = %v (present %v), want null", next, present)
	}

	_, first := f.do(t, nil, "GET", "/api/threads?limit=2&origin=api", acme, "")
	cursor, _ := first["next"].(string)
	if len(first["threads"].([]any)) != 2 || cursor == "" {
		t.Fatalf("first page = %v", first)
	}
	_, second := f.do(t, nil, "GET", "/api/threads?limit=2&origin=api&before="+cursor, acme, "")
	if len(second["threads"].([]any)) != 1 || second["next"] != nil {
		t.Fatalf("second page = %v", second)
	}
	if _, none := f.do(t, nil, "GET", "/api/threads?origin=cron", acme, ""); len(none["threads"].([]any)) != 0 {
		t.Fatalf("origin filter = %v", none)
	}
	for _, bad := range []string{"limit=0", "limit=many", "standing=sleepy", "before=not-a-cursor!"} {
		if res, _ := f.do(t, nil, "GET", "/api/threads?"+bad, acme, ""); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, res.StatusCode)
		}
	}
	// A cursor carries no access: another tenant using it sees only its own.
	globex := f.machine(t, "globex", everything...)
	if _, other := f.do(t, nil, "GET", "/api/threads?before="+cursor, globex, ""); len(other["threads"].([]any)) != 0 {
		t.Fatalf("another tenant paged into these threads: %v", other)
	}
}

func TestBillingPolicies(t *testing.T) {
	f := newFixture(t)
	acme, globex := f.machine(t, "acme", everything...), f.machine(t, "globex", everything...)
	f.do(t, nil, "POST", "/api/agents", acme, helperSpec)
	f.do(t, nil, "POST", "/api/projects", acme, `{"name":"Launch"}`)

	res, tenantPolicy := f.do(t, nil, "PUT", "/api/policies", acme, `{"scope":{"kind":"tenant"},"period":"day","max_runs":100}`)
	if res.StatusCode != http.StatusOK || tenantPolicy["version"].(float64) != 1 || tenantPolicy["action"] != "block" {
		t.Fatalf("tenant policy = %d %v", res.StatusCode, tenantPolicy)
	}
	for name, body := range map[string]string{
		"more than the tenant allows":   `{"scope":{"kind":"project","name":"Launch"},"period":"day","max_runs":500}`,
		"a project that does not exist": `{"scope":{"kind":"project","name":"Ghost"},"period":"day","max_runs":5}`,
		"an agent that does not exist":  `{"scope":{"kind":"agent","name":"ghost"},"period":"day","max_runs":5}`,
		"no limits":                     `{"scope":{"kind":"tenant"},"period":"day"}`,
		"approval, which is not built":  `{"scope":{"kind":"tenant"},"period":"day","max_runs":5,"action":"approve"}`,
	} {
		if res, _ := f.do(t, nil, "PUT", "/api/policies", acme, body); res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d", name, res.StatusCode)
		}
	}
	if res, _ := f.do(t, nil, "PUT", "/api/policies", acme, `{"scope":{"kind":"project","name":"Launch"},"period":"day","max_runs":2}`); res.StatusCode != http.StatusOK {
		t.Fatalf("project policy = %d", res.StatusCode)
	}

	// Two runs fit in the project. The third is refused with the reason, the
	// policy and its version, and when the count starts again.
	for range 2 {
		if res, out := f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"go","project":"Launch"}`); res.StatusCode != http.StatusAccepted {
			t.Fatalf("a run under the limit = %d %v", res.StatusCode, out)
		}
	}
	res, refused := f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"go","project":"Launch"}`)
	if res.StatusCode != http.StatusTooManyRequests || refused["limit"] != "runs" || refused["remaining"].(float64) != 0 || refused["resets_at"] == nil {
		t.Fatalf("refusal = %d %v", res.StatusCode, refused)
	}
	pol := refused["policy"].(map[string]any)
	if pol["version"].(float64) != 1 || pol["scope"].(map[string]any)["name"] != "Launch" {
		t.Fatalf("the refusal names the wrong policy: %v", pol)
	}
	// Outside the project the run is held only to the tenant's limit.
	if res, _ := f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"go"}`); res.StatusCode != http.StatusAccepted {
		t.Fatalf("a run outside the project = %d", res.StatusCode)
	}

	_, usage := f.do(t, nil, "GET", "/api/usage", acme, "")
	var launch map[string]any
	for _, s := range usage["scopes"].([]any) {
		if s.(map[string]any)["scope"].(map[string]any)["kind"] == "project" {
			launch = s.(map[string]any)
		}
	}
	if launch["state"] != "over" || launch["runs"].(map[string]any)["used"].(float64) != 2 || launch["spend"].(map[string]any)["metered"] != false {
		t.Fatalf("usage = %v", launch)
	}
	_, notices := f.do(t, nil, "GET", "/api/usage/notices", acme, "")
	if first := notices["notices"].([]any)[0].(map[string]any); first["kind"] != "refused" || first["version"].(float64) != 1 {
		t.Fatalf("notices = %v", notices)
	}
	_, alerts := f.do(t, nil, "GET", "/api/alerts", acme, "")
	found := false
	for _, a := range alerts["alerts"].([]any) {
		found = found || a.(map[string]any)["kind"] == "budget"
	}
	if !found {
		t.Fatalf("no budget alert was raised: %v", alerts)
	}

	// Every change is on record with who, before and after.
	f.do(t, nil, "PUT", "/api/policies", acme, `{"scope":{"kind":"project","name":"Launch"},"period":"day","max_runs":5}`)
	_, history := f.do(t, nil, "GET", "/api/policies/history", acme, "")
	latest := history["changes"].([]any)[0].(map[string]any)
	if latest["by"] != "svc-acme" || latest["before"].(map[string]any)["max_runs"].(float64) != 2 || latest["after"].(map[string]any)["version"].(float64) != 2 {
		t.Fatalf("history = %v", latest)
	}
	// Raising the limit lets the next run in.
	if res, _ := f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"go","project":"Launch"}`); res.StatusCode != http.StatusAccepted {
		t.Fatalf("after raising the limit = %d", res.StatusCode)
	}

	// Another tenant sees none of it, and only an admin may change a policy.
	for path, key := range map[string]string{"/api/policies": "policies", "/api/policies/history": "changes", "/api/usage": "scopes", "/api/usage/notices": "notices"} {
		if _, out := f.do(t, nil, "GET", path, globex, ""); len(out[key].([]any)) != 0 {
			t.Errorf("other tenant GET %s = %v", path, out)
		}
	}
	if res, _ := f.do(t, nil, "DELETE", "/api/policies?kind=project&name=Launch", globex, ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("other tenant deleted the policy: %d", res.StatusCode)
	}
	viewer := f.signIn(t, "collegium-viewers")
	if res, _ := f.do(t, viewer, "GET", "/api/usage", "", ""); res.StatusCode != http.StatusOK {
		t.Errorf("a viewer cannot read usage: %d", res.StatusCode)
	}
	if res, _ := f.do(t, viewer, "PUT", "/api/policies", "", `{"scope":{"kind":"tenant"},"period":"day","max_runs":1}`); res.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer changed a policy: %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "DELETE", "/api/policies?kind=project&name=Launch", acme, ""); res.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d", res.StatusCode)
	}
}

func TestOnboardingFlags(t *testing.T) {
	f := newFixture(t)
	admin, viewer := f.signIn(t, "collegium-admins"), f.signIn(t, "collegium-viewers")

	_, me := f.do(t, admin, "GET", "/api/me", "", "")
	if me["first_admin"] != true {
		t.Fatalf("an admin in a workspace nobody set up: first_admin = %v", me["first_admin"])
	}
	if _, v := f.do(t, viewer, "GET", "/api/me", "", ""); v["first_admin"] != false {
		t.Fatalf("a viewer was called the first admin")
	}
	if _, prefs := f.do(t, admin, "GET", "/api/preferences", "", ""); prefs["onboarded_at"] != nil {
		t.Fatalf("onboarded before onboarding: %v", prefs)
	}

	// Only an admin names the workspace, and the server says who and when.
	if res, _ := f.do(t, viewer, "PUT", "/api/settings", "", `{"workspace_name":"Mine"}`); res.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer set the workspace up: %d", res.StatusCode)
	}
	res, saved := f.do(t, admin, "PUT", "/api/settings", "", `{"workspace_name":" Acme Robotics ","setup_by":"someone-else","setup_at":"2001-01-01T00:00:00Z"}`)
	if res.StatusCode != http.StatusOK || saved["workspace_name"] != "Acme Robotics" || saved["setup_by"] != "00u-ada" {
		t.Fatalf("setup = %d %v", res.StatusCode, saved)
	}
	first := saved["setup_at"]
	if first == nil || strings.HasPrefix(first.(string), "2001") {
		t.Fatalf("setup_at came from the client: %v", first)
	}
	if res, _ := f.do(t, admin, "PUT", "/api/settings", "", `{"workspace_name":"<b>x</b>"}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("markup in a workspace name = %d", res.StatusCode)
	}
	// A later write does not move it, and nobody is the first admin any more.
	_, again := f.do(t, admin, "PUT", "/api/settings", "", `{"workspace_name":"Acme"}`)
	if again["setup_at"] != first || again["setup_by"] != "00u-ada" {
		t.Fatalf("setup moved: %v", again)
	}
	if _, me := f.do(t, admin, "GET", "/api/me", "", ""); me["first_admin"] != false {
		t.Fatal("still the first admin after setup")
	}
	if _, read := f.do(t, viewer, "GET", "/api/settings", "", ""); read["workspace_name"] != "Acme" {
		t.Fatalf("a viewer cannot read the workspace name: %v", read)
	}

	// Onboarding is per person, set once, with the server's time.
	_, done := f.do(t, admin, "PUT", "/api/preferences", "", `{"display_name":"Ada","theme":"dark","accent":"purple","onboarded":true}`)
	at := done["onboarded_at"]
	if at == nil || done["display_name"] != "Ada" {
		t.Fatalf("onboarding = %v", done)
	}
	_, later := f.do(t, admin, "PUT", "/api/preferences", "", `{"display_name":"Ada L","theme":"dark","accent":"purple"}`)
	if later["onboarded_at"] != at {
		t.Fatalf("a later save moved or cleared onboarded_at: %v", later)
	}
	// Both sign-ins in this fixture are the same person, so the other caller
	// here is a machine in the same workspace.
	if _, other := f.do(t, nil, "GET", "/api/preferences", f.machine(t, "acme", everything...), ""); other["onboarded_at"] != nil {
		t.Fatal("one caller finishing onboarding marked another")
	}
}
