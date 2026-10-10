package evals

import (
	"context"
	"errors"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func agentSuite(cases ...Case) Suite {
	return Suite{Name: "smoke", Target: Agent, Subject: "lead", Cases: cases}
}

func TestScoreAnswer(t *testing.T) {
	c := Case{ID: "c1", Input: "hi", Checks: []Check{
		{Contains, "Release Bot"},
		{NotContains, "by hand is fine"},
		{Matches, `v\d+\.\d+`},
		{Within, "500"},
	}}
	good, err := ScoreAnswer(context.Background(), c, "The release bot tags v1.2 for you.", 120*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !good.Passed || len(good.Verdicts) != 4 {
		t.Fatalf("got %+v", good)
	}

	for name, tc := range map[string]struct {
		out  string
		took time.Duration
		fail string
	}{
		"missing text":   {"A person tags v1.2.", 120 * time.Millisecond, "contains: Release Bot"},
		"forbidden text": {"The release bot tags v1.2, though by hand is fine.", 120 * time.Millisecond, "not_contains: by hand is fine"},
		"no version":     {"The release bot tags it.", 120 * time.Millisecond, `matches: v\d+\.\d+`},
		"too slow":       {"The release bot tags v1.2.", 2 * time.Second, "within_ms: 500"},
	} {
		res, err := ScoreAnswer(context.Background(), c, tc.out, tc.took)
		if err != nil {
			t.Fatal(err)
		}
		if res.Passed {
			t.Errorf("%s: the case passed", name)
		}
		for _, v := range res.Verdicts {
			if (v.Check == tc.fail) == v.Passed {
				t.Errorf("%s: check %q passed=%v", name, v.Check, v.Passed)
			}
		}
	}
}

func TestScoringIsDeterministic(t *testing.T) {
	c := Case{ID: "c1", Input: "hi", Checks: []Check{{Equals, "42"}}}
	for range 3 {
		res, err := ScoreAnswer(context.Background(), c, "  42\n", time.Millisecond)
		if err != nil || !res.Passed {
			t.Fatalf("got %+v, %v", res, err)
		}
	}
}

func TestScoreRetrieval(t *testing.T) {
	c := Case{ID: "q1", Input: "who tags a release", Expect: []string{"release process"}}
	got := []Retrieved{{"On Call", 3}, {"On Call", 2.5}, {"Release Process", 2}, {"Glossary", 1}}

	res, scores := ScoreRetrieval(c, got, 3, time.Millisecond)
	// Two passages of one document count as one result, so the expected
	// document is second, not third.
	if !res.Passed || res.Rank != 2 || len(res.Hits) != 3 {
		t.Fatalf("got %+v", res)
	}
	if scores.MRR != 0.5 || scores.HitRate != 1 {
		t.Fatalf("scores = %+v", scores)
	}

	miss, scores := ScoreRetrieval(c, got, 1, time.Millisecond)
	if miss.Passed || miss.Rank != 0 || scores.HitRate != 0 {
		t.Fatalf("a document outside the top k passed: %+v %+v", miss, scores)
	}
	none, _ := ScoreRetrieval(c, nil, 3, time.Millisecond)
	if none.Passed {
		t.Fatal("an empty result passed")
	}
}

func TestFinish(t *testing.T) {
	results := []Result{{Passed: true}, {Passed: true}, {Passed: false}, {Passed: true}}
	strict := Run{Threshold: 100}
	strict.Finish(results, nil, now)
	if strict.Status != Failed || strict.PassRate != 75 || strict.Passed != 3 {
		t.Fatalf("got %+v", strict)
	}
	lenient := Run{Threshold: 75}
	lenient.Finish(results, nil, now)
	if lenient.Status != Passed {
		t.Fatalf("75%% did not meet a 75%% threshold: %+v", lenient)
	}
	empty := Run{Threshold: 100}
	empty.Finish(nil, nil, now)
	if empty.Status != Failed {
		t.Fatal("a run with no results passed")
	}
}

func TestValidate(t *testing.T) {
	ok := Case{ID: "c1", Input: "hi", Checks: []Check{{Contains, "x"}}}
	for name, s := range map[string]Suite{
		"bad name":         {Name: "Bad Name", Target: Agent, Subject: "lead", Cases: []Case{ok}},
		"no target":        {Name: "s", Subject: "lead", Cases: []Case{ok}},
		"no subject":       {Name: "s", Target: Agent, Cases: []Case{ok}},
		"no cases":         {Name: "s", Target: Agent, Subject: "lead"},
		"duplicate ids":    agentSuite(ok, ok),
		"no input":         agentSuite(Case{ID: "c1", Checks: []Check{{Contains, "x"}}}),
		"no checks":        agentSuite(Case{ID: "c1", Input: "hi"}),
		"empty check":      agentSuite(Case{ID: "c1", Input: "hi", Checks: []Check{{Contains, ""}}}),
		"unknown check":    agentSuite(Case{ID: "c1", Input: "hi", Checks: []Check{{"vibes", "good"}}}),
		"bad regex":        agentSuite(Case{ID: "c1", Input: "hi", Checks: []Check{{Matches, "("}}}),
		"bad duration":     agentSuite(Case{ID: "c1", Input: "hi", Checks: []Check{{Within, "soon"}}}),
		"nothing expected": {Name: "s", Target: Knowledge, Subject: "handbook", Cases: []Case{{ID: "q", Input: "x"}}},
		"pass rate":        {Name: "s", Target: Agent, Subject: "lead", Cases: []Case{ok}, PassRate: 140},
	} {
		if err := s.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if err := agentSuite(ok).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStore(t *testing.T) {
	s := NewStore()
	suite := agentSuite(Case{ID: "c1", Input: "hi", Checks: []Check{{Contains, "x"}}})
	if err := s.Put("acme", suite, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("acme", suite, false); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	moved := suite
	moved.Subject = "helper"
	if err := s.Put("acme", moved, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a suite changed what it tests: %v", err)
	}

	// One open run at a time.
	run, err := s.Begin("acme", suite, 3, "ada", now)
	if err != nil || run.Rev != 3 || run.Status != Running {
		t.Fatalf("got %+v, %v", run, err)
	}
	if _, err := s.Begin("acme", suite, 3, "bob", now); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second run started: %v", err)
	}
	run.Finish([]Result{{Passed: true}}, nil, now)
	s.Save("acme", run)
	if _, err := s.Begin("acme", suite, 4, "bob", now); err != nil {
		t.Fatalf("could not run again after the first ended: %v", err)
	}
	runs, _ := s.Runs("acme", "smoke")
	if len(runs) != 2 || runs[0].Rev != 4 || runs[1].Status != Passed {
		t.Fatalf("runs = %+v", runs)
	}

	// Another tenant has none of it.
	if len(s.List("globex")) != 0 {
		t.Fatal("another tenant listed the suite")
	}
	for name, err := range map[string]error{
		"get":    second(s.Get("globex", "smoke")),
		"runs":   second(s.Runs("globex", "smoke")),
		"delete": s.Delete("globex", "smoke"),
		"put":    s.Put("globex", suite, true),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("other tenant %s: %v", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }
