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
	hits, err := s.Search("acme", nil, "who tags a release?", 0, "")
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
	hits, err := seeded(t).Search("acme", nil, "zeppelin", 0, "")
	if err != nil || len(hits) != 0 {
		t.Fatalf("got %v, %v", hits, err)
	}
}

func TestTenantsAreIsolated(t *testing.T) {
	s := seeded(t)
	if got := s.List("globex"); len(got) != 0 {
		t.Fatalf("other tenant listed %v", got)
	}
	if hits, _ := s.Search("globex", nil, "release", 0, ""); len(hits) != 0 {
		t.Fatalf("other tenant searched into %v", hits)
	}
	if _, err := s.Search("globex", []string{"handbook"}, "release", 0, ""); !errors.Is(err, ErrNotFound) {
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
	if hits, _ := s.Search("globex", nil, "release", 0, ""); len(hits) != 0 {
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
	if hits, _ := s.Search("acme", nil, "release workflow", 0, ""); len(hits) != 0 {
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
	if _, err := s.Search("acme", nil, "  ?!", 0, ""); !errors.Is(err, ErrInvalid) {
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

func TestExcludeAndVariants(t *testing.T) {
	s := seeded(t)
	docs, _ := s.Documents("acme", "handbook")
	var releases string
	for _, d := range docs {
		if d.Title == "Releases" {
			releases = d.ID
		}
	}
	hits := func(variant string) int {
		h, err := s.Search("acme", []string{"handbook"}, "release workflow", 0, variant)
		if err != nil {
			t.Fatal(err)
		}
		return len(h)
	}
	if hits("") == 0 {
		t.Fatal("nothing matched before excluding")
	}

	// A view saved now keeps everything in.
	if _, err := s.SaveVariant("acme", "handbook", "everything", now); err != nil {
		t.Fatal(err)
	}
	// Excluding keeps the document but takes it out of search.
	if _, err := s.SetExcluded("acme", "handbook", releases, true, now); err != nil {
		t.Fatal(err)
	}
	if hits("") != 0 {
		t.Fatal("an excluded document still matches")
	}
	if got := s.List("acme")[0]; got.Documents != 2 || got.Excluded != 1 {
		t.Fatalf("counts = %+v", got)
	}
	// The saved view is unaffected by what is excluded now, and a view saved
	// now records it.
	if hits("everything") == 0 {
		t.Fatal("the saved view lost a document that was excluded after it was saved")
	}
	if _, err := s.SaveVariant("acme", "handbook", "no-releases", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetExcluded("acme", "handbook", releases, false, now); err != nil {
		t.Fatal(err)
	}
	if hits("") == 0 || hits("no-releases") != 0 {
		t.Fatal("the view did not keep its own exclusions")
	}

	if _, err := s.Search("acme", []string{"handbook"}, "release", 0, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown variant: %v", err)
	}
	if _, err := s.Search("acme", nil, "release", 0, "everything"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a variant with no source: %v", err)
	}
	if _, err := s.SaveVariant("acme", "handbook", "Bad Name", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bad variant name: %v", err)
	}
	for name, err := range map[string]error{
		"exclude": func() error { _, e := s.SetExcluded("globex", "handbook", releases, true, now); return e }(),
		"save":    func() error { _, e := s.SaveVariant("globex", "handbook", "x", now); return e }(),
		"delete":  s.DeleteVariant("globex", "handbook", "everything"),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("other tenant %s: %v", name, err)
		}
	}
	// Removing a document removes it from every view.
	_ = s.Remove("acme", "handbook", releases, now)
	for _, v := range s.List("acme")[0].Variants {
		if len(v.Excluded) != 0 {
			t.Fatalf("variant %s still names a removed document", v.Name)
		}
	}
}
