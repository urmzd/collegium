package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/urmzd/open-agent-platform/pkg/alerts"
	"github.com/urmzd/open-agent-platform/pkg/auth"
	"github.com/urmzd/open-agent-platform/pkg/evals"
	"github.com/urmzd/open-agent-platform/pkg/journal"
)

// maxWindow bounds how far back analytics and the journal may be asked for.
const maxWindow = 30 * 24 * time.Hour

func (s *Server) observeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/analytics", s.authed(auth.RunsRead, s.analytics))
	mux.HandleFunc("GET /api/journal", s.authed(auth.RunsRead, s.journal))
	mux.HandleFunc("GET /api/runs/{id}/trace", s.authed(auth.RunsRead, s.trace))

	mux.HandleFunc("GET /api/alerts", s.authed(auth.AlertsRead, s.listAlerts))
	mux.HandleFunc("POST /api/alerts/{id}/ack", s.authed(auth.AlertsWrite, s.ackAlert))
	mux.HandleFunc("GET /api/alert-rules", s.authed(auth.AlertsRead, s.listRules))
	mux.HandleFunc("POST /api/alert-rules", s.authed(auth.AlertsWrite, s.createRule))
	mux.HandleFunc("DELETE /api/alert-rules/{name}", s.authed(auth.AlertsWrite, s.deleteRule))

	mux.HandleFunc("GET /api/evals", s.authed(auth.EvalsRead, s.listSuites))
	mux.HandleFunc("POST /api/evals", s.authed(auth.EvalsWrite, s.putSuite(false)))
	mux.HandleFunc("PUT /api/evals/{name}", s.authed(auth.EvalsWrite, s.putSuite(true)))
	mux.HandleFunc("DELETE /api/evals/{name}", s.authed(auth.EvalsWrite, s.deleteSuite))
	mux.HandleFunc("POST /api/evals/{name}/run", s.authed(auth.EvalsWrite, s.runSuite))
	mux.HandleFunc("GET /api/evals/{name}/runs", s.authed(auth.EvalsRead, s.suiteRuns))

	mux.HandleFunc("GET /api/knowledge", s.authed(auth.KnowledgeRead, s.listSources))
	mux.HandleFunc("POST /api/knowledge", s.authed(auth.KnowledgeWrite, s.createSource))
	mux.HandleFunc("GET /api/knowledge/search", s.authed(auth.KnowledgeRead, s.searchKnowledge))
	mux.HandleFunc("DELETE /api/knowledge/{name}", s.authed(auth.KnowledgeWrite, s.deleteSource))
	mux.HandleFunc("GET /api/knowledge/{name}/documents", s.authed(auth.KnowledgeRead, s.listDocuments))
	mux.HandleFunc("POST /api/knowledge/{name}/documents", s.authed(auth.KnowledgeWrite, s.addDocument))
	mux.HandleFunc("DELETE /api/knowledge/{name}/documents/{id}", s.authed(auth.KnowledgeWrite, s.removeDocument))
}

// window reads ?window= as a duration ending now. It defaults to an hour.
func window(r *http.Request) (since, until time.Time, ok bool) {
	d := time.Hour
	if raw := r.URL.Query().Get("window"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 || parsed > maxWindow {
			return since, until, false
		}
		d = parsed
	}
	until = time.Now()
	return until.Add(-d), until, true
}

func (s *Server) analytics(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	since, until, valid := window(r)
	if !valid {
		fail(w, http.StatusBadRequest, "window is a duration up to 720h, such as 1h or 24h")
		return
	}
	bucket := until.Sub(since) / 60
	if raw := r.URL.Query().Get("bucket"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			fail(w, http.StatusBadRequest, "bucket is a duration, such as 1m")
			return
		}
		bucket = parsed
	}
	ok(w, http.StatusOK, s.P.Runs.Summarize(who.Tenant, since, until.Add(time.Nanosecond), bucket))
}

// journal answers "what happened": events in the caller's tenant, narrowed by
// agent, run, kind, origin, text and time, newest first.
func (s *Server) journal(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	q := r.URL.Query()
	since, until, valid := window(r)
	if !valid {
		fail(w, http.StatusBadRequest, "window is a duration up to 720h, such as 1h or 24h")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	entries := s.P.Journal.Query(journal.Query{
		Tenant: who.Tenant, Agent: q.Get("agent"), RunID: q.Get("run"), Kind: q.Get("kind"),
		Origin: q.Get("origin"), Contains: q.Get("q"), Since: since, Until: until, Limit: limit,
	})
	ok(w, http.StatusOK, map[string]any{"entries": orEmpty(entries)})
}

// trace answers "what happened in this run": the run, every run under the same
// root, and the spans inside each.
func (s *Server) trace(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	tree, err := s.P.Runs.Tree(who.Tenant, r.PathValue("id"))
	if err != nil {
		s.problem(w, err)
		return
	}
	ids := make([]string, len(tree))
	for i, run := range tree {
		ids[i] = run.ID
	}
	ok(w, http.StatusOK, map[string]any{"runs": tree, "spans": orEmpty(s.P.Journal.Trace(who.Tenant, ids))})
}

func (s *Server) listAlerts(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	ok(w, http.StatusOK, map[string]any{"alerts": orEmpty(s.P.Alerts.Alerts(who.Tenant))})
}

func (s *Server) ackAlert(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	a, err := s.P.Alerts.Ack(who.Tenant, r.PathValue("id"), who.Subject, time.Now())
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, a)
}

func (s *Server) listRules(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	ok(w, http.StatusOK, map[string]any{"rules": orEmpty(s.P.Alerts.Rules(who.Tenant))})
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var rule alerts.Rule
	if !decode(w, r, &rule) {
		return
	}
	rule.CreatedBy = who.Subject
	if rule.Agent != "" {
		if _, err := s.P.Agents.Get(r.Context(), who.Tenant, rule.Agent, 0); err != nil {
			s.problem(w, err)
			return
		}
	}
	if err := s.P.Alerts.AddRule(who.Tenant, rule); err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusCreated, rule)
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	if err := s.P.Alerts.RemoveRule(who.Tenant, r.PathValue("name")); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listSources(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	ok(w, http.StatusOK, map[string]any{"sources": s.P.Knowledge.List(who.Tenant)})
}

func (s *Server) createSource(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decode(w, r, &body) {
		return
	}
	src, err := s.P.Knowledge.Create(who.Tenant, body.Name, body.Description, who.Subject, time.Now())
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusCreated, src)
}

func (s *Server) deleteSource(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	if err := s.P.Knowledge.Delete(who.Tenant, r.PathValue("name")); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	docs, err := s.P.Knowledge.Documents(who.Tenant, r.PathValue("name"))
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]any{"documents": docs})
}

func (s *Server) addDocument(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		Title string `json:"title"`
		URI   string `json:"uri"`
		Text  string `json:"text"`
	}
	if !decode(w, r, &body) {
		return
	}
	doc, err := s.P.Knowledge.Add(who.Tenant, r.PathValue("name"), body.Title, body.URI, body.Text, who.Subject, time.Now())
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusCreated, doc)
}

func (s *Server) removeDocument(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	if err := s.P.Knowledge.Remove(who.Tenant, r.PathValue("name"), r.PathValue("id"), time.Now()); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) searchKnowledge(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	q := r.URL.Query()
	var names []string
	if raw := q.Get("source"); raw != "" {
		names = strings.Split(raw, ",")
	}
	hits, err := s.P.Knowledge.Search(who.Tenant, names, q.Get("q"), 0)
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]any{"hits": hits})
}

// suiteView is a suite with its most recent runs, newest first. Results are
// left off all but the latest, so listing suites stays small.
type suiteView struct {
	evals.Suite
	Runs []evals.Run `json:"runs"`
}

func (s *Server) listSuites(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	out := []suiteView{}
	for _, suite := range s.P.Evals.List(who.Tenant) {
		runs, _ := s.P.Evals.Runs(who.Tenant, suite.Name)
		if len(runs) > 20 {
			runs = runs[:20]
		}
		for i := range runs {
			if i > 0 {
				runs[i].Results = []evals.Result{}
			}
		}
		out = append(out, suiteView{Suite: suite, Runs: orEmpty(runs)})
	}
	ok(w, http.StatusOK, map[string]any{"suites": out})
}

func (s *Server) putSuite(replace bool) handler {
	return func(w http.ResponseWriter, r *http.Request, who auth.Principal) {
		var suite evals.Suite
		if !decode(w, r, &suite) {
			return
		}
		if replace && suite.Name != r.PathValue("name") {
			fail(w, http.StatusBadRequest, "the suite's name must match the suite being changed")
			return
		}
		// What a suite tests must exist in the caller's tenant now.
		var err error
		switch suite.Target {
		case evals.Agent:
			_, err = s.P.Agents.Get(r.Context(), who.Tenant, suite.Subject, 0)
		case evals.Knowledge:
			_, err = s.P.Knowledge.Documents(who.Tenant, suite.Subject)
		}
		if err != nil {
			s.problem(w, err)
			return
		}
		suite.CreatedBy, suite.UpdatedAt = who.Subject, time.Now()
		if err := s.P.Evals.Put(who.Tenant, suite, replace); err != nil {
			s.problem(w, err)
			return
		}
		status := http.StatusCreated
		if replace {
			status = http.StatusOK
		}
		ok(w, status, suite)
	}
}

func (s *Server) deleteSuite(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	if err := s.P.Evals.Delete(who.Tenant, r.PathValue("name")); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runSuite(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	run, err := s.P.StartEval(r.Context(), who.Tenant, r.PathValue("name"), who.Subject)
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusAccepted, run)
}

func (s *Server) suiteRuns(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	runs, err := s.P.Evals.Runs(who.Tenant, r.PathValue("name"))
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]any{"runs": orEmpty(runs)})
}
