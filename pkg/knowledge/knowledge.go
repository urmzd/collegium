// Package knowledge holds the documents agents may be grounded in.
//
// A source is a named collection in one tenant. A document added to it is cut
// into passages, and Search ranks passages against a query with BM25.
//
// The store is in memory and lexical only: no embeddings, and nothing survives
// a restart. It is the smallest thing that makes a source real enough to fill,
// search and assign to an agent.
package knowledge

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Errors.
var (
	ErrInvalid  = errors.New("knowledge: invalid")
	ErrExists   = errors.New("knowledge: source already exists")
	ErrNotFound = errors.New("knowledge: not found")
)

// passageSize is the target length of a passage in bytes.
const passageSize = 800

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Source is a named collection of documents.
type Source struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Documents   int       `json:"documents"`
	Passages    int       `json:"passages"`
	CreatedBy   string    `json:"created_by,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Document is one thing added to a source. Its text is held as passages.
type Document struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	URI      string    `json:"uri,omitempty"`
	Bytes    int       `json:"bytes"`
	Passages int       `json:"passages"`
	AddedBy  string    `json:"added_by,omitempty"`
	AddedAt  time.Time `json:"added_at"`
}

// Hit is one passage that matched a query.
type Hit struct {
	Source     string  `json:"source"`
	DocumentID string  `json:"document_id"`
	Title      string  `json:"title"`
	URI        string  `json:"uri,omitempty"`
	Index      int     `json:"index"`
	Text       string  `json:"text"`
	Score      float64 `json:"score"`
}

type passage struct {
	doc   *Document
	index int
	text  string
	terms map[string]int
	n     int
}

type source struct {
	Source
	docs     []*Document
	passages []*passage
}

// Store holds every tenant's sources. Every method is confined to one tenant.
type Store struct {
	mu      sync.RWMutex
	tenants map[string]map[string]*source
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{tenants: map[string]map[string]*source{}} }

func (s *Store) get(tenant, name string) (*source, error) {
	src, ok := s.tenants[tenant][name]
	if !ok {
		return nil, fmt.Errorf("%w: source %q", ErrNotFound, name)
	}
	return src, nil
}

// Create adds an empty source.
func (s *Store) Create(tenant, name, description, by string, at time.Time) (Source, error) {
	if !validName.MatchString(name) {
		return Source{}, fmt.Errorf("%w: a source name is lowercase letters, digits and dashes", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tenants[tenant][name]; ok {
		return Source{}, fmt.Errorf("%w: %q", ErrExists, name)
	}
	if s.tenants[tenant] == nil {
		s.tenants[tenant] = map[string]*source{}
	}
	src := &source{Source: Source{Name: name, Description: description, CreatedBy: by, UpdatedAt: at}}
	s.tenants[tenant][name] = src
	return src.Source, nil
}

// Delete removes a source and everything in it.
func (s *Store) Delete(tenant, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.get(tenant, name); err != nil {
		return err
	}
	delete(s.tenants[tenant], name)
	return nil
}

// List returns a tenant's sources by name.
func (s *Store) List(tenant string) []Source {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Source, 0, len(s.tenants[tenant]))
	for _, src := range s.tenants[tenant] {
		out = append(out, src.Source)
	}
	slices.SortFunc(out, func(a, b Source) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Documents returns a source's documents, newest first.
func (s *Store) Documents(tenant, name string) ([]Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src, err := s.get(tenant, name)
	if err != nil {
		return nil, err
	}
	out := make([]Document, 0, len(src.docs))
	for _, d := range slices.Backward(src.docs) {
		out = append(out, *d)
	}
	return out, nil
}

// Add cuts a document into passages and stores it.
func (s *Store) Add(tenant, name, title, uri, text, by string, at time.Time) (Document, error) {
	title, text = strings.TrimSpace(title), strings.TrimSpace(text)
	switch {
	case title == "":
		return Document{}, fmt.Errorf("%w: a document needs a title", ErrInvalid)
	case text == "":
		return Document{}, fmt.Errorf("%w: a document needs text", ErrInvalid)
	}
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return Document{}, err
	}
	doc := &Document{ID: hex.EncodeToString(id), Title: title, URI: uri, Bytes: len(text), AddedBy: by, AddedAt: at}

	s.mu.Lock()
	defer s.mu.Unlock()
	src, err := s.get(tenant, name)
	if err != nil {
		return Document{}, err
	}
	for i, p := range split(text) {
		terms := map[string]int{}
		n := 0
		for _, t := range tokens(p) {
			terms[t]++
			n++
		}
		src.passages = append(src.passages, &passage{doc: doc, index: i, text: p, terms: terms, n: n})
		doc.Passages++
	}
	src.docs = append(src.docs, doc)
	src.Documents, src.Passages, src.UpdatedAt = len(src.docs), len(src.passages), at
	return *doc, nil
}

// Remove deletes one document from a source.
func (s *Store) Remove(tenant, name, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, err := s.get(tenant, name)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(src.docs, func(d *Document) bool { return d.ID == id })
	if i < 0 {
		return fmt.Errorf("%w: document %q", ErrNotFound, id)
	}
	src.docs = slices.Delete(src.docs, i, i+1)
	src.passages = slices.DeleteFunc(src.passages, func(p *passage) bool { return p.doc.ID == id })
	src.Documents, src.Passages, src.UpdatedAt = len(src.docs), len(src.passages), at
	return nil
}

// Search ranks passages in the named sources against a query. No names means
// every source in the tenant. A name that does not exist is an error, so a
// caller cannot mistake "no such source" for "no matches".
func (s *Store) Search(tenant string, names []string, query string, limit int) ([]Hit, error) {
	q := tokens(query)
	if len(q) == 0 {
		return nil, fmt.Errorf("%w: a search needs words to look for", ErrInvalid)
	}
	if limit <= 0 || limit > 50 {
		limit = 8
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	var in []*source
	if len(names) == 0 {
		for _, src := range s.tenants[tenant] {
			in = append(in, src)
		}
	}
	for _, n := range names {
		src, err := s.get(tenant, n)
		if err != nil {
			return nil, err
		}
		in = append(in, src)
	}

	// BM25 over the selected passages.
	const k1, b = 1.2, 0.75
	var total, count int
	df := map[string]int{}
	for _, src := range in {
		for _, p := range src.passages {
			total += p.n
			count++
			for _, t := range q {
				if p.terms[t] > 0 {
					df[t]++
				}
			}
		}
	}
	if count == 0 {
		return []Hit{}, nil
	}
	avg := float64(total) / float64(count)
	hits := []Hit{}
	for _, src := range in {
		for _, p := range src.passages {
			var score float64
			for _, t := range q {
				tf := float64(p.terms[t])
				if tf == 0 {
					continue
				}
				idf := math.Log(1 + (float64(count)-float64(df[t])+0.5)/(float64(df[t])+0.5))
				score += idf * tf * (k1 + 1) / (tf + k1*(1-b+b*float64(p.n)/avg))
			}
			if score > 0 {
				hits = append(hits, Hit{
					Source: src.Name, DocumentID: p.doc.ID, Title: p.doc.Title, URI: p.doc.URI,
					Index: p.index, Text: p.text, Score: score,
				})
			}
		}
	}
	slices.SortStableFunc(hits, func(a, b Hit) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return 0
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// split cuts text into passages on blank lines, joining short paragraphs and
// breaking long ones at whitespace.
func split(text string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		para = strings.TrimSpace(para)
		for len(para) > passageSize {
			cut := strings.LastIndexFunc(para[:passageSize], unicode.IsSpace)
			if cut <= 0 {
				cut = passageSize
				for cut > 0 && para[cut]&0xC0 == 0x80 {
					cut--
				}
			}
			flush()
			out = append(out, strings.TrimSpace(para[:cut]))
			para = strings.TrimSpace(para[cut:])
		}
		if para == "" {
			continue
		}
		if cur.Len() > 0 && cur.Len()+len(para) > passageSize {
			flush()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(para)
	}
	flush()
	return out
}

// tokens lowercases text and splits it into words of letters and digits.
func tokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
