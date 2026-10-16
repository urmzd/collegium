package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/urmzd/mandatum/pkg/agentrt"
	"github.com/urmzd/mandatum/pkg/agentrt/payload"
	"github.com/urmzd/mandatum/pkg/runtime"
	saigetypes "github.com/urmzd/saige/agent/types"

	"github.com/urmzd/collegium/pkg/alerts"
	"github.com/urmzd/collegium/pkg/artifacts"
	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/policy"
)

// Files an agent makes.
//
// A conversation has a scratch directory. An agent that is given the file
// tools reads and writes there and nowhere else, and what it writes is nobody's
// but the conversation's until it is published. Publishing copies one file into
// storage, where it gets a version, a hash and a place on the storage page.
//
// The directory belongs to the conversation, not the run, so a follow-up turn
// can keep working on what the last one wrote. It is removed when nothing has
// touched it for SandboxRetention.
//
// The directory is opened as an os.Root. A path a model supplies cannot leave
// it: not with "..", not with an absolute path, and not through a symbolic
// link, whoever made the link.

// The tools an agent is given by naming them in its spec.
const (
	ToolFileWrite = "file_write"
	ToolFileRead  = "file_read"
	ToolFileList  = "file_list"
	ToolPublish   = "publish_artifact"
)

// FileTools lists the names an agent's spec may grant.
func FileTools() []string { return []string{ToolFileWrite, ToolFileRead, ToolFileList, ToolPublish} }

// Bounds on a conversation's scratch directory.
const (
	// MaxFileBytes is the largest file an agent may write or publish.
	MaxFileBytes = 2 << 20
	// MaxSandboxFiles is how many files one conversation may hold.
	MaxSandboxFiles = 200
	// MaxSandboxBytes is how much one conversation may hold in all.
	MaxSandboxBytes = 20 << 20
	// SandboxRetention is how long an untouched scratch directory is kept.
	SandboxRetention = 7 * 24 * time.Hour
	// previewChars is how much of a text file is kept as its preview.
	previewChars = 600
)

// ErrSandbox reports a file request the sandbox refuses.
var ErrSandbox = errors.New("platform: sandbox")

// ErrStorage reports a publish that would pass a storage limit.
var ErrStorage = errors.New("platform: storage limit reached")

// dirName turns a tenant or a conversation id into a directory name. The id is
// hashed, so nothing in it can be a path.
func dirName(id string) string {
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:16])
}

func (p *Platform) sandboxDir(tenant, thread string) string {
	return filepath.Join(p.dir, "sandboxes", dirName(tenant), dirName(thread))
}

// openSandbox opens a conversation's scratch directory, creating it if needed.
func (p *Platform) openSandbox(tenant, thread string) (*os.Root, error) {
	dir := p.sandboxDir(tenant, thread)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("platform: sandbox: %w", err)
	}
	// Touching it is what keeps it past the retention sweep.
	now := p.now()
	_ = os.Chtimes(dir, now, now)
	return os.OpenRoot(dir)
}

// cleanPath checks a path a model supplied. os.Root would refuse an escape by
// itself; this says why, in words the model can act on.
func cleanPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "", fmt.Errorf("%w: a path is required", ErrSandbox)
	case strings.ContainsRune(raw, 0) || strings.Contains(raw, `\`):
		return "", fmt.Errorf("%w: %q is not a usable path", ErrSandbox, raw)
	case path.IsAbs(raw) || filepath.IsAbs(raw):
		return "", fmt.Errorf("%w: use a path relative to your working directory, not %q", ErrSandbox, raw)
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q is outside your working directory", ErrSandbox, raw)
	}
	if len(clean) > 240 {
		return "", fmt.Errorf("%w: that path is too long", ErrSandbox)
	}
	return clean, nil
}

type sandboxFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// listSandbox returns the regular files in a scratch directory. Links are not
// followed and not listed.
func listSandbox(root *os.Root) ([]sandboxFile, int64, error) {
	var files []sandboxFile
	var total int64
	err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, sandboxFile{Path: p, Size: info.Size()})
		total += info.Size()
		return nil
	})
	return files, total, err
}

func writeSandbox(root *os.Root, name, content string) (sandboxFile, error) {
	clean, err := cleanPath(name)
	if err != nil {
		return sandboxFile{}, err
	}
	if len(content) > MaxFileBytes {
		return sandboxFile{}, fmt.Errorf("%w: a file is at most %d bytes, and this is %d", ErrSandbox, MaxFileBytes, len(content))
	}
	files, total, err := listSandbox(root)
	if err != nil {
		return sandboxFile{}, fmt.Errorf("platform: sandbox: %w", err)
	}
	var replacing int64 = -1
	for _, f := range files {
		if f.Path == clean {
			replacing = f.Size
		}
	}
	if replacing < 0 && len(files) >= MaxSandboxFiles {
		return sandboxFile{}, fmt.Errorf("%w: this conversation already holds %d files, the most allowed", ErrSandbox, len(files))
	}
	if total-max(replacing, 0)+int64(len(content)) > MaxSandboxBytes {
		return sandboxFile{}, fmt.Errorf("%w: this conversation may hold %d bytes in all", ErrSandbox, MaxSandboxBytes)
	}
	// A link where the file would go is refused rather than written through.
	if st, err := root.Lstat(clean); err == nil && !st.Mode().IsRegular() {
		return sandboxFile{}, fmt.Errorf("%w: %q is not a file that can be written", ErrSandbox, name)
	}
	if dir := path.Dir(clean); dir != "." {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return sandboxFile{}, fmt.Errorf("%w: %q cannot be created there", ErrSandbox, name)
		}
	}
	if err := root.WriteFile(clean, []byte(content), 0o600); err != nil {
		return sandboxFile{}, fmt.Errorf("%w: %q cannot be written", ErrSandbox, name)
	}
	return sandboxFile{Path: clean, Size: int64(len(content))}, nil
}

func readSandbox(root *os.Root, name string) ([]byte, string, error) {
	clean, err := cleanPath(name)
	if err != nil {
		return nil, "", err
	}
	st, err := root.Lstat(clean)
	if err != nil || !st.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%w: there is no file at %q", ErrSandbox, name)
	}
	if st.Size() > MaxFileBytes {
		return nil, "", fmt.Errorf("%w: %q is larger than %d bytes", ErrSandbox, name, MaxFileBytes)
	}
	b, err := root.ReadFile(clean)
	if err != nil {
		return nil, "", fmt.Errorf("%w: there is no file at %q", ErrSandbox, name)
	}
	return b, clean, nil
}

// textTypes are what a text file is called by its extension. The bytes decide
// whether a file is text at all; the name only says which kind of text.
var textTypes = map[string]string{
	".md": "text/markdown", ".markdown": "text/markdown", ".csv": "text/csv", ".tsv": "text/tab-separated-values",
	".json": "application/json", ".html": "text/html", ".htm": "text/html", ".svg": "image/svg+xml",
	".xml": "application/xml", ".yaml": "application/yaml", ".yml": "application/yaml", ".txt": "text/plain",
}

// sniff says what bytes are. A name never makes binary content into text or
// text into something a browser would run as another type.
func sniff(name string, b []byte) string {
	found, _, _ := strings.Cut(http.DetectContentType(b), ";")
	if strings.HasPrefix(found, "text/") && utf8.Valid(b) {
		if t, ok := textTypes[strings.ToLower(path.Ext(name))]; ok {
			return t
		}
		return "text/plain"
	}
	return found
}

func isText(mime string) bool {
	return strings.HasPrefix(mime, "text/") || slices.Contains([]string{"application/json", "application/xml", "application/yaml", "image/svg+xml"}, mime)
}

// kindOf picks a kind from what the bytes are, for a publish that names none.
func kindOf(mime, name string) artifacts.Kind {
	ext := strings.ToLower(path.Ext(name))
	switch {
	case mime == "application/pdf":
		return artifacts.PDF
	case mime == "text/html":
		return artifacts.HTML
	case strings.HasPrefix(mime, "image/"):
		return artifacts.Image
	case mime == "text/csv" || mime == "text/tab-separated-values":
		return artifacts.Spreadsheet
	case mime == "application/json" || mime == "application/xml" || mime == "application/yaml":
		return artifacts.Data
	case mime == "application/zip" || mime == "application/x-gzip":
		return artifacts.Archive
	case mime == "text/markdown" || ext == ".txt" || ext == "":
		return artifacts.Document
	case isText(mime):
		return artifacts.Code
	}
	return artifacts.Data
}

// PutFile writes a file into a conversation's sandbox, under the same bounds
// an agent's own writes are held to.
func (p *Platform) PutFile(tenant, thread, name, content string) error {
	root, err := p.openSandbox(tenant, thread)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	_, err = writeSandbox(root, name, content)
	return err
}

// Publication is a request to publish one file from a conversation's sandbox.
type Publication struct {
	Tenant, Thread, Run, Agent, Project string
	// By is who published: an agent's name for a tool call.
	By          string
	Path        string
	Title       string
	Kind        string
	Description string
}

// Publish copies a file out of a conversation's sandbox into storage.
func (p *Platform) Publish(ctx context.Context, pub Publication) (artifacts.Artifact, error) {
	root, err := p.openSandbox(pub.Tenant, pub.Thread)
	if err != nil {
		return artifacts.Artifact{}, err
	}
	defer func() { _ = root.Close() }()
	b, clean, err := readSandbox(root, pub.Path)
	if err != nil {
		return artifacts.Artifact{}, err
	}
	title := pub.Title
	if strings.TrimSpace(title) == "" {
		title = path.Base(clean)
	}
	if title, err = artifacts.CleanTitle(title); err != nil {
		return artifacts.Artifact{}, err
	}
	mime := sniff(title, b)
	kind := artifacts.Kind(strings.TrimSpace(pub.Kind))
	if kind == "" {
		kind = kindOf(mime, title)
	}
	if !slices.Contains(artifacts.Kinds(), kind) {
		return artifacts.Artifact{}, fmt.Errorf("%w: kind %q is not one of %v", artifacts.ErrInvalid, kind, artifacts.Kinds())
	}

	// The check and the write are one step, so two publishes cannot both fit
	// under a limit only one of them fits under.
	p.publishing.Lock()
	defer p.publishing.Unlock()
	if err := p.storageAllows(pub.Tenant, pub.Project, pub.Agent, int64(len(b))); err != nil {
		return artifacts.Artifact{}, err
	}
	id := p.Artifacts.NextID(pub.Tenant)
	key := "artifacts/" + id
	info, err := p.Blobs.Put(ctx, dirName(pub.Tenant), key, bytes.NewReader(b), MaxFileBytes)
	if err != nil {
		return artifacts.Artifact{}, err
	}
	a := artifacts.Artifact{
		ID: id, Key: key, Title: title, Kind: kind, Mime: mime, Size: info.Size, SHA256: info.SHA256,
		Run: pub.Run, Thread: pub.Thread, Agent: pub.Agent, Project: pub.Project,
		Description: clip(strings.TrimSpace(pub.Description), 300), CreatedBy: pub.By, CreatedAt: p.now(),
	}
	if isText(mime) {
		a.Preview = clip(string(b), previewChars)
	}
	saved, err := p.Artifacts.Publish(pub.Tenant, a)
	if err != nil {
		_ = p.Blobs.Delete(ctx, dirName(pub.Tenant), key)
		return artifacts.Artifact{}, err
	}
	p.log.Info("platform: artifact published", "tenant", pub.Tenant, "artifact", saved.ID, "title", saved.Title, "version", saved.Version,
		"size", saved.Size, "sha256", saved.SHA256, "run", saved.Run, "agent", saved.Agent, "by", saved.CreatedBy)
	return saved, nil
}

// storageAllows checks a publish against every storage limit that applies, and
// says so in the inbox when one refuses it.
func (p *Platform) storageAllows(tenant, project, agent string, size int64) error {
	for _, pol := range p.Policies.For(tenant, project, agent, "") {
		if pol.MaxStorage <= 0 {
			continue
		}
		var used int64
		switch pol.Scope.Kind {
		case policy.Tenant:
			used = p.Artifacts.Used(tenant, "")
		case policy.Project:
			used = p.Artifacts.Used(tenant, pol.Scope.Name)
		case policy.Agent:
			for _, s := range p.Artifacts.Usage(tenant).ByAgent {
				if s.Name == pol.Scope.Name {
					used = s.Bytes
				}
			}
		}
		if used+size > pol.MaxStorage {
			reason := fmt.Sprintf("%s has reached its storage limit of %d bytes", pol.Scope, pol.MaxStorage)
			if a, ok := p.Alerts.Raise(tenant, "storage: "+pol.Scope.String(), alerts.Alert{
				Kind: alerts.Budget, Severity: alerts.Critical, Title: "Files are not being published: " + reason, At: p.now(),
			}); ok {
				p.inboxAlert(tenant, a)
			}
			return fmt.Errorf("%w: %s", ErrStorage, reason)
		}
	}
	return nil
}

// OpenArtifact opens an artifact's bytes for a caller.
func (p *Platform) OpenArtifact(ctx context.Context, who auth.Principal, id string) (artifacts.Artifact, []byte, error) {
	a, err := p.Artifacts.Get(who.Tenant, id)
	if err != nil {
		return artifacts.Artifact{}, nil, err
	}
	r, _, err := p.Blobs.Get(ctx, dirName(who.Tenant), a.Key)
	if err != nil {
		return artifacts.Artifact{}, nil, fmt.Errorf("%w: artifact %q", artifacts.ErrNotFound, id)
	}
	defer func() { _ = r.Close() }()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return artifacts.Artifact{}, nil, err
	}
	p.log.Info("platform: artifact downloaded", "tenant", who.Tenant, "artifact", a.ID, "by", who.Subject)
	return a, buf.Bytes(), nil
}

// DeleteArtifact removes one version of an artifact and its bytes.
func (p *Platform) DeleteArtifact(ctx context.Context, who auth.Principal, id string) error {
	a, err := p.Artifacts.Delete(who.Tenant, id)
	if err != nil {
		return err
	}
	if err := p.Blobs.Delete(ctx, dirName(who.Tenant), a.Key); err != nil {
		p.log.Warn("platform: an artifact's bytes could not be removed", "artifact", a.ID, "error", err)
	}
	p.log.Info("platform: artifact deleted", "tenant", who.Tenant, "artifact", a.ID, "title", a.Title, "version", a.Version, "by", who.Subject)
	return nil
}

// sweepSandboxes removes scratch directories nothing has touched for the
// retention period. Published files are in storage and are not affected.
func (p *Platform) sweepSandboxes() {
	base := filepath.Join(p.dir, "sandboxes")
	tenants, _ := os.ReadDir(base)
	cutoff := p.now().Add(-SandboxRetention)
	for _, t := range tenants {
		threads, _ := os.ReadDir(filepath.Join(base, t.Name()))
		for _, th := range threads {
			info, err := th.Info()
			if err != nil || !th.IsDir() || info.ModTime().After(cutoff) {
				continue
			}
			if err := os.RemoveAll(filepath.Join(base, t.Name(), th.Name())); err != nil {
				p.log.Warn("platform: a sandbox could not be removed", "error", err)
			}
		}
	}
}

func arg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func schema(required []string, props map[string]string) saigetypes.ParameterSchema {
	out := saigetypes.ParameterSchema{Type: "object", Required: required, Properties: map[string]saigetypes.PropertyDef{}}
	for name, desc := range props {
		out.Properties[name] = saigetypes.PropertyDef{Type: "string", Description: desc}
	}
	return out
}

// fileTools is the catalog of file tools for one turn. An agent has a tool
// only if its spec names it; this decides what the tool does, not who has it.
func (p *Platform) fileTools(base runtime.Catalog) runtime.Catalog {
	return func(req runtime.Request, action func(payload.Action) error) (agentrt.Tools, error) {
		var inner agentrt.Tools
		if base != nil {
			b, err := base(req, action)
			if err != nil {
				return nil, err
			}
			inner = b
		}
		tools := p.turnFileTools(req, action)
		return agentrt.ToolsFunc(func(name string) (saigetypes.Tool, error) {
			if t, ok := tools[name]; ok {
				return t, nil
			}
			if inner == nil {
				return nil, fmt.Errorf("%w: %q", agentrt.ErrNoSuchTool, name)
			}
			return inner.Lookup(name)
		}), nil
	}
}

func (p *Platform) turnFileTools(req runtime.Request, action func(payload.Action) error) map[string]saigetypes.Tool {
	// The conversation is looked up when a tool is used, not when the turn
	// starts. A turn delegated by another agent inside its own turn has no
	// run of its own, so it has no conversation and no files.
	where := func() (thread, project string, root *os.Root, err error) {
		if req.RunID == "" {
			return "", "", nil, fmt.Errorf("%w: files are not available to a turn another agent delegated", ErrSandbox)
		}
		run, err := p.Runs.Get(req.Tenant, req.RunID)
		if err != nil {
			return "", "", nil, fmt.Errorf("%w: this run has no conversation", ErrSandbox)
		}
		thread = run.Thread
		if thread == "" {
			thread = run.ID
		}
		root, err = p.openSandbox(req.Tenant, thread)
		return thread, run.Project, root, err
	}
	tool := func(name, desc string, params saigetypes.ParameterSchema, fn func(ctx context.Context, args map[string]any) (string, error)) saigetypes.Tool {
		return &saigetypes.ToolFunc{Def: saigetypes.ToolDef{Name: name, Description: desc, Parameters: params}, Fn: fn}
	}
	return map[string]saigetypes.Tool{
		ToolDelegate:  p.delegateTool(req, action),
		ToolCallAgent: p.callAgentTool(req, action),
		ToolFileWrite: tool(ToolFileWrite,
			"Write a text file in this conversation's working directory, replacing it if it exists. Files stay for later turns. Publish one with publish_artifact to give it to the person.",
			schema([]string{"path", "content"}, map[string]string{"path": "Relative path, such as report.md or data/q3.csv", "content": "The whole content of the file"}),
			func(_ context.Context, args map[string]any) (string, error) {
				_, _, root, err := where()
				if err != nil {
					return "", err
				}
				defer func() { _ = root.Close() }()
				f, err := writeSandbox(root, arg(args, "path"), arg(args, "content"))
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Wrote %s (%d bytes).", f.Path, f.Size), nil
			}),
		ToolFileRead: tool(ToolFileRead,
			"Read a file from this conversation's working directory.",
			schema([]string{"path"}, map[string]string{"path": "Relative path of the file"}),
			func(_ context.Context, args map[string]any) (string, error) {
				_, _, root, err := where()
				if err != nil {
					return "", err
				}
				defer func() { _ = root.Close() }()
				b, _, err := readSandbox(root, arg(args, "path"))
				if err != nil {
					return "", err
				}
				if !utf8.Valid(b) {
					return "", fmt.Errorf("%w: that file is not text", ErrSandbox)
				}
				return string(b), nil
			}),
		ToolFileList: tool(ToolFileList,
			"List the files in this conversation's working directory.",
			schema(nil, nil),
			func(context.Context, map[string]any) (string, error) {
				_, _, root, err := where()
				if err != nil {
					return "", err
				}
				defer func() { _ = root.Close() }()
				files, _, err := listSandbox(root)
				if err != nil {
					return "", fmt.Errorf("platform: sandbox: %w", err)
				}
				if len(files) == 0 {
					return "No files yet.", nil
				}
				var b strings.Builder
				for _, f := range files {
					fmt.Fprintf(&b, "%s (%d bytes)\n", f.Path, f.Size)
				}
				return b.String(), nil
			}),
		ToolPublish: tool(ToolPublish,
			"Publish a file from the working directory so the person can open and download it. Publishing a title again in the same conversation makes a new version.",
			schema([]string{"path"}, map[string]string{
				"path":        "Relative path of the file to publish",
				"title":       "File name to show and save it under. Defaults to the file's own name",
				"kind":        "One of document, spreadsheet, presentation, image, pdf, html, code, data, archive. Leave out to have it worked out",
				"description": "One line on what is in it",
			}),
			func(ctx context.Context, args map[string]any) (string, error) {
				thread, project, root, err := where()
				if err != nil {
					return "", err
				}
				_ = root.Close()
				a, err := p.Publish(ctx, Publication{
					Tenant: req.Tenant, Thread: thread, Run: req.RunID, Agent: req.Agent(), Project: project, By: "agent:" + req.Agent(),
					Path: arg(args, "path"), Title: arg(args, "title"), Kind: arg(args, "kind"), Description: arg(args, "description"),
				})
				if err != nil {
					// The failure goes on the run's stream as well as back to
					// the model, so the conversation shows a file that failed.
					_ = action(payload.Action{Name: ToolPublish, Summary: arg(args, "title"), Error: err.Error()})
					return "", err
				}
				data, _ := json.Marshal(a)
				_ = action(payload.Action{Name: ToolPublish, Summary: fmt.Sprintf("Published %s, version %d", a.Title, a.Version), Data: data})
				return fmt.Sprintf("Published %s as version %d (%d bytes). The person can open it now.", a.Title, a.Version, a.Size), nil
			}),
	}
}
