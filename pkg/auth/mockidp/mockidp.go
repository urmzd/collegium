// Package mockidp is an OpenID Connect identity provider for development and
// tests. It runs in process, signs real tokens with a key made at start, and
// lets anyone who can reach it sign in as any persona it was configured with.
// There are no passwords.
//
// Because of that last part it refuses to run anywhere but loopback: Start
// fails unless Config.Enabled is set, unless the listen address is 127.0.0.1
// or localhost, and unless every redirect URI is a loopback URL. Its issuer is
// a loopback http URL, which auth.Verifier accepts only for loopback, so a
// verifier configured with it cannot be pointed anywhere else. It logs a
// warning at start, and its sign-in page says what it is.
//
// The tokens it issues go through auth.Verifier like any provider's: the
// sign-in flow, the nonce, PKCE and the keys are all exercised, not bypassed.
package mockidp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Development credentials for the web client. They are not secrets: anyone
// can read them here, which is fine only because the provider is loopback.
const (
	DevClientID     = "collegium-dev"
	DevClientSecret = "mock-client-secret"
)

// ErrDisabled is returned by Start when Config.Enabled is false.
var ErrDisabled = errors.New("mockidp: not enabled; it lets anyone sign in as anyone, so it must be switched on explicitly")

// Person is someone who can sign in.
type Person struct {
	// Subject is the stable identifier, the "sub" claim.
	Subject string `json:"subject"`
	Name    string `json:"name"`
	Email   string `json:"email,omitempty"`
	// Groups go in the groups claim.
	Groups []string `json:"groups,omitempty"`
	// Claims are added to the tokens. They cannot replace a claim the
	// provider sets itself, such as "sub" or "aud".
	Claims map[string]any `json:"claims,omitempty"`
	// Tenant goes in the tenant claim. Empty means Config.Tenant.
	Tenant string `json:"tenant,omitempty"`
}

// Machine is a client that gets tokens with client credentials.
type Machine struct {
	ClientID string `json:"client_id"`
	Secret   string `json:"secret"`
	// Scopes are the scopes it may be issued, written in full, for example
	// "collegium:runs:read".
	Scopes []string `json:"scopes"`
	// Tenant goes in the tenant claim. Empty means Config.Tenant.
	Tenant string `json:"tenant,omitempty"`
}

// Config is the provider's setup.
type Config struct {
	// Enabled must be true. It exists so that a zero Config, or one built
	// by mistake, starts nothing.
	Enabled bool
	// Addr is where to listen: "127.0.0.1:<port>" or "localhost:<port>".
	// Port 0 picks a free one.
	Addr string
	// ClientID and ClientSecret are the web client that signs people in.
	ClientID     string
	ClientSecret string
	// RedirectURIs are where the web client may receive a code. Each must
	// be a loopback URL, and a request must name one exactly.
	RedirectURIs []string
	// Audience is the audience of access tokens, the verifier's Audience.
	Audience string
	// Tenant is the tenant claim for a persona that names none.
	Tenant string
	// TenantClaim and GroupsClaim name those claims. Empty means "tenant"
	// and "groups".
	TenantClaim string
	GroupsClaim string
	People      []Person
	Machines    []Machine
	// TokenTTL is how long a token lasts. Zero means an hour.
	TokenTTL time.Duration
	// Now is the clock tokens are dated by. Nil means time.Now. Tests set
	// it to issue tokens that have already expired.
	Now func() time.Time
	// Log receives the startup warning. Nil means slog.Default().
	Log *slog.Logger
}

// DefaultPeople exercise the built-in policy with the default group names:
// a viewer, an operator, an admin, and someone in no mapped group.
func DefaultPeople() []Person {
	return []Person{
		{Subject: "mock-viewer", Name: "Vera Viewer", Email: "vera@example.test", Groups: []string{"collegium-viewers"}},
		{Subject: "mock-operator", Name: "Otto Operator", Email: "otto@example.test", Groups: []string{"collegium-operators"}},
		{Subject: "mock-admin", Name: "Ada Admin", Email: "ada@example.test", Groups: []string{"collegium-admins"}},
		{Subject: "mock-nobody", Name: "Nell Nobody", Email: "nell@example.test", Groups: []string{"everyone"}},
	}
}

// DefaultMachines is one automation client that may read and start runs.
func DefaultMachines() []Machine {
	return []Machine{{
		ClientID: "mock-ci", Secret: "mock-ci-secret",
		Scopes: []string{"collegium:runs:read", "collegium:runs:write"},
	}}
}

// reserved are the claims the provider sets. A persona cannot override them.
var reserved = map[string]bool{
	"iss": true, "sub": true, "aud": true, "exp": true, "iat": true, "nbf": true,
	"jti": true, "nonce": true, "azp": true, "cid": true, "scp": true, "scope": true,
	"email_verified": true,
}

// Server is a running provider.
type Server struct {
	cfg    Config
	issuer string
	signer jose.Signer
	keys   jose.JSONWebKeySet
	ln     net.Listener
	srv    *http.Server

	mu    sync.Mutex
	codes map[string]grant
}

// grant is an authorization code waiting to be exchanged.
type grant struct {
	person      Person
	redirectURI string
	nonce       string
	challenge   string
	expires     time.Time
}

const codeTTL = time.Minute

// Start validates the configuration, makes a signing key and listens. It
// refuses anything that could expose it beyond this machine.
func Start(cfg Config) (*Server, error) {
	if !cfg.Enabled {
		return nil, ErrDisabled
	}
	if err := cfg.fill(); err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("mockidp: address %q: %w", cfg.Addr, err)
	}
	if host != "127.0.0.1" && host != "localhost" {
		return nil, fmt.Errorf("mockidp: refusing to listen on %q: only 127.0.0.1 or localhost, because anyone who reaches it can sign in as anyone", cfg.Addr)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("mockidp: key: %w", err)
	}
	kid := randomToken()[:16]
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid),
	)
	if err != nil {
		return nil, fmt.Errorf("mockidp: signer: %w", err)
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("mockidp: listen: %w", err)
	}
	// "localhost" is a name, and a name can resolve to anything. What was
	// actually bound is what counts.
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() {
		_ = ln.Close()
		return nil, fmt.Errorf("mockidp: refusing %s: it is not a loopback address", ln.Addr())
	}
	s := &Server{
		cfg:    cfg,
		issuer: fmt.Sprintf("http://%s:%d", host, tcp.Port),
		signer: signer,
		keys: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &key.PublicKey, KeyID: kid, Algorithm: string(jose.RS256), Use: "sig",
		}}},
		ln:    ln,
		codes: map[string]grant{},
	}
	s.srv = &http.Server{Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	cfg.Log.Warn("MOCK IDENTITY PROVIDER IS RUNNING. Anyone who can reach it can sign in as any persona without a password. It is for development and tests only; never use it in a real deployment.",
		"issuer", s.issuer, "people", len(cfg.People), "machines", len(cfg.Machines))
	return s, nil
}

func (c *Config) fill() error {
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.TokenTTL <= 0 {
		c.TokenTTL = time.Hour
	}
	if c.TenantClaim == "" {
		c.TenantClaim = "tenant"
	}
	if c.GroupsClaim == "" {
		c.GroupsClaim = "groups"
	}
	switch {
	case c.ClientID == "" || c.ClientSecret == "":
		return errors.New("mockidp: needs a client id and a client secret")
	case c.Audience == "":
		return errors.New("mockidp: needs an audience")
	case len(c.RedirectURIs) == 0:
		return errors.New("mockidp: needs at least one redirect uri")
	case len(c.People) == 0 && len(c.Machines) == 0:
		return errors.New("mockidp: needs at least one person or machine")
	}
	if reserved[c.TenantClaim] || reserved[c.GroupsClaim] {
		return errors.New("mockidp: the tenant and groups claims cannot be ones the provider sets")
	}
	for _, r := range c.RedirectURIs {
		u, err := url.Parse(r)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" || !loopbackHost(u.Hostname()) {
			return fmt.Errorf("mockidp: redirect uri %q must be a loopback http url, or a code could leave this machine", r)
		}
	}
	seen := map[string]bool{}
	for _, p := range c.People {
		if p.Subject == "" || seen[p.Subject] {
			return fmt.Errorf("mockidp: person %q needs a subject of their own", p.Name)
		}
		seen[p.Subject] = true
		for k := range p.Claims {
			if reserved[k] || k == c.TenantClaim || k == c.GroupsClaim || k == "name" || k == "email" {
				return fmt.Errorf("mockidp: person %q sets claim %q, which comes from their own fields or from the provider", p.Subject, k)
			}
		}
	}
	clients := map[string]bool{c.ClientID: true}
	for _, m := range c.Machines {
		if m.ClientID == "" || m.Secret == "" || clients[m.ClientID] {
			return fmt.Errorf("mockidp: machine %q needs a client id of its own and a secret", m.ClientID)
		}
		clients[m.ClientID] = true
	}
	return nil
}

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Issuer is the provider's issuer URL, for auth.OIDCConfig.Issuer.
func (s *Server) Issuer() string { return s.issuer }

// Close stops the provider.
func (s *Server) Close() error { return s.srv.Close() }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.keys) })
	mux.HandleFunc("GET /authorize", s.authorizePage)
	mux.HandleFunc("POST /authorize", s.authorize)
	mux.HandleFunc("POST /token", s.token)
	return mux
}

func (s *Server) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.issuer,
		"authorization_endpoint":                s.issuer + "/authorize",
		"token_endpoint":                        s.issuer + "/token",
		"jwks_uri":                              s.issuer + "/keys",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "client_credentials"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"scopes_supported":                      []string{"openid", "profile", "email", s.cfg.GroupsClaim},
	})
}

// request is a checked authorization request.
type request struct {
	clientID, redirectURI, state, nonce, challenge, scope string
}

func (s *Server) checkRequest(v url.Values) (request, error) {
	r := request{
		clientID: v.Get("client_id"), redirectURI: v.Get("redirect_uri"), state: v.Get("state"),
		nonce: v.Get("nonce"), challenge: v.Get("code_challenge"), scope: v.Get("scope"),
	}
	switch {
	case r.clientID != s.cfg.ClientID:
		return r, errors.New("unknown client_id")
	case !slices.Contains(s.cfg.RedirectURIs, r.redirectURI):
		return r, errors.New("redirect_uri is not registered")
	case v.Get("response_type") != "code":
		return r, errors.New(`response_type must be "code"`)
	case !slices.Contains(strings.Fields(r.scope), "openid"):
		return r, errors.New(`scope must include "openid"`)
	case r.state == "":
		return r, errors.New("state is required")
	case r.nonce == "":
		return r, errors.New("nonce is required")
	case r.challenge == "" || v.Get("code_challenge_method") != "S256":
		return r, errors.New("PKCE with S256 is required")
	}
	return r, nil
}

func (s *Server) authorizePage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.checkRequest(r.URL.Query()); err != nil {
		s.page(w, http.StatusBadRequest, pageData{Error: err.Error()})
		return
	}
	hidden := map[string]string{}
	for _, k := range []string{"client_id", "redirect_uri", "response_type", "scope", "state", "nonce", "code_challenge", "code_challenge_method"} {
		hidden[k] = r.URL.Query().Get(k)
	}
	s.page(w, http.StatusOK, pageData{People: s.cfg.People, Machines: s.cfg.Machines, Hidden: hidden, Issuer: s.issuer})
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		s.page(w, http.StatusBadRequest, pageData{Error: "the form could not be read"})
		return
	}
	req, err := s.checkRequest(r.PostForm)
	if err != nil {
		s.page(w, http.StatusBadRequest, pageData{Error: err.Error()})
		return
	}
	i := slices.IndexFunc(s.cfg.People, func(p Person) bool { return p.Subject == r.PostForm.Get("persona") })
	if i < 0 {
		s.page(w, http.StatusBadRequest, pageData{Error: "pick one of the personas listed"})
		return
	}
	code := randomToken()
	now := s.cfg.Now()
	s.mu.Lock()
	for k, g := range s.codes {
		if now.After(g.expires) {
			delete(s.codes, k)
		}
	}
	s.codes[code] = grant{
		person: s.cfg.People[i], redirectURI: req.redirectURI, nonce: req.nonce,
		challenge: req.challenge, expires: now.Add(codeTTL),
	}
	s.mu.Unlock()

	u, _ := url.Parse(req.redirectURI)
	q := u.Query()
	q.Set("code", code)
	q.Set("state", req.state)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	id, secret, ok := r.BasicAuth()
	if ok {
		// RFC 6749 section 2.3.1: both are form-encoded inside the header.
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		if !s.webClient(id, secret) {
			oauthError(w, http.StatusUnauthorized, "invalid_client")
			return
		}
		s.exchange(w, r)
	case "client_credentials":
		m, ok := s.machine(id, secret)
		if !ok {
			oauthError(w, http.StatusUnauthorized, "invalid_client")
			return
		}
		s.clientCredentials(w, r, m)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type")
	}
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func (s *Server) webClient(id, secret string) bool {
	return equal(id, s.cfg.ClientID) && equal(secret, s.cfg.ClientSecret)
}

func (s *Server) machine(id, secret string) (Machine, bool) {
	for _, m := range s.cfg.Machines {
		if equal(id, m.ClientID) && equal(secret, m.Secret) {
			return m, true
		}
	}
	return Machine{}, false
}

func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	code := r.PostForm.Get("code")
	s.mu.Lock()
	g, ok := s.codes[code]
	// A code is used once, whether or not the exchange succeeds.
	delete(s.codes, code)
	s.mu.Unlock()
	if !ok || s.cfg.Now().After(g.expires) || r.PostForm.Get("redirect_uri") != g.redirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !equal(base64.RawURLEncoding.EncodeToString(sum[:]), g.challenge) {
		oauthError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	p := g.person
	idClaims := s.personClaims(p, s.cfg.ClientID)
	idClaims["nonce"] = g.nonce
	idToken, err := s.sign(idClaims)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error")
		return
	}
	access, err := s.sign(s.personClaims(p, s.cfg.Audience))
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "id_token": idToken,
		"expires_in": int(s.cfg.TokenTTL.Seconds()),
	})
}

func (s *Server) clientCredentials(w http.ResponseWriter, r *http.Request, m Machine) {
	scopes := m.Scopes
	if asked := strings.Fields(r.PostForm.Get("scope")); len(asked) > 0 {
		for _, sc := range asked {
			if !slices.Contains(m.Scopes, sc) {
				oauthError(w, http.StatusBadRequest, "invalid_scope")
				return
			}
		}
		scopes = asked
	}
	claims := s.base(m.ClientID, s.cfg.Audience)
	claims["cid"] = m.ClientID
	claims["scp"] = scopes
	claims[s.cfg.TenantClaim] = s.tenant(m.Tenant)
	access, err := s.sign(claims)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer",
		"expires_in": int(s.cfg.TokenTTL.Seconds()), "scope": strings.Join(scopes, " "),
	})
}

func (s *Server) tenant(t string) string {
	if t == "" {
		return s.cfg.Tenant
	}
	return t
}

func (s *Server) base(subject, aud string) map[string]any {
	now := s.cfg.Now()
	return map[string]any{
		"iss": s.issuer, "sub": subject, "aud": aud,
		"iat": now.Unix(), "exp": now.Add(s.cfg.TokenTTL).Unix(), "jti": randomToken()[:22],
	}
}

func (s *Server) personClaims(p Person, aud string) map[string]any {
	claims := s.base(p.Subject, aud)
	for k, v := range p.Claims {
		claims[k] = v
	}
	claims["name"] = p.Name
	if p.Email != "" {
		claims["email"] = p.Email
		claims["email_verified"] = true
	}
	groups := p.Groups
	if groups == nil {
		// A groups claim, even empty, marks an access token as a person's
		// rather than a machine's.
		groups = []string{}
	}
	claims[s.cfg.GroupsClaim] = groups
	if t := s.tenant(p.Tenant); t != "" {
		claims[s.cfg.TenantClaim] = t
	}
	return claims
}

func (s *Server) sign(claims map[string]any) (string, error) {
	return jwt.Signed(s.signer).Claims(claims).Serialize()
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("mockidp: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
