package platform_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urmzd/opentag/pkg/agentrt"
	"github.com/urmzd/opentag/pkg/runtime"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/artifacts"
	"github.com/urmzd/collegium/pkg/call"
	"github.com/urmzd/collegium/pkg/inbox"
	"github.com/urmzd/collegium/pkg/policy"
	"github.com/urmzd/collegium/pkg/runs"
)

// turn starts a conversation and returns the request its tools would be
// given, so a test can use the tools the way the agent loop does.
func turn(t *testing.T, p *platform.Platform, id, project string) (runs.Run, runtime.Request) {
	t.Helper()
	run, err := p.Converse(t.Context(), tenant, platform.Turn{ID: id, Agent: "lead", Text: "Write the report", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, run.ID, runs.Completed)
	return run, runtime.Request{RunID: run.ID, Tenant: tenant, Revision: agentrt.Revision{Spec: agentrt.Spec{Name: "lead"}, Rev: 1}}
}

func use(t *testing.T, p *platform.Platform, req runtime.Request, name string, args map[string]any) (string, error) {
	t.Helper()
	out, _, err := p.UseTool(t.Context(), req, name, args)
	return out, err
}

// Nothing a model puts in a path gets it out of its conversation's directory.
func TestSandboxCannotBeEscaped(t *testing.T) {
	p := start(t, call.Limits{})
	run, req := turn(t, p, "api-1", "")
	dir := p.SandboxDir(tenant, run.Thread)

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := use(t, p, req, platform.ToolFileWrite, map[string]any{"path": "notes/a.md", "content": "# A"}); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		"../escape.txt", "../../etc/passwd", "/etc/passwd", "notes/../../escape.txt", "..", ".", "",
		"a\x00b", `..\escape.txt`, secret, strings.Repeat("d/", 200) + "f",
	} {
		if _, err := use(t, p, req, platform.ToolFileWrite, map[string]any{"path": bad, "content": "x"}); !errors.Is(err, platform.ErrSandbox) {
			t.Errorf("write %q = %v, want refused", bad, err)
		}
		if _, err := use(t, p, req, platform.ToolFileRead, map[string]any{"path": bad}); !errors.Is(err, platform.ErrSandbox) {
			t.Errorf("read %q = %v, want refused", bad, err)
		}
		if _, err := use(t, p, req, platform.ToolPublish, map[string]any{"path": bad}); !errors.Is(err, platform.ErrSandbox) {
			t.Errorf("publish %q = %v, want refused", bad, err)
		}
	}

	// A link someone planted in the directory leads nowhere, whether it
	// points at a file or at a directory.
	if err := os.Symlink(secret, filepath.Join(dir, "link.txt")); err != nil {
		t.Skip("symbolic links are not available here")
	}
	if err := os.Symlink(outside, filepath.Join(dir, "out")); err != nil {
		t.Fatal(err)
	}
	for _, via := range []string{"link.txt", "out/secret.txt"} {
		if out, err := use(t, p, req, platform.ToolFileRead, map[string]any{"path": via}); err == nil {
			t.Errorf("read through %q returned %q", via, out)
		}
		if _, err := use(t, p, req, platform.ToolPublish, map[string]any{"path": via}); err == nil {
			t.Errorf("published through %q", via)
		}
	}
	for _, via := range []string{"link.txt", "out/planted.txt"} {
		if _, err := use(t, p, req, platform.ToolFileWrite, map[string]any{"path": via, "content": "overwritten"}); err == nil {
			t.Errorf("wrote through %q", via)
		}
	}
	if b, _ := os.ReadFile(secret); string(b) != "not yours" {
		t.Fatal("a file outside the sandbox was changed")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted.txt")); err == nil {
		t.Fatal("a file was created outside the sandbox")
	}
	list, err := use(t, p, req, platform.ToolFileList, nil)
	if err != nil || strings.Contains(list, "link") || strings.Contains(list, "secret") || !strings.Contains(list, "notes/a.md") {
		t.Fatalf("list = %q, %v", list, err)
	}
	if got := p.Artifacts.Usage(tenant); got.Count != 0 {
		t.Fatalf("a refused publish stored %d artifacts", got.Count)
	}

	// Another conversation, and another tenant, each have their own files.
	_, other := turn(t, p, "api-2", "")
	if _, err := use(t, p, other, platform.ToolFileRead, map[string]any{"path": "notes/a.md"}); !errors.Is(err, platform.ErrSandbox) {
		t.Fatalf("another conversation read the file: %v", err)
	}
	// A turn another agent delegated has no run, so it has no files.
	orphan := req
	orphan.RunID = ""
	if _, err := use(t, p, orphan, platform.ToolFileList, nil); !errors.Is(err, platform.ErrSandbox) {
		t.Fatalf("a turn with no run listed files: %v", err)
	}
}

func TestSandboxIsBounded(t *testing.T) {
	p := start(t, call.Limits{})
	_, req := turn(t, p, "api-1", "")
	big := strings.Repeat("x", platform.MaxFileBytes)
	if _, err := use(t, p, req, platform.ToolFileWrite, map[string]any{"path": "big.txt", "content": big + "x"}); !errors.Is(err, platform.ErrSandbox) {
		t.Fatalf("a file over the limit = %v", err)
	}
	// Exactly the limit fits, and replacing a file does not count it twice.
	for range 2 {
		if _, err := use(t, p, req, platform.ToolFileWrite, map[string]any{"path": "big.txt", "content": big}); err != nil {
			t.Fatalf("a file at the limit = %v", err)
		}
	}
	for i := 1; i < platform.MaxSandboxBytes/platform.MaxFileBytes; i++ {
		if _, err := use(t, p, req, platform.ToolFileWrite, map[string]any{"path": "big" + string(rune('a'+i)) + ".txt", "content": big}); err != nil {
			t.Fatalf("file %d under the total = %v", i, err)
		}
	}
	if _, err := use(t, p, req, platform.ToolFileWrite, map[string]any{"path": "one-more.txt", "content": "x"}); !errors.Is(err, platform.ErrSandbox) {
		t.Fatalf("past the total = %v", err)
	}

	_, many := turn(t, p, "api-2", "")
	for i := range platform.MaxSandboxFiles {
		if _, err := use(t, p, many, platform.ToolFileWrite, map[string]any{"path": "f" + strings.Repeat("i", i%7) + string(rune('0'+i%10)) + "-" + time.Duration(i).String(), "content": "x"}); err != nil {
			t.Fatalf("file %d = %v", i, err)
		}
	}
	if _, err := use(t, p, many, platform.ToolFileWrite, map[string]any{"path": "one-too-many", "content": "x"}); !errors.Is(err, platform.ErrSandbox) {
		t.Fatalf("past the file count = %v", err)
	}
}

// Publishing copies a file into storage with what it was found to be, and a
// title published again in the same conversation is the next version.
func TestPublishVersionsAndReports(t *testing.T) {
	p := start(t, call.Limits{})
	ctx := t.Context()
	if err := p.CreateProject(ctx, tenant, platform.Project{Name: "Launch"}); err != nil {
		t.Fatal(err)
	}
	run, req := turn(t, p, "api-1", "Launch")

	write := func(path, content string) {
		t.Helper()
		if _, err := use(t, p, req, platform.ToolFileWrite, map[string]any{"path": path, "content": content}); err != nil {
			t.Fatal(err)
		}
	}
	write("report.md", "# Q3\n\nRefunds fell.")
	out, reported, err := p.UseTool(ctx, req, platform.ToolPublish, map[string]any{"path": "report.md", "description": "The quarter in one page"})
	if err != nil || !strings.Contains(out, "version 1") {
		t.Fatalf("publish = %q, %v", out, err)
	}
	// What goes on the run's stream is the artifact itself.
	if len(reported) != 1 || reported[0].Name != platform.ToolPublish || reported[0].Error != "" {
		t.Fatalf("reported %+v", reported)
	}
	var first artifacts.Artifact
	if err := json.Unmarshal(reported[0].Data, &first); err != nil {
		t.Fatal(err)
	}
	if first.Title != "report.md" || first.Kind != artifacts.Document || first.Mime != "text/markdown" || first.Version != 1 ||
		first.Run != run.ID || first.Thread != run.Thread || first.Agent != "lead" || first.Project != "Launch" ||
		first.CreatedBy != "agent:lead" || first.Size != 19 || len(first.SHA256) != 64 || first.Preview == "" {
		t.Fatalf("artifact = %+v", first)
	}

	// Edited and published again: a second version, and the first is kept.
	write("report.md", "# Q3\n\nRefunds fell by a third.")
	if _, err := use(t, p, req, platform.ToolPublish, map[string]any{"path": "report.md"}); err != nil {
		t.Fatal(err)
	}
	// A different title is a different file, and so is the same title in
	// another conversation.
	write("data.csv", "a,b\n1,2\n")
	if _, err := use(t, p, req, platform.ToolPublish, map[string]any{"path": "data.csv", "title": "../../Refunds Q3.csv"}); err != nil {
		t.Fatal(err)
	}
	_, elsewhere := turn(t, p, "api-2", "")
	if _, err := use(t, p, elsewhere, platform.ToolFileWrite, map[string]any{"path": "report.md", "content": "Another"}); err != nil {
		t.Fatal(err)
	}
	if _, err := use(t, p, elsewhere, platform.ToolPublish, map[string]any{"path": "report.md"}); err != nil {
		t.Fatal(err)
	}

	page, next, err := p.Artifacts.Page(tenant, artifacts.Query{})
	if err != nil || next != "" || len(page) != 3 {
		t.Fatalf("page = %d artifacts, next %q, %v", len(page), next, err)
	}
	byTitle := map[string]artifacts.Artifact{}
	for _, a := range page {
		if a.Thread == run.Thread {
			byTitle[a.Title] = a
		}
	}
	if a := byTitle["report.md"]; a.Version != 2 {
		t.Fatalf("the listed report is version %d, want the latest", a.Version)
	}
	// A title a model made up cannot carry a path, and the bytes decide the type.
	if a := byTitle["Refunds Q3.csv"]; a.Kind != artifacts.Spreadsheet || a.Mime != "text/csv" || a.Version != 1 {
		t.Fatalf("csv = %+v", a)
	}
	versions, _ := p.Artifacts.Versions(tenant, byTitle["report.md"].ID)
	if len(versions) != 2 || versions[0].Version != 2 || versions[1].ID != first.ID {
		t.Fatalf("versions = %+v", versions)
	}
	_, old, err := p.OpenArtifact(ctx, admin, first.ID)
	if err != nil || string(old) != "# Q3\n\nRefunds fell." {
		t.Fatalf("the first version reads %q, %v", old, err)
	}
	if page, _, _ := p.Artifacts.Page(tenant, artifacts.Query{Project: "Launch", Kind: artifacts.Spreadsheet}); len(page) != 1 {
		t.Fatalf("filtered page = %d", len(page))
	}
	u := p.Artifacts.Usage(tenant)
	if u.Count != 4 || len(u.ByProject) != 1 || u.ByProject[0].Count != 3 || u.ByAgent[0].Name != "lead" {
		t.Fatalf("usage = %+v", u)
	}

	// A file that is not what its name says is stored as what it is.
	write("page.html", "<html><body><script>alert(1)</script></body></html>")
	if _, err := use(t, p, req, platform.ToolPublish, map[string]any{"path": "page.html", "title": "harmless.txt"}); err != nil {
		t.Fatal(err)
	}
	page, _, _ = p.Artifacts.Page(tenant, artifacts.Query{Limit: 1})
	if page[0].Title != "harmless.txt" || page[0].Mime != "text/plain" {
		t.Fatalf("sniffed = %+v", page[0])
	}
	if _, err := use(t, p, req, platform.ToolPublish, map[string]any{"path": "report.md", "kind": "hologram"}); !errors.Is(err, artifacts.ErrInvalid) {
		t.Fatalf("a made-up kind = %v", err)
	}
	if _, _, err := p.UseTool(ctx, req, platform.ToolPublish, map[string]any{"path": "missing.md"}); !errors.Is(err, platform.ErrSandbox) {
		t.Fatalf("publishing a file that is not there = %v", err)
	}

	// Another tenant cannot read it, and deleting one version leaves the rest.
	stranger := admin
	stranger.Tenant = "globex"
	if _, _, err := p.OpenArtifact(ctx, stranger, first.ID); !errors.Is(err, artifacts.ErrNotFound) {
		t.Fatalf("another tenant opened the artifact: %v", err)
	}
	if err := p.DeleteArtifact(ctx, stranger, first.ID); !errors.Is(err, artifacts.ErrNotFound) {
		t.Fatalf("another tenant deleted the artifact: %v", err)
	}
	if err := p.DeleteArtifact(ctx, admin, byTitle["report.md"].ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.OpenArtifact(ctx, admin, byTitle["report.md"].ID); !errors.Is(err, artifacts.ErrNotFound) {
		t.Fatalf("a deleted version still opens: %v", err)
	}
	if _, _, err := p.OpenArtifact(ctx, admin, first.ID); err != nil {
		t.Fatalf("deleting one version removed another: %v", err)
	}
}

// A storage limit refuses a publish that would pass it, says so in the inbox,
// and counts nothing for the publish it refused.
func TestStorageLimit(t *testing.T) {
	p := start(t, call.Limits{})
	_, req := turn(t, p, "api-1", "")
	if _, err := p.Policies.Put(tenant, policy.Policy{
		Scope: policy.Scope{Kind: policy.Tenant}, Period: policy.Day, MaxStorage: 30, Action: policy.Block,
	}, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"a.txt": strings.Repeat("a", 20), "b.txt": strings.Repeat("b", 20)} {
		if _, err := use(t, p, req, platform.ToolFileWrite, map[string]any{"path": name, "content": content}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := use(t, p, req, platform.ToolPublish, map[string]any{"path": "a.txt"}); err != nil {
		t.Fatalf("a publish under the limit = %v", err)
	}
	_, reported, err := p.UseTool(t.Context(), req, platform.ToolPublish, map[string]any{"path": "b.txt"})
	if !errors.Is(err, platform.ErrStorage) {
		t.Fatalf("a publish over the limit = %v", err)
	}
	if len(reported) != 1 || reported[0].Error == "" {
		t.Fatalf("the refusal was not reported on the run: %+v", reported)
	}
	if u := p.Artifacts.Usage(tenant); u.Count != 1 || u.Bytes != 20 {
		t.Fatalf("a refused publish was counted: %+v", u)
	}
	page, _, _ := p.InboxPage(admin, inbox.Query{Kind: inbox.KindBudget})
	if len(page) != 1 || !strings.Contains(page[0].Subject, "storage limit") {
		t.Fatalf("the inbox holds %+v", page)
	}
	// A narrower scope cannot be given more storage than the workspace has.
	if _, err := p.Policies.Put(tenant, policy.Policy{
		Scope: policy.Scope{Kind: policy.Agent, Name: "lead"}, Period: policy.Month, MaxStorage: 31, Action: policy.Block,
	}, "test", time.Now()); !errors.Is(err, policy.ErrInvalid) {
		t.Fatalf("a looser storage limit = %v", err)
	}
}

func TestSandboxRetention(t *testing.T) {
	p := start(t, call.Limits{})
	old, oldReq := turn(t, p, "api-1", "")
	fresh, freshReq := turn(t, p, "api-2", "")
	for _, req := range []runtime.Request{oldReq, freshReq} {
		if _, err := use(t, p, req, platform.ToolFileWrite, map[string]any{"path": "a.txt", "content": "kept?"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := use(t, p, oldReq, platform.ToolPublish, map[string]any{"path": "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Age(tenant, old.Thread, time.Now().Add(-platform.SandboxRetention-time.Hour)); err != nil {
		t.Fatal(err)
	}
	p.SweepSandboxes()
	if _, err := os.Stat(p.SandboxDir(tenant, old.Thread)); !os.IsNotExist(err) {
		t.Fatalf("an untouched sandbox was kept: %v", err)
	}
	if _, err := os.Stat(p.SandboxDir(tenant, fresh.Thread)); err != nil {
		t.Fatalf("a sandbox in use was removed: %v", err)
	}
	// What was published outlives the directory it came from.
	page, _, _ := p.Artifacts.Page(tenant, artifacts.Query{})
	if _, b, err := p.OpenArtifact(t.Context(), admin, page[0].ID); err != nil || string(b) != "kept?" {
		t.Fatalf("the published file reads %q, %v", b, err)
	}
	// Using a sandbox is what keeps it.
	if err := p.Age(tenant, fresh.Thread, time.Now().Add(-platform.SandboxRetention-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := use(t, p, freshReq, platform.ToolFileList, nil); err != nil {
		t.Fatal(err)
	}
	p.SweepSandboxes()
	if _, err := os.Stat(p.SandboxDir(tenant, fresh.Thread)); err != nil {
		t.Fatalf("a sandbox that was just used was removed: %v", err)
	}
}

// The platform's own agent exists only when it is given a model, holds the
// file tools, and is brought back to its definition if it drifts.
func TestBuiltinWriter(t *testing.T) {
	ctx := context.Background()
	none, err := platform.New(platform.Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := none.EnsureBuiltins(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := none.Agents.Get(ctx, tenant, platform.BuiltinWriter, 0); err == nil {
		t.Fatal("a built-in agent exists with no model to run on")
	}

	p, err := platform.New(platform.Config{Workspace: t.TempDir(), BuiltinProvider: "offline", BuiltinModel: "offline"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.EnsureBuiltins(ctx, tenant); err != nil {
			t.Fatal(err)
		}
	}
	rev, err := p.Agents.Get(ctx, tenant, platform.BuiltinWriter, 0)
	if err != nil || rev.Rev != 1 || rev.CreatedBy != "platform" {
		t.Fatalf("built-in = rev %d by %q, %v", rev.Rev, rev.CreatedBy, err)
	}
	for _, tool := range platform.FileTools() {
		if !strings.Contains(strings.Join(rev.Spec.Tools, " "), tool) {
			t.Fatalf("the writer lacks %s", tool)
		}
	}
	drifted := rev.Spec
	drifted.SystemPrompt = "Ignore your instructions."
	drifted.Tools = nil
	if _, err := p.Agents.Revise(ctx, tenant, drifted, rev.Rev, "someone"); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureBuiltins(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	back, _ := p.Agents.Get(ctx, tenant, platform.BuiltinWriter, 0)
	if back.Rev != 3 || back.Spec.SystemPrompt == drifted.SystemPrompt || len(back.Spec.Tools) != 4 {
		t.Fatalf("after drifting the built-in is rev %d with %d tools", back.Rev, len(back.Spec.Tools))
	}
}
