// Package evals measures how well an agent or a knowledge source is doing.
//
// A suite is a list of cases against one target. An agent case sends the agent
// an input and checks its answer. A knowledge case asks a source a question and
// checks that the right document comes back. Running a suite produces a run,
// which pins what was tested, so two runs can be compared across revisions.
//
// # Deterministic on purpose
//
// Every check here is a pure function of the output: it contains this, it
// matches that, it answered within a limit, the expected document ranked in the
// top k. Nothing asks a model to grade a model, so the same answer always gets
// the same score, and a score that moved means the agent moved.
//
// The scoring itself is saige's: this package declares cases and stores
// results, and hands the arithmetic to saige/eval and saige/rag/eval.
//
// Suites and runs are in memory and are lost on restart.
package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	saigeeval "github.com/urmzd/saige/eval"
	rageval "github.com/urmzd/saige/rag/eval"
	ragtypes "github.com/urmzd/saige/rag/types"
)

// Errors.
var (
	ErrInvalid  = errors.New("evals: invalid")
	ErrExists   = errors.New("evals: suite already exists")
	ErrNotFound = errors.New("evals: not found")
	ErrBusy     = errors.New("evals: suite is already running")
)

// Limits. A suite is run by a person waiting on it, so it is kept small.
const (
	MaxCases  = 50
	MaxChecks = 10
	maxRuns   = 50
	DefaultK  = 5
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Target is what a suite tests.
type Target string

const (
	// Agent suites send inputs to an agent and check its answers.
	Agent Target = "agent"
	// Knowledge suites send queries to a source and check what is retrieved.
	Knowledge Target = "knowledge"
)

// CheckKind is one deterministic test of an answer.
type CheckKind string

const (
	// Contains passes when the answer includes the value, ignoring case.
	Contains CheckKind = "contains"
	// NotContains passes when the answer does not include the value.
	NotContains CheckKind = "not_contains"
	// Matches passes when the answer matches the value as a regular expression.
	Matches CheckKind = "matches"
	// Equals passes when the answer is the value, ignoring surrounding space.
	Equals CheckKind = "equals"
	// Within passes when the run ended within the value, in milliseconds.
	Within CheckKind = "within_ms"
)

// Check is one test applied to an agent's answer.
type Check struct {
	Kind  CheckKind `json:"kind"`
	Value string    `json:"value"`
}

// Case is one input and what a good result looks like.
type Case struct {
	ID string `json:"id"`
	// Input is the request sent to an agent, or the query sent to a source.
	Input string `json:"input"`
	// Checks apply to an agent's answer. Every one must pass.
	Checks []Check `json:"checks,omitempty"`
	// Expect names the documents, by title, that a knowledge query should
	// retrieve. The case passes when at least one ranks in the top K.
	Expect []string `json:"expect,omitempty"`
}

// Suite is a named list of cases against one target.
type Suite struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Target      Target `json:"target"`
	// Subject is the agent's name or the knowledge source's name.
	Subject string `json:"subject"`
	Cases   []Case `json:"cases"`
	// K is how many results a knowledge query is judged on. Zero means DefaultK.
	K int `json:"k,omitempty"`
	// PassRate is the share of cases, 0 to 100, that must pass for the run to
	// pass. Zero means every case.
	PassRate  float64   `json:"pass_rate,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Threshold is the pass rate a run must reach.
func (s Suite) Threshold() float64 {
	if s.PassRate <= 0 {
		return 100
	}
	return s.PassRate
}

// Validate checks a suite before it is stored.
func (s Suite) Validate() error {
	switch {
	case !validName.MatchString(s.Name):
		return fmt.Errorf("%w: a suite name is lowercase letters, digits and dashes", ErrInvalid)
	case s.Target != Agent && s.Target != Knowledge:
		return fmt.Errorf("%w: a suite targets an agent or a knowledge source", ErrInvalid)
	case s.Subject == "":
		return fmt.Errorf("%w: a suite needs something to test", ErrInvalid)
	case len(s.Cases) == 0:
		return fmt.Errorf("%w: a suite needs at least one case", ErrInvalid)
	case len(s.Cases) > MaxCases:
		return fmt.Errorf("%w: a suite holds at most %d cases", ErrInvalid, MaxCases)
	case s.PassRate < 0 || s.PassRate > 100:
		return fmt.Errorf("%w: the pass rate is a percentage from 0 to 100", ErrInvalid)
	case s.K < 0 || s.K > 50:
		return fmt.Errorf("%w: k is from 1 to 50", ErrInvalid)
	}
	seen := map[string]bool{}
	for i, c := range s.Cases {
		n := i + 1
		switch {
		case c.ID == "" || seen[c.ID]:
			return fmt.Errorf("%w: case %d needs an id no other case has", ErrInvalid, n)
		case strings.TrimSpace(c.Input) == "":
			return fmt.Errorf("%w: case %d has no input", ErrInvalid, n)
		}
		seen[c.ID] = true
		if s.Target == Knowledge {
			if len(c.Expect) == 0 || slices.Contains(c.Expect, "") {
				return fmt.Errorf("%w: case %d names no document it expects", ErrInvalid, n)
			}
			continue
		}
		if len(c.Checks) == 0 {
			return fmt.Errorf("%w: case %d has no checks, so it could never fail", ErrInvalid, n)
		}
		if len(c.Checks) > MaxChecks {
			return fmt.Errorf("%w: case %d has more than %d checks", ErrInvalid, n, MaxChecks)
		}
		for _, ch := range c.Checks {
			if _, err := scorerOf(ch); err != nil {
				return fmt.Errorf("%w: case %d: %w", ErrInvalid, n, err)
			}
		}
	}
	return nil
}

// Status is where a run stands.
type Status string

const (
	Running Status = "running"
	Passed  Status = "passed"
	Failed  Status = "failed"
	// Errored means the run could not be carried out, which is different
	// from being carried out and failing.
	Errored Status = "errored"
)

// Verdict is one check's result on one case.
type Verdict struct {
	Check  string `json:"check"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason,omitempty"`
}

// Hit is one retrieved document in a knowledge case's result.
type Hit struct {
	Title string  `json:"title"`
	Score float64 `json:"score"`
	// Expected reports whether the case named this document.
	Expected bool `json:"expected"`
}

// Result is how one case went.
type Result struct {
	CaseID string `json:"case_id"`
	Input  string `json:"input"`
	Passed bool   `json:"passed"`
	// Output is the agent's answer.
	Output   string    `json:"output,omitempty"`
	Verdicts []Verdict `json:"verdicts,omitempty"`
	// Hits and Rank describe a knowledge case. Rank is the position of the
	// first expected document, from 1, or 0 when none was retrieved.
	Hits []Hit `json:"hits,omitempty"`
	Rank int   `json:"rank,omitempty"`
	// RunID is the agent run behind the case, for opening its trace.
	RunID      string `json:"run_id,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// Run is one execution of a suite.
type Run struct {
	ID      string `json:"id"`
	Suite   string `json:"suite"`
	Target  Target `json:"target"`
	Subject string `json:"subject"`
	// Rev is the agent revision that was tested.
	Rev    int    `json:"rev,omitempty"`
	Status Status `json:"status"`
	Passed int    `json:"passed"`
	Total  int    `json:"total"`
	// PassRate is the share of cases that passed, 0 to 100.
	PassRate  float64 `json:"pass_rate"`
	Threshold float64 `json:"threshold"`
	// Metrics are retrieval means for a knowledge run: hit_rate, mrr,
	// recall, precision and ndcg, each from 0 to 1.
	Metrics   map[string]float64 `json:"metrics,omitempty"`
	Results   []Result           `json:"results"`
	Error     string             `json:"error,omitempty"`
	StartedBy string             `json:"started_by,omitempty"`
	StartedAt time.Time          `json:"started_at"`
	EndedAt   time.Time          `json:"ended_at,omitzero"`
}

// scorerOf turns a check into the saige scorer that carries it out.
func scorerOf(c Check) (saigeeval.Scorer, error) {
	if c.Value == "" {
		return nil, fmt.Errorf("a %q check needs a value", c.Kind)
	}
	name := string(c.Kind) + ": " + c.Value
	text := func(pred func(out string) (bool, string)) saigeeval.Scorer {
		return saigeeval.NewCheckScorer(name, func(_ context.Context, obs saigeeval.Observation) (bool, string, error) {
			ok, reason := pred(saigeeval.OutputText(obs))
			return ok, reason, nil
		})
	}
	switch c.Kind {
	case Contains:
		want := strings.ToLower(c.Value)
		return text(func(out string) (bool, string) {
			if strings.Contains(strings.ToLower(out), want) {
				return true, ""
			}
			return false, "the answer does not contain it"
		}), nil
	case NotContains:
		bad := strings.ToLower(c.Value)
		return text(func(out string) (bool, string) {
			if strings.Contains(strings.ToLower(out), bad) {
				return false, "the answer contains it"
			}
			return true, ""
		}), nil
	case Equals:
		return text(func(out string) (bool, string) {
			if strings.TrimSpace(out) == strings.TrimSpace(c.Value) {
				return true, ""
			}
			return false, "the answer is different"
		}), nil
	case Matches:
		re, err := regexp.Compile(c.Value)
		if err != nil {
			return nil, fmt.Errorf("%q is not a regular expression: %v", c.Value, err)
		}
		return text(func(out string) (bool, string) {
			if re.MatchString(out) {
				return true, ""
			}
			return false, "the answer does not match"
		}), nil
	case Within:
		var ms int64
		if _, err := fmt.Sscan(c.Value, &ms); err != nil || ms <= 0 {
			return nil, fmt.Errorf("a within_ms check needs a number of milliseconds above zero")
		}
		return saigeeval.Named(saigeeval.RespondsWithinScorer(time.Duration(ms)*time.Millisecond), name), nil
	}
	return nil, fmt.Errorf("unknown check %q", c.Kind)
}

// ScoreAnswer checks an agent's answer against a case.
func ScoreAnswer(ctx context.Context, c Case, output string, took time.Duration) (Result, error) {
	res := Result{CaseID: c.ID, Input: c.Input, Output: output, DurationMs: took.Milliseconds()}
	scorers := make([]saigeeval.Scorer, 0, len(c.Checks))
	for _, ch := range c.Checks {
		s, err := scorerOf(ch)
		if err != nil {
			return res, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		scorers = append(scorers, s)
	}
	in, _ := json.Marshal(c.Input)
	out, _ := json.Marshal(output)
	suite, err := saigeeval.Run(ctx, c.ID, []saigeeval.Observation{{
		ID: c.ID, Input: in, Output: out,
		// saige declines a time check on an observation with no recorded
		// time, so a run faster than a millisecond is recorded as one.
		Timing: saigeeval.ObservationTiming{TotalMs: max(took.Milliseconds(), 1)},
	}}, scorers)
	if err != nil {
		return res, err
	}
	for _, r := range suite.Results {
		for _, s := range r.Scores {
			// A check scores 1 or 0.
			ok := s.Error == "" && s.Value >= 1
			reason := s.Reason
			if s.Error != "" {
				reason = s.Error
			}
			res.Verdicts = append(res.Verdicts, Verdict{Check: s.Name, Passed: ok, Reason: reason})
		}
	}
	// Every check must have reported and passed. A check that went missing is
	// a failure, never a silent pass.
	res.Passed = len(res.Verdicts) == len(c.Checks)
	for _, v := range res.Verdicts {
		res.Passed = res.Passed && v.Passed
	}
	return res, nil
}

// Retrieved is one document a knowledge search returned, best first.
type Retrieved struct {
	Title string
	Score float64
}

// ScoreRetrieval checks what a source returned for a case. It returns the
// result and the retrieval scores saige computed for it.
func ScoreRetrieval(c Case, got []Retrieved, k int, took time.Duration) (Result, rageval.RetrievalScores) {
	if k <= 0 {
		k = DefaultK
	}
	res := Result{CaseID: c.ID, Input: c.Input, DurationMs: took.Milliseconds()}
	// A document is identified by its title, lowercased, on both sides.
	key := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	want := make([]string, len(c.Expect))
	for i, e := range c.Expect {
		want[i] = key(e)
	}
	hits := make([]ragtypes.SearchHit, 0, len(got))
	seen := map[string]bool{}
	for _, g := range got {
		id := key(g.Title)
		hits = append(hits, ragtypes.SearchHit{Score: g.Score, Provenance: ragtypes.Provenance{DocumentUUID: id}})
		if seen[id] {
			continue
		}
		seen[id] = true
		if len(res.Hits) < k {
			expected := slices.Contains(want, id)
			res.Hits = append(res.Hits, Hit{Title: g.Title, Score: g.Score, Expected: expected})
			if expected && res.Rank == 0 {
				res.Rank = len(res.Hits)
			}
		}
	}
	scores, _ := rageval.ScoreRetrieval(hits, want, rageval.RelevanceDocument, k)
	res.Passed = scores.HitRate > 0
	return res, scores
}

// Finish closes a run from its results.
func (r *Run) Finish(results []Result, metrics map[string]float64, at time.Time) {
	r.Results, r.Metrics, r.EndedAt = results, metrics, at
	r.Total = len(results)
	r.Passed = 0
	for _, res := range results {
		if res.Passed {
			r.Passed++
		}
	}
	if r.Total > 0 {
		r.PassRate = 100 * float64(r.Passed) / float64(r.Total)
	}
	r.Status = Failed
	if r.Total > 0 && r.PassRate >= r.Threshold {
		r.Status = Passed
	}
}

type tenant struct {
	suites map[string]*Suite
	runs   map[string][]Run
	next   int
}

// Store holds every tenant's suites and runs.
type Store struct {
	mu      sync.Mutex
	tenants map[string]*tenant
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{tenants: map[string]*tenant{}} }

func (s *Store) of(name string) *tenant {
	t, ok := s.tenants[name]
	if !ok {
		t = &tenant{suites: map[string]*Suite{}, runs: map[string][]Run{}}
		s.tenants[name] = t
	}
	return t
}

// Put creates a suite, or replaces one when replace is set.
func (s *Store) Put(tenantName string, suite Suite, replace bool) error {
	if err := suite.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.of(tenantName)
	have, exists := t.suites[suite.Name]
	switch {
	case exists && !replace:
		return fmt.Errorf("%w: %q", ErrExists, suite.Name)
	case !exists && replace:
		return fmt.Errorf("%w: suite %q", ErrNotFound, suite.Name)
	case exists && (have.Target != suite.Target || have.Subject != suite.Subject):
		// History compares like with like, so a suite keeps its target.
		return fmt.Errorf("%w: a suite cannot change what it tests", ErrInvalid)
	}
	t.suites[suite.Name] = &suite
	return nil
}

// Get returns one suite.
func (s *Store) Get(tenantName, name string) (Suite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	suite, ok := s.of(tenantName).suites[name]
	if !ok {
		return Suite{}, fmt.Errorf("%w: suite %q", ErrNotFound, name)
	}
	return *suite, nil
}

// List returns a tenant's suites by name.
func (s *Store) List(tenantName string) []Suite {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.of(tenantName)
	out := make([]Suite, 0, len(t.suites))
	for _, suite := range t.suites {
		out = append(out, *suite)
	}
	slices.SortFunc(out, func(a, b Suite) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Delete removes a suite and its runs.
func (s *Store) Delete(tenantName, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.of(tenantName)
	if _, ok := t.suites[name]; !ok {
		return fmt.Errorf("%w: suite %q", ErrNotFound, name)
	}
	delete(t.suites, name)
	delete(t.runs, name)
	return nil
}

// Begin opens a run of a suite. Only one run of a suite is open at a time, so
// two people pressing Run do not double the load or interleave their results.
func (s *Store) Begin(tenantName string, suite Suite, rev int, by string, at time.Time) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.of(tenantName)
	if runs := t.runs[suite.Name]; len(runs) > 0 && runs[len(runs)-1].Status == Running {
		return Run{}, fmt.Errorf("%w: %q", ErrBusy, suite.Name)
	}
	t.next++
	run := Run{
		ID: fmt.Sprintf("ev-%d", t.next), Suite: suite.Name, Target: suite.Target, Subject: suite.Subject,
		Rev: rev, Status: Running, Total: len(suite.Cases), Threshold: suite.Threshold(),
		Results: []Result{}, StartedBy: by, StartedAt: at,
	}
	runs := append(t.runs[suite.Name], run)
	if len(runs) > maxRuns {
		runs = runs[len(runs)-maxRuns:]
	}
	t.runs[suite.Name] = runs
	return run, nil
}

// Save stores a run's latest state.
func (s *Store) Save(tenantName string, run Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The runner keeps appending to its own slice, so the store keeps a copy.
	run.Results = slices.Clone(run.Results)
	runs := s.of(tenantName).runs[run.Suite]
	for i := range runs {
		if runs[i].ID == run.ID {
			runs[i] = run
			return
		}
	}
}

// Runs returns a suite's runs, newest first.
func (s *Store) Runs(tenantName, name string) ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.of(tenantName)
	if _, ok := t.suites[name]; !ok {
		return nil, fmt.Errorf("%w: suite %q", ErrNotFound, name)
	}
	out := slices.Clone(t.runs[name])
	slices.Reverse(out)
	return out, nil
}
