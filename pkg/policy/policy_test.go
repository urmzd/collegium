package policy

import (
	"errors"
	"math"
	"testing"
	"time"
)

var (
	tenantScope = Scope{Kind: Tenant}
	launch      = Scope{Kind: Project, Name: "Launch"}
	lead        = Scope{Kind: Agent, Name: "lead"}
)

func pol(s Scope, mut func(*Policy)) Policy {
	p := Policy{Scope: s, Period: Day, Action: Block}
	mut(&p)
	return p
}

func TestDecide(t *testing.T) {
	runs := func(max int) func(*Policy) { return func(p *Policy) { p.MaxRuns = max } }
	for name, tc := range map[string]struct {
		applied []Applied
		req     Request
		want    Verdict
		limit   Limit
		scope   Scope
		left    int64
	}{
		"no policies":          {nil, Request{}, Allow, "", Scope{}, 0},
		"well under":           {[]Applied{{pol(tenantScope, runs(10)), Usage{Runs: 3}}}, Request{}, Allow, "", Scope{}, 0},
		"reaches the warning":  {[]Applied{{pol(tenantScope, runs(10)), Usage{Runs: 7}}}, Request{}, Warn, LimitRuns, tenantScope, 3},
		"the last one allowed": {[]Applied{{pol(tenantScope, runs(10)), Usage{Runs: 9}}}, Request{}, Warn, LimitRuns, tenantScope, 1},
		"one past the limit":   {[]Applied{{pol(tenantScope, runs(10)), Usage{Runs: 10}}}, Request{}, Deny, LimitRuns, tenantScope, 0},
		"a custom warning threshold": {
			[]Applied{{pol(tenantScope, func(p *Policy) { p.MaxRuns = 10; p.WarnAt = 50 }), Usage{Runs: 4}}},
			Request{}, Warn, LimitRuns, tenantScope, 6,
		},
		"concurrency": {
			[]Applied{{pol(tenantScope, func(p *Policy) { p.MaxConcurrent = 2 }), Usage{Concurrent: 2}}},
			Request{}, Deny, LimitConcurrent, tenantScope, 0,
		},
		"spend": {
			[]Applied{{pol(tenantScope, func(p *Policy) { p.SpendLimit = 1000 }), Usage{Spend: 900}}},
			Request{Lease: 200}, Deny, LimitSpend, tenantScope, 100,
		},
		// A run that asks for no budget is still refused once the scope's
		// spend has reached its limit, to the micro-dollar.
		"spend reached, no lease asked": {
			[]Applied{{pol(tenantScope, func(p *Policy) { p.SpendLimit = 1000 }), Usage{Spend: 1000}}},
			Request{}, Deny, LimitSpend, tenantScope, 0,
		},
		"spend one micro under": {
			[]Applied{{pol(tenantScope, func(p *Policy) { p.SpendLimit = 1000 }), Usage{Spend: 999}}},
			Request{}, Warn, LimitSpend, tenantScope, 1,
		},
		"tokens reached": {
			[]Applied{{pol(tenantScope, func(p *Policy) { p.MaxTokens = 5000 }), Usage{Tokens: 5000}}},
			Request{}, Deny, LimitTokens, tenantScope, 0,
		},
		"a lease larger than any run may have": {
			[]Applied{{pol(tenantScope, func(p *Policy) { p.MaxLease = 100 }), Usage{}}},
			Request{Lease: 101}, Deny, LimitLease, tenantScope, 100,
		},
		"approval instead of refusal": {
			[]Applied{{pol(tenantScope, func(p *Policy) { p.MaxRuns = 1; p.Action = Approve }), Usage{Runs: 1}}},
			Request{}, NeedsApproval, LimitRuns, tenantScope, 0,
		},
		// A project with no policy of its own is held to the tenant's.
		"inherits the tenant's": {
			[]Applied{{pol(tenantScope, runs(5)), Usage{Runs: 5}}},
			Request{}, Deny, LimitRuns, tenantScope, 0,
		},
		// The narrower policy is stricter and decides.
		"the project tightens": {
			[]Applied{{pol(launch, runs(2)), Usage{Runs: 2}}, {pol(tenantScope, runs(100)), Usage{Runs: 2}}},
			Request{}, Deny, LimitRuns, launch, 0,
		},
		// The wider policy is exhausted, so a roomy narrower one cannot help.
		"the project cannot loosen": {
			[]Applied{{pol(launch, runs(50)), Usage{Runs: 1}}, {pol(tenantScope, runs(10)), Usage{Runs: 10}}},
			Request{}, Deny, LimitRuns, tenantScope, 0,
		},
		"a denial outranks a warning": {
			[]Applied{{pol(lead, runs(10)), Usage{Runs: 8}}, {pol(tenantScope, runs(10)), Usage{Runs: 10}}},
			Request{}, Deny, LimitRuns, tenantScope, 0,
		},
		"the nearest scope names a tie": {
			[]Applied{{pol(lead, runs(1)), Usage{Runs: 1}}, {pol(tenantScope, runs(1)), Usage{Runs: 1}}},
			Request{}, Deny, LimitRuns, lead, 0,
		},
	} {
		got := Decide(tc.applied, tc.req)
		if got.Verdict != tc.want || got.Limit != tc.limit || got.Remaining != tc.left {
			t.Errorf("%s: got %s on %q with %d left, want %s on %q with %d left", name, got.Verdict, got.Limit, got.Remaining, tc.want, tc.limit, tc.left)
			continue
		}
		if tc.want != Allow && (got.Policy == nil || got.Policy.Scope != tc.scope || got.Reason == "") {
			t.Errorf("%s: decided by %+v with reason %q, want %v", name, got.Policy, got.Reason, tc.scope)
		}
	}
}

func TestDecideIsPure(t *testing.T) {
	applied := []Applied{{pol(tenantScope, func(p *Policy) { p.MaxRuns = 3 }), Usage{Runs: 3}}}
	first := Decide(applied, Request{})
	for range 10 {
		again := Decide(applied, Request{})
		if again.Verdict != first.Verdict || again.Reason != first.Reason {
			t.Fatalf("%+v != %+v", again, first)
		}
	}
	if applied[0].Usage.Runs != 3 {
		t.Fatal("Decide changed the usage it was given")
	}
}

// Counts start again exactly at the boundary, not a moment either side of it.
func TestPeriodRollsOverAtTheBoundary(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for name, tc := range map[string]struct {
		p          Period
		now, start string
	}{
		"the last instant of a day":    {Day, "2026-10-09T23:59:59.999999999Z", "2026-10-09T00:00:00Z"},
		"the first instant of a day":   {Day, "2026-10-10T00:00:00Z", "2026-10-10T00:00:00Z"},
		"a day, from another zone":     {Day, "2026-10-09T22:30:00-05:00", "2026-10-10T00:00:00Z"},
		"the last instant of a month":  {Month, "2026-10-31T23:59:59.999999999Z", "2026-10-01T00:00:00Z"},
		"the first instant of a month": {Month, "2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z"},
		"february in a leap year":      {Month, "2028-02-29T12:00:00Z", "2028-02-01T00:00:00Z"},
	} {
		if got := PeriodStart(tc.p, at(tc.now)); !got.Equal(at(tc.start)) {
			t.Errorf("%s: started %s, want %s", name, got.Format(time.RFC3339), tc.start)
		}
	}
	if got := PeriodEnd(Month, at("2026-12-15T00:00:00Z")); !got.Equal(at("2027-01-01T00:00:00Z")) {
		t.Errorf("december ends %s", got)
	}
}

func TestValidate(t *testing.T) {
	ok := pol(tenantScope, func(p *Policy) { p.MaxRuns = 10 })
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Policy){
		"no limits":           func(p *Policy) { p.MaxRuns = 0 },
		"a negative limit":    func(p *Policy) { p.MaxConcurrent = -1 },
		"no period":           func(p *Policy) { p.Period = "" },
		"an unknown scope":    func(p *Policy) { p.Scope.Kind = "galaxy" },
		"a named tenant":      func(p *Policy) { p.Scope.Name = "acme" },
		"a nameless project":  func(p *Policy) { p.Scope = Scope{Kind: Project} },
		"a warning over 100":  func(p *Policy) { p.WarnAt = 150 },
		"approval, not built": func(p *Policy) { p.Action = Approve },
		"an unknown action":   func(p *Policy) { p.Action = "shrug" },
	} {
		p := ok
		mut(&p)
		if err := p.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func TestStoreKeepsNarrowerPoliciesWithinTheTenants(t *testing.T) {
	s := NewStore()
	if _, err := s.Put("acme", pol(tenantScope, func(p *Policy) { p.MaxRuns = 100; p.MaxConcurrent = 5 }), "ada", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("acme", pol(launch, func(p *Policy) { p.MaxRuns = 20 }), "ada", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("acme", pol(lead, func(p *Policy) { p.MaxRuns = 500 }), "ada", t0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an agent was allowed more than the tenant: %v", err)
	}
	// The tenant cannot be lowered underneath a project that would then claim more.
	if _, err := s.Put("acme", pol(tenantScope, func(p *Policy) { p.MaxRuns = 10 }), "ada", t0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the tenant was lowered below a project: %v", err)
	}
	// A limit the tenant does not set is the narrower scope's to choose.
	if _, err := s.Put("acme", pol(lead, func(p *Policy) { p.MaxLease = 5000 }), "ada", t0); err != nil {
		t.Fatal(err)
	}
	got := s.For("acme", "Launch", "lead", "")
	if len(got) != 3 || got[0].Scope != lead || got[1].Scope != launch || got[2].Scope != tenantScope {
		t.Fatalf("most specific first: %+v", got)
	}
	if len(s.For("acme", "", "helper", "")) != 1 {
		t.Fatal("a run in no project, by an agent with no policy, should fall under the tenant's alone")
	}
	if len(s.For("globex", "Launch", "lead", "")) != 0 || len(s.List("globex")) != 0 {
		t.Fatal("another tenant falls under these policies")
	}
}

func TestStoreVersionsAndRemembers(t *testing.T) {
	s := NewStore()
	first, _ := s.Put("acme", pol(launch, func(p *Policy) { p.MaxRuns = 10 }), "ada", t0)
	second, _ := s.Put("acme", pol(launch, func(p *Policy) { p.MaxRuns = 5 }), "bob", t0.Add(time.Hour))
	if first.Version != 1 || second.Version != 2 || second.UpdatedBy != "bob" {
		t.Fatalf("versions %d, %d by %s", first.Version, second.Version, second.UpdatedBy)
	}
	if err := s.Delete("acme", launch, "ada", t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("acme", launch, "ada", t0.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: %v", err)
	}
	// Set again, the count continues, so a version names one state for good.
	third, _ := s.Put("acme", pol(launch, func(p *Policy) { p.MaxRuns = 7 }), "ada", t0.Add(3*time.Hour))
	if third.Version != 3 {
		t.Fatalf("version after a delete = %d, want 3", third.Version)
	}

	h := s.History("acme")
	if len(h) != 4 || h[0].After.MaxRuns != 7 || h[1].After != nil || h[1].Before.MaxRuns != 5 || h[3].Before != nil {
		t.Fatalf("history = %+v", h)
	}
	if h[2].By != "bob" || h[2].Before.MaxRuns != 10 || h[2].After.MaxRuns != 5 {
		t.Fatalf("the change records who, before and after: %+v", h[2])
	}

	// What was the limit at a moment in the past.
	for when, want := range map[time.Duration]int{30 * time.Minute: 10, 90 * time.Minute: 5, 4 * time.Hour: 7} {
		got, ok := s.At("acme", launch, t0.Add(when))
		if !ok || got.MaxRuns != want {
			t.Errorf("at +%s: %d (found %v), want %d", when, got.MaxRuns, ok, want)
		}
	}
	if _, ok := s.At("acme", launch, t0.Add(150*time.Minute)); ok {
		t.Error("a policy was reported for a time when it had been deleted")
	}
	if _, ok := s.At("acme", launch, t0.Add(-time.Hour)); ok {
		t.Error("a policy was reported before it existed")
	}
}

// A client is a scope like a project or an agent: its policy applies to the
// runs it raised, comes after the agent's, and can only tighten the tenant's.
func TestAClientIsAScopeThatCanOnlyTighten(t *testing.T) {
	s := NewStore()
	docs := Scope{Kind: Client, Name: "docs-app"}
	if _, err := s.Put("acme", pol(tenantScope, func(p *Policy) { p.MaxConcurrent = 5 }), "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("acme", pol(docs, func(p *Policy) { p.MaxConcurrent = 9 }), "ada", time.Now()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a client's policy loosened the tenant's: %v", err)
	}
	if _, err := s.Put("acme", pol(docs, func(p *Policy) { p.MaxConcurrent = 2 }), "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("acme", pol(tenantScope, func(p *Policy) { p.MaxConcurrent = 1 }), "ada", time.Now()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the tenant was lowered below a client: %v", err)
	}
	if _, err := s.Put("acme", pol(lead, func(p *Policy) { p.MaxConcurrent = 1 }), "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	got := s.For("acme", "", "lead", "docs-app")
	if len(got) != 3 || got[0].Scope != lead || got[1].Scope != docs || got[2].Scope != tenantScope {
		t.Fatalf("most specific first: %+v", got)
	}
	// A run no client raised falls under no client's policy.
	if got := s.For("acme", "", "helper", ""); len(got) != 1 || got[0].Scope != tenantScope {
		t.Fatalf("a run with no client: %+v", got)
	}
	if got := s.For("acme", "", "helper", "ops-app"); len(got) != 1 {
		t.Fatalf("another client's run fell under docs-app's policy: %+v", got)
	}
	if list := s.List("acme"); list[len(list)-1].Scope != docs {
		t.Fatalf("clients sort last: %+v", list)
	}
}

// How many clients there may be is the tenant's to say and nobody else's.
func TestOnlyTheTenantLimitsHowManyClients(t *testing.T) {
	for name, p := range map[string]Policy{
		"a client":         pol(Scope{Kind: Client, Name: "docs"}, func(p *Policy) { p.MaxClients = 1 }),
		"an agent":         pol(lead, func(p *Policy) { p.MaxClientsPerSurface = 1 }),
		"past the ceiling": pol(tenantScope, func(p *Policy) { p.MaxClients = MaxClientsCeiling + 1 }),
		"negative":         pol(tenantScope, func(p *Policy) { p.MaxClients = -1 }),
		"client storage":   pol(Scope{Kind: Client, Name: "docs"}, func(p *Policy) { p.MaxStorage = 1 }),
		"client no name":   pol(Scope{Kind: Client}, func(p *Policy) { p.MaxRuns = 1 }),
	} {
		if err := p.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := pol(tenantScope, func(p *Policy) { p.MaxClients = 3 }).Validate(); err != nil {
		t.Fatalf("a tenant policy that only caps clients: %v", err)
	}
}

// Exceeded judges a run already in flight on spend and tokens alone: the
// count of runs that decided whether it could start does not stop it.
func TestExceededJudgesSpendAndTokensOnly(t *testing.T) {
	spend := func(max int64) func(*Policy) { return func(p *Policy) { p.SpendLimit = max; p.MaxRuns = 1 } }
	for name, tc := range map[string]struct {
		applied []Applied
		want    Verdict
		limit   Limit
	}{
		"nothing set":          {nil, Allow, ""},
		"runs at their limit":  {[]Applied{{pol(tenantScope, spend(1000)), Usage{Runs: 1, Spend: 10}}}, Allow, ""},
		"under the warning":    {[]Applied{{pol(tenantScope, spend(1000)), Usage{Spend: 799}}}, Allow, ""},
		"at the warning":       {[]Applied{{pol(tenantScope, spend(1000)), Usage{Spend: 800}}}, Warn, LimitSpend},
		"at the hard limit":    {[]Applied{{pol(tenantScope, spend(1000)), Usage{Spend: 1000}}}, Deny, LimitSpend},
		"past the hard limit":  {[]Applied{{pol(tenantScope, spend(1000)), Usage{Spend: 1700}}}, Deny, LimitSpend},
		"tokens at the limit":  {[]Applied{{pol(launch, func(p *Policy) { p.MaxTokens = 10 }), Usage{Tokens: 10}}}, Deny, LimitTokens},
		"a denial outranks":    {[]Applied{{pol(lead, spend(1000)), Usage{Spend: 900}}, {pol(tenantScope, spend(500)), Usage{Spend: 500}}}, Deny, LimitSpend},
		"unpriced is not cost": {[]Applied{{pol(tenantScope, spend(1000)), Usage{UnpricedCalls: 50, Tokens: 1 << 30}}}, Allow, ""},
	} {
		got := Exceeded(tc.applied)
		if got.Verdict != tc.want || got.Limit != tc.limit {
			t.Errorf("%s: %s on %q, want %s on %q (%s)", name, got.Verdict, got.Limit, tc.want, tc.limit, got.Reason)
		}
	}
}

// The warning threshold and the percentage in a reason hold for limits near
// the largest int64, where multiplying by 100 would wrap.
func TestThresholdsDoNotOverflow(t *testing.T) {
	const huge = int64(math.MaxInt64 - 7)
	if Reaches(huge/2, huge, 80) || !Reaches(huge/10*9, huge, 80) || !Reaches(huge, huge, 100) || Reaches(huge-1, huge, 100) {
		t.Fatal("Reaches is wrong near the largest int64")
	}
	if got := Percent(huge/10*9, huge); got != 89 && got != 90 {
		t.Fatalf("Percent = %d", got)
	}
	d := Exceeded([]Applied{{pol(tenantScope, func(p *Policy) { p.SpendLimit = huge }), Usage{Spend: huge / 10 * 9}}})
	if d.Verdict != Warn {
		t.Fatalf("90%% of a huge limit = %s", d.Verdict)
	}
	d = Decide([]Applied{{pol(tenantScope, func(p *Policy) { p.SpendLimit = huge }), Usage{Spend: huge}}}, Request{Lease: huge})
	if d.Verdict != Deny {
		t.Fatalf("a huge lease past a huge limit = %s", d.Verdict)
	}
	for used := range int64(200) {
		for _, max := range []int64{1, 7, 99, 100, 101, 1000} {
			if want := used*100 >= max*80; Reaches(used, max, 80) != want {
				t.Fatalf("Reaches(%d, %d, 80) != %v", used, max, want)
			}
		}
	}
}
