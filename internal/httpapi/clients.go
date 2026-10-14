package httpapi

import (
	"net/http"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/clients"
	"github.com/urmzd/collegium/pkg/policy"
)

// A client is one Slack app or GitHub installation the workspace answers
// through. Anyone in the workspace can see which exist. Only an admin can
// create or change one, because that hands the workspace's agents a
// credential. A secret is written and never read back: no response carries one.
func (s *Server) clientRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/clients", s.authed(auth.ClientsRead, s.listClients))
	mux.HandleFunc("POST /api/clients", s.authed(auth.ClientsWrite, s.createClient))
	mux.HandleFunc("GET /api/clients/{id}", s.authed(auth.ClientsRead, s.getClient))
	mux.HandleFunc("PUT /api/clients/{id}", s.authed(auth.ClientsWrite, s.updateClient))
	mux.HandleFunc("DELETE /api/clients/{id}", s.authed(auth.ClientsWrite, s.deleteClient))
}

// clientBody is a client as a caller writes it. It has no field for the
// tenant, for who manages the client, or for its version: those are the
// server's to say.
type clientBody struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Surface      clients.Surface        `json:"surface"`
	Identity     clients.Identity       `json:"identity"`
	DefaultAgent string                 `json:"default_agent"`
	Agents       []string               `json:"agents"`
	Bots         map[string]string      `json:"bots"`
	Slack        *clients.SlackOptions  `json:"slack"`
	GitHub       *clients.GitHubOptions `json:"github"`
	// Enabled defaults to true on create.
	Enabled     *bool `json:"enabled"`
	Credentials struct {
		Token         clients.SecretInput `json:"token"`
		SigningSecret clients.SecretInput `json:"signing_secret"`
	} `json:"credentials"`
}

func (b clientBody) input() platform.ClientInput {
	return platform.ClientInput{
		Client: clients.Client{
			ID: b.ID, Name: b.Name, Surface: b.Surface, Identity: b.Identity,
			DefaultAgent: b.DefaultAgent, Agents: b.Agents, Bots: b.Bots, Slack: b.Slack, GitHub: b.GitHub,
			Enabled: b.Enabled == nil || *b.Enabled,
		},
		Token: b.Credentials.Token, SigningSecret: b.Credentials.SigningSecret,
	}
}

// clientView is a client and the limits on the runs it raises.
type clientView struct {
	clients.Client
	// Limits is the client's own policy, if it has one. It is set through
	// /api/policies with a scope of kind "client".
	Limits *policy.Policy `json:"limits,omitempty"`
}

func (s *Server) view(tenant string, c clients.Client) clientView {
	v := clientView{Client: c}
	for _, pol := range s.P.Policies.For(tenant, "", "", c.ID) {
		if pol.Scope.Kind == policy.Client {
			v.Limits = &pol
		}
	}
	return v
}

func (s *Server) listClients(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	// The store returns one tenant's clients and never more than its ceiling.
	all := s.P.Clients.List(who.Tenant)
	out := make([]clientView, 0, len(all))
	for _, c := range all {
		out = append(out, s.view(who.Tenant, c))
	}
	ok(w, http.StatusOK, map[string]any{"clients": out, "caps": s.P.ClientCaps(who.Tenant)})
}

func (s *Server) getClient(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	c, err := s.P.Clients.Get(who.Tenant, r.PathValue("id"))
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, s.view(who.Tenant, c))
}

func (s *Server) createClient(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body clientBody
	if !decode(w, r, &body) {
		return
	}
	c, err := s.P.CreateClient(r.Context(), who.Tenant, who.Subject, body.input())
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusCreated, s.view(who.Tenant, c))
}

func (s *Server) updateClient(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body clientBody
	if !decode(w, r, &body) {
		return
	}
	c, err := s.P.UpdateClient(r.Context(), who.Tenant, r.PathValue("id"), who.Subject, body.input())
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, s.view(who.Tenant, c))
}

func (s *Server) deleteClient(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	if err := s.P.DeleteClient(r.Context(), who.Tenant, r.PathValue("id"), who.Subject); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
