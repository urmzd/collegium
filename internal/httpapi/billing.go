package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/urmzd/open-agent-platform/internal/platform"
	"github.com/urmzd/open-agent-platform/pkg/auth"
	"github.com/urmzd/open-agent-platform/pkg/policy"
)

// Billing policies are a workspace's limits. Anyone in the workspace can read
// them and what has been used against them. Only an admin can change them.
func (s *Server) billingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/policies", s.authed(auth.BillingRead, s.listPolicies))
	mux.HandleFunc("PUT /api/policies", s.authed(auth.BillingWrite, s.putPolicy))
	mux.HandleFunc("DELETE /api/policies", s.authed(auth.BillingWrite, s.deletePolicy))
	mux.HandleFunc("GET /api/policies/history", s.authed(auth.BillingRead, s.policyHistory))
	mux.HandleFunc("GET /api/usage", s.authed(auth.BillingRead, s.usage))
	mux.HandleFunc("GET /api/usage/notices", s.authed(auth.BillingRead, s.usageNotices))
}

func (s *Server) listPolicies(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	ok(w, http.StatusOK, map[string]any{"policies": s.P.Policies.List(who.Tenant)})
}

// exists checks that a scope names something in the caller's tenant.
func (s *Server) exists(r *http.Request, who auth.Principal, scope policy.Scope) error {
	switch scope.Kind {
	case policy.Project:
		if !s.P.HasProject(who.Tenant, scope.Name) {
			return platform.ErrProjectNotFound
		}
	case policy.Agent:
		_, err := s.P.Agents.Get(r.Context(), who.Tenant, scope.Name, 0)
		return err
	}
	return nil
}

func (s *Server) putPolicy(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		Scope         policy.Scope  `json:"scope"`
		Period        policy.Period `json:"period"`
		MaxRuns       int           `json:"max_runs"`
		MaxConcurrent int           `json:"max_concurrent"`
		SpendLimit    int64         `json:"spend_limit"`
		MaxLease      int64         `json:"max_lease"`
		MaxStorage    int64         `json:"max_storage"`
		WarnAt        int           `json:"warn_at"`
		Action        policy.Action `json:"action"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.exists(r, who, body.Scope); err != nil {
		s.problem(w, err)
		return
	}
	if body.Action == "" {
		body.Action = policy.Block
	}
	saved, err := s.P.Policies.Put(who.Tenant, policy.Policy{
		Scope: body.Scope, Period: body.Period, MaxRuns: body.MaxRuns, MaxConcurrent: body.MaxConcurrent,
		SpendLimit: body.SpendLimit, MaxLease: body.MaxLease, MaxStorage: body.MaxStorage, WarnAt: body.WarnAt, Action: body.Action,
	}, who.Subject, time.Now())
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, saved)
}

func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	q := r.URL.Query()
	scope := policy.Scope{Kind: policy.Kind(q.Get("kind")), Name: q.Get("name")}
	if err := s.P.Policies.Delete(who.Tenant, scope, who.Subject, time.Now()); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) policyHistory(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	ok(w, http.StatusOK, map[string]any{"changes": s.P.Policies.History(who.Tenant)})
}

// measure is one limit and what has been used against it. A limit of zero
// means the scope sets none.
type measure struct {
	Used  int64 `json:"used"`
	Limit int64 `json:"limit"`
	// Projected is where Used ends the period at the pace so far. It is left
	// out for a count that does not accumulate, such as runs in flight.
	Projected *int64 `json:"projected,omitempty"`
}

// scopeUsage is one policy and where its scope stands against it.
type scopeUsage struct {
	Scope       policy.Scope  `json:"scope"`
	Period      policy.Period `json:"period"`
	PeriodStart time.Time     `json:"period_start"`
	PeriodEnd   time.Time     `json:"period_end"`
	Version     int           `json:"version"`
	WarnAt      int           `json:"warn_at"`
	Action      policy.Action `json:"action"`
	Runs        measure       `json:"runs"`
	Concurrent  measure       `json:"concurrent"`
	// Spend is recorded and not enforced: the runtime reports no cost.
	Spend struct {
		measure
		Metered bool `json:"metered"`
	} `json:"spend"`
	MaxLease int64 `json:"max_lease"`
	// State is the worst of the enforced limits: ok, warn or over.
	State string `json:"state"`
}

// project estimates where a count ends the period at the pace so far.
func project(used int64, start, end, now time.Time) *int64 {
	elapsed := now.Sub(start)
	if elapsed <= 0 {
		return nil
	}
	p := int64(float64(used) * float64(end.Sub(start)) / float64(elapsed))
	return &p
}

func (s *Server) usage(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	now := time.Now()
	out := []scopeUsage{}
	for _, pol := range s.P.Policies.List(who.Tenant) {
		start, end := policy.PeriodStart(pol.Period, now), policy.PeriodEnd(pol.Period, now)
		u := s.P.Usage.Usage(who.Tenant, pol.Scope, start)
		warn := pol.WarnAt
		if warn == 0 {
			warn = policy.DefaultWarnAt
		}
		v := scopeUsage{
			Scope: pol.Scope, Period: pol.Period, PeriodStart: start, PeriodEnd: end,
			Version: pol.Version, WarnAt: warn, Action: pol.Action, MaxLease: pol.MaxLease,
			Runs:       measure{Used: int64(u.Runs), Limit: int64(pol.MaxRuns), Projected: project(int64(u.Runs), start, end, now)},
			Concurrent: measure{Used: int64(u.Concurrent), Limit: int64(pol.MaxConcurrent)},
			State:      "ok",
		}
		v.Spend.measure = measure{Used: u.Spend, Limit: pol.SpendLimit}
		for _, m := range []measure{v.Runs, v.Concurrent} {
			switch {
			case m.Limit > 0 && m.Used >= m.Limit:
				v.State = "over"
			case m.Limit > 0 && m.Used*100 >= m.Limit*int64(warn) && v.State == "ok":
				v.State = "warn"
			}
		}
		out = append(out, v)
	}
	ok(w, http.StatusOK, map[string]any{"scopes": out})
}

func (s *Server) usageNotices(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	ok(w, http.StatusOK, map[string]any{"notices": orEmpty(s.P.Usage.Notices(who.Tenant, 200))})
}

// denied answers a run a policy refused. A count limit is "too many, try
// later" and a money limit is "payment required". Either way the body names
// the policy, its version, the limit and what was left, so a caller can show
// exactly why.
func denied(w http.ResponseWriter, err error) bool {
	var d *platform.Denied
	if !errors.As(err, &d) {
		return false
	}
	status := http.StatusTooManyRequests
	if d.Decision.Limit == policy.LimitSpend || d.Decision.Limit == policy.LimitLease {
		status = http.StatusPaymentRequired
	}
	body := map[string]any{
		"error": d.Decision.Reason, "limit": d.Decision.Limit, "remaining": d.Decision.Remaining,
	}
	if p := d.Decision.Policy; p != nil {
		body["policy"] = map[string]any{"scope": p.Scope, "version": p.Version, "period": p.Period}
		if d.Decision.Limit == policy.LimitRuns {
			// When the count starts again.
			body["resets_at"] = policy.PeriodEnd(p.Period, time.Now())
		}
	}
	ok(w, status, body)
	return true
}
