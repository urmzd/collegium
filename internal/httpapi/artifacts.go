package httpapi

import (
	"mime"
	"net/http"
	"slices"
	"strconv"

	"github.com/urmzd/open-agent-platform/pkg/artifacts"
	"github.com/urmzd/open-agent-platform/pkg/auth"
	"github.com/urmzd/open-agent-platform/pkg/policy"
)

func (s *Server) artifactRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/artifacts", s.authed(auth.ArtifactsRead, s.listArtifacts))
	mux.HandleFunc("GET /api/artifacts/{id}", s.authed(auth.ArtifactsRead, s.getArtifact))
	mux.HandleFunc("GET /api/artifacts/{id}/versions", s.authed(auth.ArtifactsRead, s.artifactVersions))
	mux.HandleFunc("GET /api/artifacts/{id}/content", s.authed(auth.ArtifactsRead, s.artifactContent))
	mux.HandleFunc("DELETE /api/artifacts/{id}", s.authed(auth.ArtifactsWrite, s.deleteArtifact))
	mux.HandleFunc("GET /api/storage", s.authed(auth.ArtifactsRead, s.storage))
}

// listArtifacts pages a tenant's published files, newest first, the latest
// version of each. project, agent, run, thread, kind, q, limit and before are
// optional. q matches part of a title.
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	q := r.URL.Query()
	query := artifacts.Query{
		Project: q.Get("project"), Agent: q.Get("agent"), Run: q.Get("run"), Thread: q.Get("thread"),
		Kind: artifacts.Kind(q.Get("kind")), Before: q.Get("before"), Title: q.Get("q"),
	}
	if query.Kind != "" && !slices.Contains(artifacts.Kinds(), query.Kind) {
		fail(w, http.StatusBadRequest, "kind is not one this server knows")
		return
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			fail(w, http.StatusBadRequest, "limit is a number above zero")
			return
		}
		query.Limit = n
	}
	page, next, err := s.P.Artifacts.Page(who.Tenant, query)
	if err != nil {
		fail(w, http.StatusBadRequest, "before is not a cursor this server issued")
		return
	}
	var cursor *string
	if next != "" {
		cursor = &next
	}
	ok(w, http.StatusOK, map[string]any{"artifacts": page, "next": cursor})
}

func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	a, err := s.P.Artifacts.Get(who.Tenant, r.PathValue("id"))
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, a)
}

func (s *Server) artifactVersions(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	versions, err := s.P.Artifacts.Versions(who.Tenant, r.PathValue("id"))
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]any{"versions": versions})
}

// inlineTypes are the only types ever shown in the browser from this origin.
// Each is inert: none can carry script. HTML and SVG are not here, and never
// will be, because a file an agent wrote from a stranger's text would then run
// as this application.
var inlineTypes = []string{"text/plain", "image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"}

// artifactContent serves an artifact's bytes. The caller's tenant and right
// are checked on every request, by the route and by the lookup: there is no
// link that works without them.
func (s *Server) artifactContent(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	a, body, err := s.P.OpenArtifact(r.Context(), who, r.PathValue("id"))
	if err != nil {
		s.problem(w, err)
		return
	}
	h := w.Header()
	disposition, served := "attachment", "application/octet-stream"
	if r.URL.Query().Get("inline") == "1" && slices.Contains(inlineTypes, a.Mime) {
		disposition, served = "inline", a.Mime
		if a.Mime == "text/plain" {
			served = "text/plain; charset=utf-8"
		}
	}
	h.Set("Content-Type", served)
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": a.Title}))
	// The browser must take the type as given, and whatever it does with the
	// bytes it does with no script, no network and no access to this origin.
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", "private, no-store")
	h.Set("ETag", `"`+a.SHA256+`"`)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) deleteArtifact(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	if err := s.P.DeleteArtifact(r.Context(), who, r.PathValue("id")); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// storage reports what a tenant has published and what it is allowed.
func (s *Server) storage(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	u := s.P.Artifacts.Usage(who.Tenant)
	var limit *int64
	for _, pol := range s.P.Policies.List(who.Tenant) {
		if pol.Scope.Kind == policy.Tenant && pol.MaxStorage > 0 {
			limit = &pol.MaxStorage
		}
	}
	ok(w, http.StatusOK, map[string]any{
		"used_bytes": u.Bytes, "count": u.Count, "limit_bytes": limit,
		"by_project": orEmpty(u.ByProject), "by_agent": orEmpty(u.ByAgent),
	})
}
