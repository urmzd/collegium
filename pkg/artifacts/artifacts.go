// Package artifacts is the record of published files: what each one is, where
// it came from, and which version of it this is.
//
// The bytes live in a blob store. This index holds everything else, and is
// what the console's storage page and the download route read.
package artifacts

import (
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Errors a caller can match.
var (
	ErrNotFound = errors.New("artifacts: not found")
	ErrInvalid  = errors.New("artifacts: invalid")
	ErrCursor   = errors.New("artifacts: invalid cursor")
)

// Kind says what sort of file an artifact is, for how it is shown.
type Kind string

// The kinds the console knows how to show.
const (
	Document     Kind = "document"
	Spreadsheet  Kind = "spreadsheet"
	Presentation Kind = "presentation"
	Image        Kind = "image"
	PDF          Kind = "pdf"
	HTML         Kind = "html"
	Code         Kind = "code"
	Data         Kind = "data"
	Archive      Kind = "archive"
)

// Kinds lists every kind.
func Kinds() []Kind {
	return []Kind{Document, Spreadsheet, Presentation, Image, PDF, HTML, Code, Data, Archive}
}

// MaxTitle bounds a title, which is also the name a download is saved under.
const MaxTitle = 120

// Artifact is one published version of a file.
type Artifact struct {
	ID string `json:"id"`
	// Title is the file's name, as a person will save it.
	Title string `json:"title"`
	Kind  Kind   `json:"kind"`
	// Mime is what the bytes were found to be, not what anyone said they were.
	Mime    string `json:"mime"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Version int    `json:"version"`
	Run     string `json:"run"`
	Thread  string `json:"thread"`
	Agent   string `json:"agent"`
	Project string `json:"project,omitempty"`
	// Description is the publisher's line about what is in it.
	Description string `json:"description,omitempty"`
	// Preview is the start of a text file.
	Preview   string    `json:"preview,omitempty"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	// Key is where the bytes are in the blob store.
	Key string `json:"-"`
}

// CleanTitle turns what a model called a file into a name that is safe to
// save a download under: the last path element, no control characters, bounded.
func CleanTitle(title string) (string, error) {
	title = path.Base(strings.ReplaceAll(strings.TrimSpace(title), `\`, "/"))
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`"<>:|?*`, r) {
			return -1
		}
		return r
	}, title)
	title = strings.TrimLeft(strings.TrimSpace(title), ".")
	if title == "" || title == "/" {
		return "", fmt.Errorf("%w: an artifact needs a file name for a title", ErrInvalid)
	}
	if len(title) > MaxTitle {
		ext := path.Ext(title)
		if len(ext) > 12 {
			ext = ""
		}
		keep := MaxTitle - len(ext)
		for keep > 0 && title[keep]&0xC0 == 0x80 {
			keep--
		}
		title = title[:keep] + ext
	}
	return title, nil
}

type tenant struct {
	items []*Artifact // oldest first
	byID  map[string]*Artifact
	next  int
}

// Index holds every tenant's artifacts.
type Index struct {
	mu      sync.RWMutex
	tenants map[string]*tenant
}

// NewIndex returns an empty index.
func NewIndex() *Index { return &Index{tenants: map[string]*tenant{}} }

func (x *Index) of(name string) *tenant {
	t, ok := x.tenants[name]
	if !ok {
		t = &tenant{byID: map[string]*Artifact{}}
		x.tenants[name] = t
	}
	return t
}

// NextID reserves the id the next artifact will have, so its bytes can be
// stored under it before it is recorded.
func (x *Index) NextID(tenantName string) string {
	x.mu.Lock()
	defer x.mu.Unlock()
	t := x.of(tenantName)
	t.next++
	return "art-" + strconv.Itoa(t.next)
}

// Publish records an artifact under an id from NextID. A file with a title
// already published in the same conversation is the next version of it.
func (x *Index) Publish(tenantName string, a Artifact) (Artifact, error) {
	if a.ID == "" || a.Key == "" || a.Thread == "" || a.Title == "" {
		return Artifact{}, fmt.Errorf("%w: an artifact needs an id, a key, a conversation and a title", ErrInvalid)
	}
	if !slices.Contains(Kinds(), a.Kind) {
		return Artifact{}, fmt.Errorf("%w: kind %q is not one of %v", ErrInvalid, a.Kind, Kinds())
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	t := x.of(tenantName)
	if t.byID[a.ID] != nil {
		return Artifact{}, fmt.Errorf("%w: %s is already published", ErrInvalid, a.ID)
	}
	a.Version = 1
	for _, o := range t.items {
		if o.Thread == a.Thread && o.Title == a.Title && o.Version >= a.Version {
			a.Version = o.Version + 1
		}
	}
	t.items = append(t.items, &a)
	t.byID[a.ID] = &a
	return a, nil
}

// Get returns one artifact.
func (x *Index) Get(tenantName, id string) (Artifact, error) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	a := x.of(tenantName).byID[id]
	if a == nil {
		return Artifact{}, fmt.Errorf("%w: artifact %q", ErrNotFound, id)
	}
	return *a, nil
}

// Versions returns every version of the file an artifact is a version of,
// newest first.
func (x *Index) Versions(tenantName, id string) ([]Artifact, error) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	t := x.of(tenantName)
	a := t.byID[id]
	if a == nil {
		return nil, fmt.Errorf("%w: artifact %q", ErrNotFound, id)
	}
	var out []Artifact
	for i := len(t.items) - 1; i >= 0; i-- {
		if o := t.items[i]; o.Thread == a.Thread && o.Title == a.Title {
			out = append(out, *o)
		}
	}
	return out, nil
}

// Delete removes one version and returns it, so its bytes can be removed too.
func (x *Index) Delete(tenantName, id string) (Artifact, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	t := x.of(tenantName)
	a := t.byID[id]
	if a == nil {
		return Artifact{}, fmt.Errorf("%w: artifact %q", ErrNotFound, id)
	}
	delete(t.byID, id)
	t.items = slices.DeleteFunc(t.items, func(o *Artifact) bool { return o.ID == id })
	return *a, nil
}

// Paging bounds for Page.
const (
	DefaultPage = 50
	MaxPage     = 200
)

// Query narrows a page of artifacts. Empty fields match everything.
type Query struct {
	Project string
	Agent   string
	Run     string
	Thread  string
	Kind    Kind
	// Title keeps files whose title contains this, whatever the case.
	Title  string
	Limit  int
	Before string
}

func (q Query) match(a *Artifact) bool {
	return (q.Project == "" || a.Project == q.Project) && (q.Agent == "" || a.Agent == q.Agent) &&
		(q.Run == "" || a.Run == q.Run) && (q.Thread == "" || a.Thread == q.Thread) && (q.Kind == "" || a.Kind == q.Kind) &&
		(q.Title == "" || strings.Contains(strings.ToLower(a.Title), strings.ToLower(q.Title)))
}

func encodeCursor(a *Artifact) string {
	return base64.RawURLEncoding.EncodeToString([]byte(a.ID))
}

func seq(id string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "art-"))
	return n, err == nil && strings.HasPrefix(id, "art-")
}

// Page returns one page of a tenant's artifacts, newest first, and the cursor
// for the page after it. Only the latest version of each file is listed. Ids
// are issued in order and never reused, so a page neither repeats nor skips
// when something is published or deleted between pages.
func (x *Index) Page(tenantName string, q Query) ([]Artifact, string, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultPage
	}
	limit = min(limit, MaxPage)
	before := 0
	if q.Before != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Before)
		n, ok := seq(string(raw))
		if err != nil || !ok {
			return nil, "", ErrCursor
		}
		before = n
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	t := x.of(tenantName)
	// A newer version of the same file hides the older ones.
	latest := map[string]bool{}
	out := []Artifact{}
	var last *Artifact
	for i := len(t.items) - 1; i >= 0; i-- {
		a := t.items[i]
		file := a.Thread + "\x00" + a.Title
		if latest[file] {
			continue
		}
		latest[file] = true
		if n, _ := seq(a.ID); before > 0 && n >= before {
			continue
		}
		if !q.match(a) {
			continue
		}
		if len(out) == limit {
			return out, encodeCursor(last), nil
		}
		out = append(out, *a)
		last = a
	}
	return out, "", nil
}

// Share is how much of a tenant's storage one project or agent holds.
type Share struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Count int    `json:"count"`
}

// Usage is what a tenant has stored, every version counted.
type Usage struct {
	Bytes     int64   `json:"used_bytes"`
	Count     int     `json:"count"`
	ByProject []Share `json:"by_project"`
	ByAgent   []Share `json:"by_agent"`
}

// Usage totals a tenant's storage.
func (x *Index) Usage(tenantName string) Usage {
	x.mu.RLock()
	defer x.mu.RUnlock()
	u := Usage{}
	projects, agents := map[string]*Share{}, map[string]*Share{}
	add := func(m map[string]*Share, name string, size int64) {
		if name == "" {
			return
		}
		if m[name] == nil {
			m[name] = &Share{Name: name}
		}
		m[name].Bytes += size
		m[name].Count++
	}
	for _, a := range x.of(tenantName).items {
		u.Bytes += a.Size
		u.Count++
		add(projects, a.Project, a.Size)
		add(agents, a.Agent, a.Size)
	}
	flat := func(m map[string]*Share) []Share {
		out := make([]Share, 0, len(m))
		for _, s := range m {
			out = append(out, *s)
		}
		slices.SortFunc(out, func(a, b Share) int {
			if a.Bytes != b.Bytes {
				return int(b.Bytes - a.Bytes)
			}
			return strings.Compare(a.Name, b.Name)
		})
		return out
	}
	u.ByProject, u.ByAgent = flat(projects), flat(agents)
	return u
}

// Used returns the bytes a tenant holds, or one of its projects when named.
func (x *Index) Used(tenantName, project string) int64 {
	x.mu.RLock()
	defer x.mu.RUnlock()
	var n int64
	for _, a := range x.of(tenantName).items {
		if project == "" || a.Project == project {
			n += a.Size
		}
	}
	return n
}
