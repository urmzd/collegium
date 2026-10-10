package httpapi

import (
	"net/http"
	"testing"
	"time"
)

var everything = []string{
	"oap:agents:read", "oap:agents:write", "oap:runs:read", "oap:runs:write",
	"oap:schedules:read", "oap:schedules:write", "oap:alerts:read", "oap:alerts:write",
	"oap:knowledge:read", "oap:knowledge:write",
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
