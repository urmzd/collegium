// Package httpapi is the platform's HTTP edge: sign-in, the JSON API, the event
// stream, and the web console's files.
//
// Every /api route authenticates, then checks one permission, then confines
// what it reads and writes to the caller's tenant. The tenant always comes from
// the Principal and never from the request.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/urmzd/opentag/pkg/agentspec"
	"github.com/urmzd/opentag/pkg/bus"
	"github.com/urmzd/opentag/pkg/connectors/cron"
	"github.com/urmzd/opentag/pkg/envelope"
	"github.com/urmzd/opentag/pkg/registry"
	"github.com/urmzd/opentag/pkg/topic"

	"github.com/urmzd/open-agent-platform/internal/platform"
	"github.com/urmzd/open-agent-platform/pkg/alerts"
	"github.com/urmzd/open-agent-platform/pkg/artifacts"
	"github.com/urmzd/open-agent-platform/pkg/auth"
	"github.com/urmzd/open-agent-platform/pkg/call"
	"github.com/urmzd/open-agent-platform/pkg/evals"
	"github.com/urmzd/open-agent-platform/pkg/inbox"
	"github.com/urmzd/open-agent-platform/pkg/knowledge"
	"github.com/urmzd/open-agent-platform/pkg/memory"
	"github.com/urmzd/open-agent-platform/pkg/pick"
	"github.com/urmzd/open-agent-platform/pkg/policy"
	"github.com/urmzd/open-agent-platform/pkg/prefs"
	"github.com/urmzd/open-agent-platform/pkg/runs"
)

// maxBody bounds every request body. Nothing this API accepts is large.
const maxBody = 1 << 20

// heartbeat keeps an idle event stream from being closed by a proxy.
const heartbeat = 15 * time.Second

// Server serves the platform over HTTP.
type Server struct {
	P     *platform.Platform
	Authn *Authn
	Log   *slog.Logger
	// WebDir, when set, is a built web console to serve at /.
	WebDir string
}

// Handler returns the server's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })

	mux.HandleFunc("GET /auth/login", s.Authn.handleLogin)
	mux.HandleFunc("GET /auth/callback", s.Authn.handleCallback)
	mux.HandleFunc("POST /auth/logout", s.Authn.handleLogout)

	mux.HandleFunc("GET /api/me", s.authed("", s.me))
	mux.HandleFunc("GET /api/agents", s.authed(auth.AgentsRead, s.listAgents))
	mux.HandleFunc("POST /api/agents", s.authed(auth.AgentsWrite, s.createAgent))
	mux.HandleFunc("PUT /api/agents/{name}", s.authed(auth.AgentsWrite, s.reviseAgent))
	mux.HandleFunc("GET /api/agents/{name}/history", s.authed(auth.AgentsRead, s.agentHistory))
	mux.HandleFunc("GET /api/agents/{name}/memory", s.authed(auth.AgentsRead, s.listMemory))
	mux.HandleFunc("POST /api/agents/{name}/memory", s.authed(auth.AgentsWrite, s.addMemory))
	mux.HandleFunc("DELETE /api/agents/{name}/memory/{id}", s.authed(auth.AgentsWrite, s.removeMemory))
	mux.HandleFunc("POST /api/agents/{name}/memory/approve", s.authed(auth.AgentsWrite, s.approveMemory))
	mux.HandleFunc("GET /api/memory/pending", s.authed(auth.AgentsRead, s.pendingMemory))
	mux.HandleFunc("GET /api/runs", s.authed(auth.RunsRead, s.listRuns))
	mux.HandleFunc("POST /api/runs", s.authed(auth.RunsWrite, s.createRun))
	mux.HandleFunc("GET /api/threads", s.authed(auth.RunsRead, s.listThreads))
	mux.HandleFunc("GET /api/threads/{id}", s.authed(auth.RunsRead, s.getThread))
	mux.HandleFunc("GET /api/pick", s.authed(auth.AgentsRead, s.pickAgent))
	mux.HandleFunc("GET /api/settings", s.authed(auth.AgentsRead, s.getSettings))
	mux.HandleFunc("PUT /api/settings", s.authed(auth.AgentsWrite, s.putSettings))
	mux.HandleFunc("GET /api/runs/{id}", s.authed(auth.RunsRead, s.getRun))
	mux.HandleFunc("GET /api/runs/{id}/tree", s.authed(auth.RunsRead, s.runTree))
	mux.HandleFunc("POST /api/runs/{id}/calls", s.authed(auth.RunsWrite, s.createCall))
	mux.HandleFunc("GET /api/schedules", s.authed(auth.SchedulesRead, s.listSchedules))
	mux.HandleFunc("POST /api/schedules", s.authed(auth.SchedulesWrite, s.createSchedule))
	mux.HandleFunc("DELETE /api/schedules/{name}", s.authed(auth.SchedulesWrite, s.deleteSchedule))
	mux.HandleFunc("POST /api/schedules/{name}/pause", s.authed(auth.SchedulesWrite, s.pauseSchedule(true)))
	mux.HandleFunc("POST /api/schedules/{name}/resume", s.authed(auth.SchedulesWrite, s.pauseSchedule(false)))
	mux.HandleFunc("POST /api/schedules/{name}/run", s.authed(auth.SchedulesWrite, s.runSchedule))
	mux.HandleFunc("GET /api/events", s.authed(auth.RunsRead, s.events))
	s.inboxRoutes(mux)
	s.artifactRoutes(mux)
	s.observeRoutes(mux)
	s.preferenceRoutes(mux)
	s.projectRoutes(mux)
	s.billingRoutes(mux)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { fail(w, http.StatusNotFound, "no such route") })

	if s.WebDir != "" {
		mux.Handle("/", spa(s.WebDir))
	}

	// A cookie authenticates a browser without the page's consent, so a
	// state-changing request from another origin is refused before it reaches
	// a handler. Bearer callers are not browsers and are unaffected.
	return headers(http.NewCrossOriginProtection().Handler(mux))
}

// headers sets the response headers every route should carry.
func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob: https:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

type handler func(w http.ResponseWriter, r *http.Request, who auth.Principal)

// authed authenticates, authorizes, bounds the body, and records every change.
// An empty permission means any signed-in caller.
func (s *Server) authed(perm auth.Permission, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		who, err := s.Authn.Authenticate(r)
		if err != nil {
			fail(w, http.StatusUnauthorized, "sign in to continue")
			return
		}
		if perm != "" && !who.Can(perm) {
			s.Log.Warn("httpapi: denied", "subject", who.Subject, "tenant", who.Tenant, "permission", perm, "path", r.URL.Path)
			fail(w, http.StatusForbidden, fmt.Sprintf("you need %s", perm))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if r.Method != http.MethodGet {
			s.Log.Info("httpapi: change", "subject", who.Subject, "kind", who.Kind, "tenant", who.Tenant,
				"method", r.Method, "path", r.URL.Path)
		}
		h(w, r.WithContext(auth.With(r.Context(), who)), who)
	}
}

func (s *Server) me(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	// first_admin says the caller is the one who should set the workspace
	// up: an admin, in a workspace nobody has set up yet.
	first := who.Can(auth.AgentsWrite) && s.P.Settings(who.Tenant).SetupAt.IsZero()
	ok(w, http.StatusOK, map[string]any{"principal": who, "permissions": who.Granted(), "first_admin": first})
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	if err := s.P.EnsureBuiltins(r.Context(), who.Tenant); err != nil {
		s.Log.Warn("httpapi: built-in agents", "error", err)
	}
	var all []agentspec.Revision
	page := registry.Page{Size: 100}
	for {
		revs, next, err := s.P.Agents.List(r.Context(), who.Tenant, page)
		if err != nil {
			s.problem(w, err)
			return
		}
		all = append(all, revs...)
		if next == "" {
			break
		}
		page.Token = next
	}
	// builtin names the agents the platform provides, which cannot be edited.
	ok(w, http.StatusOK, map[string]any{"agents": orEmpty(all), "builtin": platform.Builtins()})
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var spec agentspec.AgentSpec
	if !decode(w, r, &spec) {
		return
	}
	if platform.IsBuiltin(spec.Name) {
		fail(w, http.StatusForbidden, "that name belongs to an agent the platform provides")
		return
	}
	rev, err := s.P.Agents.Create(r.Context(), who.Tenant, spec, who.Subject)
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusCreated, rev)
}

func (s *Server) reviseAgent(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		Spec        agentspec.AgentSpec `json:"spec"`
		ExpectedRev int                 `json:"expected_rev"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Spec.Name != r.PathValue("name") {
		fail(w, http.StatusBadRequest, "the spec's name must match the agent being revised")
		return
	}
	if platform.IsBuiltin(body.Spec.Name) {
		fail(w, http.StatusForbidden, "this agent is provided by the platform and cannot be edited")
		return
	}
	rev, err := s.P.Agents.Revise(r.Context(), who.Tenant, body.Spec, body.ExpectedRev, who.Subject)
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, rev)
}

// agentHistory returns every revision of an agent, newest first.
func (s *Server) agentHistory(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	revs, err := s.P.Agents.History(r.Context(), who.Tenant, r.PathValue("name"))
	if err != nil {
		s.problem(w, err)
		return
	}
	slices.SortFunc(revs, func(a, b agentspec.Revision) int { return b.Rev - a.Rev })
	ok(w, http.StatusOK, map[string]any{"revisions": orEmpty(revs)})
}

func (s *Server) listMemory(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	if _, err := s.P.Agents.Get(r.Context(), who.Tenant, r.PathValue("name"), 0); err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]any{"notes": orEmpty(s.P.Memory.List(who.Tenant, r.PathValue("name")))})
}

func (s *Server) addMemory(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &body) {
		return
	}
	if _, err := s.P.Agents.Get(r.Context(), who.Tenant, r.PathValue("name"), 0); err != nil {
		s.problem(w, err)
		return
	}
	// A person writing a note is the approval.
	note, err := s.P.Memory.Add(who.Tenant, r.PathValue("name"), body.Text, memory.Person, memory.Approved, who.Subject, time.Now())
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusCreated, note)
}

// approveMemory keeps the proposals a person selected. The rest stay pending
// until they are approved or discarded.
func (s *Server) approveMemory(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &body) {
		return
	}
	if _, err := s.P.Agents.Get(r.Context(), who.Tenant, r.PathValue("name"), 0); err != nil {
		s.problem(w, err)
		return
	}
	n, err := s.P.ApproveMemory(who.Tenant, r.PathValue("name"), body.IDs)
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]int{"approved": n})
}

// pendingMemory lists every proposal waiting in the tenant. A proposal is for
// an agent or for a project, and says which.
func (s *Server) pendingMemory(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	type proposal struct {
		Agent   string      `json:"agent,omitempty"`
		Project string      `json:"project,omitempty"`
		Note    memory.Note `json:"note"`
	}
	out := []proposal{}
	for _, p := range s.P.Memory.Pending(who.Tenant) {
		if project, isProject := memory.OwnerProject(p.Agent); isProject {
			out = append(out, proposal{Project: project, Note: p.Note})
		} else {
			out = append(out, proposal{Agent: p.Agent, Note: p.Note})
		}
	}
	ok(w, http.StatusOK, map[string]any{"proposals": out})
}

func (s *Server) removeMemory(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	if err := s.P.RemoveMemory(who.Tenant, r.PathValue("name"), r.PathValue("id")); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listRuns(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	ok(w, http.StatusOK, map[string]any{"runs": orEmpty(s.P.Runs.List(who.Tenant, 200))})
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		// Agent is who should answer. Empty or "auto" lets the platform pick.
		Agent string `json:"agent"`
		Text  string `json:"text"`
		// After is the turn this message follows. Empty starts a conversation.
		After string `json:"after"`
		// Project files a new conversation under a project.
		Project string `json:"project"`
	}
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		fail(w, http.StatusBadRequest, "a run needs a request")
		return
	}
	// A caller that retries sends the same key and joins the run it started.
	id := "api-" + token()[:22]
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		id = "api-" + who.Subject + "-" + key
	}
	run, err := s.P.Converse(r.Context(), who.Tenant, platform.Turn{
		ID: id, Agent: body.Agent, Text: body.Text, After: body.After, Project: body.Project,
		Actor: envelope.Actor{ID: who.Subject, Display: who.Name, Bot: who.Kind == auth.Machine},
	})
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusAccepted, run)
}

// listThreads pages a tenant's conversations, newest activity first.
//
// limit, before, origin, agent and standing are all optional. The answer
// carries next, the cursor for the page after this one, or null at the end.
func (s *Server) listThreads(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	q := r.URL.Query()
	query := runs.ThreadQuery{
		Origin: q.Get("origin"), Agent: q.Get("agent"), Before: q.Get("before"),
		Standing: runs.Standing(q.Get("standing")),
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			fail(w, http.StatusBadRequest, "limit is a number above zero")
			return
		}
		query.Limit = n
	}
	switch query.Standing {
	case "", runs.StandingRunning, runs.StandingFailed:
	default:
		fail(w, http.StatusBadRequest, "standing is running or failed")
		return
	}
	page, next, err := s.P.Runs.ThreadPage(who.Tenant, query)
	if err != nil {
		fail(w, http.StatusBadRequest, "before is not a cursor this server issued")
		return
	}
	var cursor *string
	if next != "" {
		cursor = &next
	}
	ok(w, http.StatusOK, map[string]any{"threads": page, "next": cursor})
}

func (s *Server) getThread(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	turns, err := s.P.Runs.Conversation(who.Tenant, r.PathValue("id"))
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]any{"turns": turns})
}

// pickAgent previews which agent would answer a message, and why.
func (s *Server) pickAgent(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	choice, err := s.P.Pick(r.Context(), who.Tenant, r.URL.Query().Get("q"))
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, choice)
}

func (s *Server) getSettings(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	ok(w, http.StatusOK, s.P.Settings(who.Tenant))
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var next platform.Settings
	if !decode(w, r, &next) {
		return
	}
	before := s.P.Settings(who.Tenant)
	if err := s.P.SetSettings(r.Context(), who.Tenant, next); err != nil {
		s.problem(w, err)
		return
	}
	// The first admin to save settings is on record as having set the
	// workspace up.
	saved := s.P.MarkSetup(who.Tenant, who.Subject)
	s.Log.Info("httpapi: settings changed", "subject", who.Subject, "tenant", who.Tenant,
		"workspace_before", before.WorkspaceName, "workspace_after", saved.WorkspaceName,
		"baseline_before", before.Baseline, "baseline_after", saved.Baseline, "first_setup", before.SetupAt.IsZero())
	ok(w, http.StatusOK, saved)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	run, err := s.P.Runs.Get(who.Tenant, r.PathValue("id"))
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, run)
}

func (s *Server) runTree(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	tree, err := s.P.Runs.Tree(who.Tenant, r.PathValue("id"))
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]any{"runs": tree})
}

func (s *Server) createCall(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		Key   string    `json:"key"`
		Agent string    `json:"agent"`
		Text  string    `json:"text"`
		Mode  call.Mode `json:"mode"`
		Lease int64     `json:"lease"`
	}
	if !decode(w, r, &body) {
		return
	}
	run, err := s.P.Call(r.Context(), who.Tenant, r.PathValue("id"), call.Request(body))
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusAccepted, run)
}

// events streams the caller's tenant's events as server-sent events.
//
// topic narrows it: "agent" is everything, "agent:<name>" one agent,
// "agent:<name>:<run>" one run. from resumes after a sequence number.
func (s *Server) events(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	t := topic.All()
	if raw := r.URL.Query().Get("topic"); raw != "" {
		parsed, err := topic.Parse(raw)
		if err != nil {
			fail(w, http.StatusBadRequest, "topic is not valid")
			return
		}
		t = parsed
	}
	sub := envelope.Subscription{Topic: t}
	if raw := r.URL.Query().Get("from"); raw != "" {
		if _, err := fmt.Sscan(raw, &sub.From); err != nil {
			fail(w, http.StatusBadRequest, "from is not a sequence number")
			return
		}
	}
	stream, err := s.P.Subscribe(r.Context(), who.Tenant, sub)
	if err != nil {
		s.problem(w, err)
		return
	}
	defer func() { _ = stream.Close() }()

	rc := http.NewResponseController(w)
	// A stream outlives the server's write timeout by design.
	_ = rc.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()

	out := make(chan envelope.Event)
	go func() {
		defer close(out)
		for {
			e, err := stream.Recv(r.Context())
			if err != nil {
				if !errors.Is(err, bus.ErrClosed) && r.Context().Err() == nil {
					s.Log.Warn("httpapi: event stream ended", "subject", who.Subject, "error", err)
				}
				return
			}
			select {
			case out <- e:
			case <-r.Context().Done():
				return
			}
		}
	}()

	// Inbox changes ride the same stream as a named event, so a reader that
	// only listens for run events never sees them. A notice names a message
	// and nothing more: the reader fetches it and is shown what they may see.
	notices, unwatch := s.P.Inbox.Watch(who.Tenant)
	defer unwatch()

	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case n := <-notices:
			if !platform.InboxSees(who, n.Kind) {
				continue
			}
			b, err := json.Marshal(n)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: inbox\ndata: %s\n\n", b); err != nil {
				return
			}
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
		case e, open := <-out:
			if !open {
				// The client reconnects and resumes. A consumer that fell
				// behind is ended here rather than fed a gap.
				return
			}
			b, err := json.Marshal(e)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return
			}
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

// problem maps a domain error to a response. Anything unrecognized is logged
// and reported without detail, so an internal error never leaks through.
func (s *Server) problem(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if denied(w, err) {
		return
	}
	switch {
	case errors.Is(err, runs.ErrNotFound), errors.Is(err, registry.ErrNotFound), errors.Is(err, platform.ErrScheduleNotFound),
		errors.Is(err, platform.ErrProjectNotFound),
		errors.Is(err, alerts.ErrNotFound), errors.Is(err, knowledge.ErrNotFound), errors.Is(err, evals.ErrNotFound),
		errors.Is(err, memory.ErrNotFound), errors.Is(err, policy.ErrNotFound), errors.Is(err, inbox.ErrNotFound),
		errors.Is(err, artifacts.ErrNotFound):
		fail(w, http.StatusNotFound, "not found")
	case errors.Is(err, registry.ErrExists), errors.Is(err, platform.ErrScheduleExists), errors.Is(err, platform.ErrProjectExists),
		errors.Is(err, alerts.ErrExists), errors.Is(err, knowledge.ErrExists), errors.Is(err, evals.ErrExists):
		fail(w, http.StatusConflict, "already exists")
	case errors.Is(err, pick.ErrNoAgent):
		fail(w, http.StatusBadRequest, "no agent matched this message and no baseline agent is set, so choose an agent")
	case errors.Is(err, platform.ErrSettings):
		fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, platform.ErrNoBaseline):
		fail(w, http.StatusBadRequest, "that setting names an agent that does not exist")
	case errors.Is(err, evals.ErrBusy):
		fail(w, http.StatusConflict, "that suite is already running")
	case errors.Is(err, registry.ErrConflict):
		fail(w, http.StatusConflict, "the agent was revised by someone else, reload and try again")
	case errors.Is(err, platform.ErrForbidden), errors.Is(err, call.ErrDepth), errors.Is(err, call.ErrFanOut), errors.Is(err, call.ErrBudget):
		fail(w, http.StatusForbidden, err.Error())
	case errors.Is(err, call.ErrInvalid), errors.Is(err, registry.ErrInvalid), errors.Is(err, agentspec.ErrInvalid),
		errors.Is(err, envelope.ErrInvalid), errors.Is(err, cron.ErrSchedule),
		errors.Is(err, alerts.ErrInvalid), errors.Is(err, knowledge.ErrInvalid), errors.Is(err, evals.ErrInvalid),
		errors.Is(err, prefs.ErrInvalid), errors.Is(err, memory.ErrInvalid), errors.Is(err, policy.ErrInvalid),
		errors.Is(err, inbox.ErrInvalid), errors.Is(err, platform.ErrAction):
		fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, platform.ErrSandbox), errors.Is(err, artifacts.ErrInvalid):
		fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, memory.ErrSensitive):
		fail(w, http.StatusBadRequest, "that note carries personal data, such as an email address or a card number, so it was not stored")
	case errors.As(err, &tooBig):
		fail(w, http.StatusRequestEntityTooLarge, "request is too large")
	default:
		s.Log.Error("httpapi: internal error", "error", err)
		fail(w, http.StatusInternalServerError, "something went wrong on our side")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, http.StatusRequestEntityTooLarge, "request is too large")
		} else {
			fail(w, http.StatusBadRequest, "request body is not the expected JSON")
		}
		return false
	}
	return true
}

func ok(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	ok(w, status, map[string]string{"error": msg})
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// spa serves a built single-page app, falling back to index.html for routes the
// app handles itself.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := filepath.Join(dir, filepath.FromSlash(filepath.Clean("/"+r.URL.Path)))
		if info, err := os.Stat(p); err != nil || info.IsDir() {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}
