package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/urmzd/collegium/internal/platform"
)

func (f *fixture) raw(t *testing.T, path, bearer string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("GET", f.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

// A published file is listed, versioned and downloaded only by callers in its
// tenant who hold the right, and is never served in a form a browser would run.
func TestArtifactsAreServedSafely(t *testing.T) {
	f := newFixture(t)
	admin := f.machine(t, "acme", everything...)
	reader := f.machine(t, "acme", "collegium:artifacts:read")
	none := f.machine(t, "acme", "collegium:runs:read")
	globex := f.machine(t, "globex", everything...)

	publish := func(thread, name, content string) string {
		t.Helper()
		if err := f.p.PutFile("acme", thread, name, content); err != nil {
			t.Fatal(err)
		}
		a, err := f.p.Publish(t.Context(), platform.Publication{
			Tenant: "acme", Thread: thread, Run: thread, Agent: "helper", Project: "Launch", By: "agent:helper", Path: name,
		})
		if err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	page := publish("t1", "page.html", "<html><body><script>alert(document.cookie)</script></body></html>")
	svg := publish("t1", "logo.svg", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	notes := publish("t1", "notes.txt", "Refunds fell.")
	publish("t1", "notes.txt", "Refunds fell by a third.")
	publish("t2", `we"ird name.md`, "# Hi")

	// Listed newest first, latest version of each, with filters and paging.
	_, out := f.do(t, nil, "GET", "/api/artifacts", admin, "")
	list := out["artifacts"].([]any)
	if len(list) != 4 || out["next"] != nil {
		t.Fatalf("list = %d artifacts, next %v", len(list), out["next"])
	}
	latest := list[1].(map[string]any)
	if latest["title"] != "notes.txt" || latest["version"].(float64) != 2 || latest["mime"] != "text/plain" || latest["kind"] != "document" {
		t.Fatalf("latest notes = %v", latest)
	}
	if _, has := latest["key"]; has {
		t.Fatal("where the bytes are stored is exposed")
	}
	_, first := f.do(t, nil, "GET", "/api/artifacts?limit=3", admin, "")
	_, second := f.do(t, nil, "GET", "/api/artifacts?limit=3&before="+first["next"].(string), admin, "")
	if len(first["artifacts"].([]any)) != 3 || len(second["artifacts"].([]any)) != 1 || second["next"] != nil {
		t.Fatalf("pages = %d then %d", len(first["artifacts"].([]any)), len(second["artifacts"].([]any)))
	}
	if _, out := f.do(t, nil, "GET", "/api/artifacts?thread=t2", admin, ""); len(out["artifacts"].([]any)) != 1 {
		t.Fatal("the thread filter did not narrow the list")
	}
	if _, out := f.do(t, nil, "GET", "/api/artifacts?kind=html", admin, ""); len(out["artifacts"].([]any)) != 1 {
		t.Fatal("the kind filter did not narrow the list")
	}
	if _, out := f.do(t, nil, "GET", "/api/artifacts?q=NOTES", admin, ""); len(out["artifacts"].([]any)) != 1 {
		t.Fatal("searching by title did not find the one file")
	}
	for _, bad := range []string{"?kind=hologram", "?limit=0", "?before=not-a-cursor!"} {
		if res, _ := f.do(t, nil, "GET", "/api/artifacts"+bad, admin, ""); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d", bad, res.StatusCode)
		}
	}
	_, versions := f.do(t, nil, "GET", "/api/artifacts/"+notes+"/versions", admin, "")
	if v := versions["versions"].([]any); len(v) != 2 || v[0].(map[string]any)["version"].(float64) != 2 {
		t.Fatalf("versions = %v", versions)
	}

	// Every download is an attachment of an opaque type unless it is asked
	// for inline and is a type that cannot carry script.
	for name, c := range map[string]struct {
		path, disposition, ctype string
	}{
		"html":                  {page + "/content", "attachment", "application/octet-stream"},
		"html asked for inline": {page + "/content?inline=1", "attachment", "application/octet-stream"},
		"svg asked for inline":  {svg + "/content?inline=1", "attachment", "application/octet-stream"},
		"text":                  {notes + "/content", "attachment", "application/octet-stream"},
		"text asked for inline": {notes + "/content?inline=1", "inline", "text/plain; charset=utf-8"},
	} {
		res, body := f.raw(t, "/api/artifacts/"+c.path, reader)
		h := res.Header
		if res.StatusCode != http.StatusOK || !strings.HasPrefix(h.Get("Content-Disposition"), c.disposition+";") || h.Get("Content-Type") != c.ctype {
			t.Errorf("%s: %d, %q, %q", name, res.StatusCode, h.Get("Content-Disposition"), h.Get("Content-Type"))
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || h.Get("Cache-Control") != "private, no-store" {
			t.Errorf("%s: headers %v", name, h)
		}
		if body == "" {
			t.Errorf("%s: no body", name)
		}
	}
	if _, body := f.raw(t, "/api/artifacts/"+notes+"/content", reader); body != "Refunds fell." {
		t.Fatalf("the first version reads %q", body)
	}
	// A title cannot break out of the header it is quoted in.
	odd := list[0].(map[string]any)["id"].(string)
	if res, _ := f.raw(t, "/api/artifacts/"+odd+"/content", reader); strings.ContainsAny(res.Header.Get("Content-Disposition"), "\r\n") || !strings.Contains(res.Header.Get("Content-Disposition"), "weird name.md") {
		t.Fatalf("disposition = %q", res.Header.Get("Content-Disposition"))
	}

	// Without the right, or from another tenant, there is nothing there.
	for _, path := range []string{"", "/" + notes, "/" + notes + "/content", "/" + notes + "/versions"} {
		if res, _ := f.raw(t, "/api/artifacts"+path, none); res.StatusCode != http.StatusForbidden {
			t.Errorf("without artifacts:read, %q = %d", path, res.StatusCode)
		}
	}
	if _, out := f.do(t, nil, "GET", "/api/artifacts", globex, ""); len(out["artifacts"].([]any)) != 0 {
		t.Fatal("another tenant lists the files")
	}
	for _, path := range []string{"/" + notes, "/" + notes + "/content", "/" + notes + "/versions"} {
		if res, body := f.raw(t, "/api/artifacts"+path, globex); res.StatusCode != http.StatusNotFound || strings.Contains(body, "Refunds") {
			t.Errorf("another tenant, %q = %d", path, res.StatusCode)
		}
	}
	if res, _ := f.do(t, nil, "DELETE", "/api/artifacts/"+notes, globex, ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("another tenant deleting = %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "DELETE", "/api/artifacts/"+notes, reader, ""); res.StatusCode != http.StatusForbidden {
		t.Fatalf("a reader deleting = %d", res.StatusCode)
	}

	_, storage := f.do(t, nil, "GET", "/api/storage", admin, "")
	if storage["count"].(float64) != 5 || storage["limit_bytes"] != nil || storage["by_project"].([]any)[0].(map[string]any)["name"] != "Launch" {
		t.Fatalf("storage = %v", storage)
	}
	if _, other := f.do(t, nil, "GET", "/api/storage", globex, ""); other["used_bytes"].(float64) != 0 {
		t.Fatal("another tenant is charged for the files")
	}

	if res, _ := f.do(t, nil, "DELETE", "/api/artifacts/"+notes, admin, ""); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", res.StatusCode)
	}
	if res, _ := f.raw(t, "/api/artifacts/"+notes+"/content", reader); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a deleted file still downloads: %d", res.StatusCode)
	}
	if _, after := f.do(t, nil, "GET", "/api/storage", admin, ""); after["count"].(float64) != 4 {
		t.Fatalf("after deleting one version there are %v", after["count"])
	}

	// The platform's own agent cannot be edited or impersonated.
	spec := `{"name":"collegium-writer","model":"offline","provider":"offline","system_prompt":"x"}`
	if res, _ := f.do(t, nil, "POST", "/api/agents", admin, spec); res.StatusCode != http.StatusForbidden {
		t.Fatalf("creating an agent under a built-in's name = %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "PUT", "/api/agents/collegium-writer", admin, `{"spec":`+spec+`,"expected_rev":1}`); res.StatusCode != http.StatusForbidden {
		t.Fatalf("revising a built-in = %d", res.StatusCode)
	}
}

// A run's whole record can be read back after it has stopped streaming: its
// events as published, and one manifest for it and every run under it.
func TestRunManifest(t *testing.T) {
	f := newFixture(t)
	acme, globex := f.machine(t, "acme", everything...), f.machine(t, "globex", everything...)
	f.do(t, nil, "POST", "/api/agents", acme, helperSpec)
	_, run := f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"Say hello"}`)
	id := run["id"].(string)

	var events []any
	for range 200 {
		_, out := f.do(t, nil, "GET", "/api/runs/"+id+"/events", acme, "")
		events, _ = out["events"].([]any)
		if n := len(events); n > 0 && events[n-1].(map[string]any)["kind"] == "lifecycle.completed" {
			if out["complete"] != true || events[0].(map[string]any)["kind"] != "lifecycle.accepted" {
				t.Fatalf("events = %v", out)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(events) < 2 {
		t.Fatalf("the run's events never completed: %v", events)
	}

	res, m := f.do(t, nil, "GET", "/api/runs/"+id+"/manifest", acme, "")
	if res.StatusCode != http.StatusOK || m["root"] != id || m["version"].(float64) != 1 {
		t.Fatalf("manifest = %d %v", res.StatusCode, m)
	}
	entry := m["runs"].([]any)[0].(map[string]any)
	if entry["state"] != "done" || entry["complete"] != true || len(entry["events"].([]any)) != len(events) {
		t.Fatalf("entry = %v", entry)
	}
	def := entry["definition"].(map[string]any)
	if def["rev"].(float64) != 1 || def["hash"] == "" || def["spec"].(map[string]any)["name"] != "helper" {
		t.Fatalf("definition = %v", def)
	}
	// Bodies are decoded, not base64: the document reads without a tool.
	last := entry["events"].([]any)[len(events)-1].(map[string]any)
	if body, isObject := last["body"].(map[string]any); !isObject || body["text"] == "" {
		t.Fatalf("the last event's body = %v", last["body"])
	}
	if u := m["usage"].(map[string]any); u["metered"] != true || u["total"].(map[string]any)["model_calls"].(float64) < 1 {
		t.Fatalf("the manifest's usage = %v", u)
	}
	for _, path := range []string{"/events", "/manifest"} {
		if res, _ := f.do(t, nil, "GET", "/api/runs/"+id+path, globex, ""); res.StatusCode != http.StatusNotFound {
			t.Errorf("another tenant reading %s = %d", path, res.StatusCode)
		}
	}
}
