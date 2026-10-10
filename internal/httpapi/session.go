package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/urmzd/open-agent-platform/pkg/auth"
)

const (
	sessionTTL = 8 * time.Hour
	loginTTL   = 10 * time.Minute
)

// Authn authenticates requests. Exactly one of Dev or Verifier is set.
//
// A browser never holds a token. Sign-in runs the authorization code flow with
// PKCE on the server, and the browser gets an opaque, HttpOnly session cookie.
// Machines, and people using a CLI, send an access token as a bearer.
type Authn struct {
	// Dev, when set, is who every request is. It exists so the platform can
	// be previewed before an identity provider is configured, and the server
	// refuses to start with it on anything but a loopback address.
	Dev *auth.Principal

	Verifier *auth.Verifier
	OAuth    *oauth2.Config
	// Secure marks cookies Secure and gives them the __Host- prefix. It is
	// true whenever the public URL is https.
	Secure bool

	now func() time.Time

	mu       sync.Mutex
	sessions map[string]session
	logins   map[string]login
}

type session struct {
	who     auth.Principal
	expires time.Time
}

// login is one sign-in in flight, keyed by its state parameter.
type login struct {
	nonce    string
	verifier string
	returnTo string
	expires  time.Time
}

// NewAuthn returns an authenticator for a verified identity provider.
// publicURL is where browsers reach this server, and scopes are what sign-in
// asks the provider for.
func NewAuthn(v *auth.Verifier, clientID, clientSecret, publicURL string, scopes []string) (*Authn, error) {
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("httpapi: public url %q is not a url", publicURL)
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"
	if u.Scheme != "https" && !loopback {
		return nil, fmt.Errorf("httpapi: public url %q must be https, or sessions would cross the network in the clear", publicURL)
	}
	if clientSecret == "" {
		return nil, errors.New("httpapi: sign-in needs a client secret")
	}
	return &Authn{
		Verifier: v,
		OAuth: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     v.Provider().Endpoint(),
			RedirectURL:  strings.TrimSuffix(publicURL, "/") + "/auth/callback",
			Scopes:       scopes,
		},
		Secure: u.Scheme == "https",
	}, nil
}

func (a *Authn) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *Authn) cookieName(base string) string {
	if a.Secure {
		return "__Host-" + base
	}
	return base
}

func (a *Authn) setCookie(w http.ResponseWriter, base, value string, ttl time.Duration) {
	c := &http.Cookie{
		Name:     a.cookieName(base),
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	}
	if ttl <= 0 {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("httpapi: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Authenticate identifies the caller, or returns auth.ErrUnauthenticated.
func (a *Authn) Authenticate(r *http.Request) (auth.Principal, error) {
	if a.Dev != nil {
		return *a.Dev, nil
	}
	if h := r.Header.Get("Authorization"); h != "" {
		raw, ok := strings.CutPrefix(h, "Bearer ")
		if !ok {
			return auth.Principal{}, fmt.Errorf("%w: unsupported authorization scheme", auth.ErrUnauthenticated)
		}
		return a.Verifier.Bearer(r.Context(), raw)
	}
	c, err := r.Cookie(a.cookieName("oap_session"))
	if err != nil {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[c.Value]
	if !ok || a.clock().After(s.expires) {
		delete(a.sessions, c.Value)
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return s.who, nil
}

// handleLogin starts a sign-in and redirects to the identity provider.
func (a *Authn) handleLogin(w http.ResponseWriter, r *http.Request) {
	if a.Dev != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	state, nonce, verifier := token(), token(), oauth2.GenerateVerifier()
	a.mu.Lock()
	if a.logins == nil {
		a.logins = map[string]login{}
	}
	now := a.clock()
	for k, l := range a.logins {
		if now.After(l.expires) {
			delete(a.logins, k)
		}
	}
	a.logins[state] = login{
		nonce: nonce, verifier: verifier,
		returnTo: safeReturn(r.URL.Query().Get("return_to")),
		expires:  now.Add(loginTTL),
	}
	a.mu.Unlock()

	// The state also goes in a cookie, so the callback can tell that the
	// browser finishing the flow is the browser that started it.
	a.setCookie(w, "oap_login", state, loginTTL)
	http.Redirect(w, r, a.OAuth.AuthCodeURL(state,
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.S256ChallengeOption(verifier),
	), http.StatusFound)
}

// handleCallback finishes a sign-in.
func (a *Authn) handleCallback(w http.ResponseWriter, r *http.Request) {
	if a.Dev != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	c, err := r.Cookie(a.cookieName("oap_login"))
	a.setCookie(w, "oap_login", "", 0)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		http.Error(w, "sign-in was not started from this browser", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	l, ok := a.logins[state]
	delete(a.logins, state)
	a.mu.Unlock()
	if !ok || a.clock().After(l.expires) {
		http.Error(w, "sign-in expired, start again", http.StatusBadRequest)
		return
	}
	if q.Get("error") != "" {
		// The provider's description is not echoed: it is attacker-reachable
		// through a crafted link.
		http.Error(w, "the identity provider refused the sign-in", http.StatusForbidden)
		return
	}

	who, err := a.exchange(r.Context(), q.Get("code"), l)
	if err != nil {
		http.Error(w, "sign-in could not be verified", http.StatusForbidden)
		return
	}
	id := token()
	a.mu.Lock()
	if a.sessions == nil {
		a.sessions = map[string]session{}
	}
	a.sessions[id] = session{who: who, expires: a.clock().Add(sessionTTL)}
	a.mu.Unlock()
	a.setCookie(w, "oap_session", id, sessionTTL)
	http.Redirect(w, r, l.returnTo, http.StatusFound)
}

func (a *Authn) exchange(ctx context.Context, code string, l login) (auth.Principal, error) {
	tok, err := a.OAuth.Exchange(ctx, code, oauth2.VerifierOption(l.verifier))
	if err != nil {
		return auth.Principal{}, err
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return auth.Principal{}, errors.New("httpapi: provider returned no id token")
	}
	return a.Verifier.IDToken(ctx, raw, l.nonce)
}

// handleLogout ends the session.
func (a *Authn) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(a.cookieName("oap_session")); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	a.setCookie(w, "oap_session", "", 0)
	w.WriteHeader(http.StatusNoContent)
}

// safeReturn keeps a post-sign-in redirect on this site. Anything that is not a
// plain local path becomes "/", so sign-in cannot be used to bounce a person to
// another origin.
func safeReturn(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		return "/"
	}
	return p
}
