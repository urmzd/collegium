package knowledge

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func seeded(t *testing.T) *Store {
	t.Helper()
	s := NewStore()
	if _, err := s.Create("acme", "handbook", "How we work", "ada", now); err != nil {
		t.Fatal(err)
	}
	for title, text := range map[string]string{
		"Releases": "Merging to main runs the release workflow. It builds, signs and uploads the binaries.\n\nA release is tagged by the release bot, never by hand.",
		"On call":  "The on-call engineer acknowledges a page within five minutes.\n\nEscalate to the incident commander after fifteen.",
	} {
		if _, err := s.Add("acme", "handbook", title, "", text, "ada", now); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestSearchRanksTheRightPassage(t *testing.T) {
	s := seeded(t)
	hits, err := s.Search("acme", nil, "who tags a release?", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Title != "Releases" || !strings.Contains(hits[0].Text, "release") {
		t.Fatalf("hits = %+v", hits)
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].Score > hits[i-1].Score {
			t.Fatalf("hits are not ranked: %+v", hits)
		}
	}
}

func TestNoMatchIsEmptyNotAnError(t *testing.T) {
	hits, err := seeded(t).Search("acme", nil, "zeppelin", 0)
	if err != nil || len(hits) != 0 {
		t.Fatalf("got %v, %v", hits, err)
	}
}

func TestTenantsAreIsolated(t *testing.T) {
	s := seeded(t)
	if got := s.List("globex"); len(got) != 0 {
		t.Fatalf("other tenant listed %v", got)
	}
	if hits, _ := s.Search("globex", nil, "release", 0); len(hits) != 0 {
		t.Fatalf("other tenant searched into %v", hits)
	}
	if _, err := s.Search("globex", []string{"handbook"}, "release", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant named the source: %v", err)
	}
	if _, err := s.Documents("globex", "handbook"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant listed documents: %v", err)
	}
	if _, err := s.Add("globex", "handbook", "x", "", "y", "eve", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant added a document: %v", err)
	}
	if err := s.Delete("globex", "handbook"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant deleted the source: %v", err)
	}
	// The same name in another tenant is a different source.
	if _, err := s.Create("globex", "handbook", "", "eve", now); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search("globex", nil, "release", 0); len(hits) != 0 {
		t.Fatalf("a same-named source leaked: %v", hits)
	}
}

func TestRemoveADocument(t *testing.T) {
	s := seeded(t)
	docs, _ := s.Documents("acme", "handbook")
	for _, d := range docs {
		if d.Title == "Releases" {
			if err := s.Remove("acme", "handbook", d.ID, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	if hits, _ := s.Search("acme", nil, "release workflow", 0); len(hits) != 0 {
		t.Fatalf("a removed document still matches: %v", hits)
	}
	if got := s.List("acme")[0]; got.Documents != 1 {
		t.Fatalf("counts are stale: %+v", got)
	}
}

func TestInvalid(t *testing.T) {
	s := seeded(t)
	if _, err := s.Create("acme", "Bad Name", "", "ada", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("name: %v", err)
	}
	if _, err := s.Create("acme", "handbook", "", "ada", now); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := s.Add("acme", "handbook", "", "", "text", "ada", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no title: %v", err)
	}
	if _, err := s.Search("acme", nil, "  ?!", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty query: %v", err)
	}
}

func TestLongTextIsCutIntoPassages(t *testing.T) {
	words := strings.Repeat("déploiement ", 400)
	parts := split(words)
	if len(parts) < 4 {
		t.Fatalf("got %d passages", len(parts))
	}
	for _, p := range parts {
		if len(p) > passageSize || !strings.HasPrefix(p, "déploiement") {
			t.Fatalf("bad passage (%d bytes): %q", len(p), p[:20])
		}
	}
}
