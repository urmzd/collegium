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
	"github.com/urmzd/open-agent-platform/pkg/auth"
	"github.com/urmzd/open-agent-platform/pkg/call"
	"github.com/urmzd/open-agent-platform/pkg/knowledge"
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
	mux.HandleFunc("GET /api/runs", s.authed(auth.RunsRead, s.listRuns))
	mux.HandleFunc("POST /api/runs", s.authed(auth.RunsWrite, s.createRun))
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
	s.observeRoutes(mux)
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
			"default-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
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
	ok(w, http.StatusOK, map[string]any{"principal": who, "permissions": who.Granted()})
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request, who auth.Principal) {
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
	ok(w, http.StatusOK, map[string]any{"agents": orEmpty(all)})
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var spec agentspec.AgentSpec
	if !decode(w, r, &spec) {
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
	rev, err := s.P.Agents.Revise(r.Context(), who.Tenant, body.Spec, body.ExpectedRev, who.Subject)
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, rev)
}

func (s *Server) listRuns(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	ok(w, http.StatusOK, map[string]any{"runs": orEmpty(s.P.Runs.List(who.Tenant, 200))})
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		Agent string `json:"agent"`
		Text  string `json:"text"`
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
	run, err := s.P.Invoke(r.Context(), envelope.Tag{
		ID:     id,
		Tenant: who.Tenant,
		Agent:  body.Agent,
		Origin: "api",
		Text:   body.Text,
		Actor:  envelope.Actor{ID: who.Subject, Display: who.Name, Bot: who.Kind == auth.Machine},
	})
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusAccepted, run)
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

	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
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
	switch {
	case errors.Is(err, runs.ErrNotFound), errors.Is(err, registry.ErrNotFound), errors.Is(err, platform.ErrScheduleNotFound),
		errors.Is(err, alerts.ErrNotFound), errors.Is(err, knowledge.ErrNotFound):
		fail(w, http.StatusNotFound, "not found")
	case errors.Is(err, registry.ErrExists), errors.Is(err, platform.ErrScheduleExists),
		errors.Is(err, alerts.ErrExists), errors.Is(err, knowledge.ErrExists):
		fail(w, http.StatusConflict, "already exists")
	case errors.Is(err, registry.ErrConflict):
		fail(w, http.StatusConflict, "the agent was revised by someone else, reload and try again")
	case errors.Is(err, platform.ErrForbidden), errors.Is(err, call.ErrDepth), errors.Is(err, call.ErrFanOut), errors.Is(err, call.ErrBudget):
		fail(w, http.StatusForbidden, err.Error())
	case errors.Is(err, call.ErrInvalid), errors.Is(err, registry.ErrInvalid), errors.Is(err, agentspec.ErrInvalid),
		errors.Is(err, envelope.ErrInvalid), errors.Is(err, cron.ErrSchedule),
		errors.Is(err, alerts.ErrInvalid), errors.Is(err, knowledge.ErrInvalid):
		fail(w, http.StatusBadRequest, err.Error())
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
