package httpapi

import (
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"

	"github.com/urmzd/collegium/pkg/auth"
)

// tailscaleAuth trusts Serve on this host. Local processes must be trusted:
// loopback prevents remote callers from forging identity headers, but does
// not authenticate the local process making the request.
type tailscaleAuth struct {
	tenant string
	host   string
	policy *auth.Policy
}

// NewTailscaleAuthn authenticates through Tailscale Serve identity headers.
// addr must listen on loopback and publicURL must be the HTTPS Serve origin.
func NewTailscaleAuthn(addr, publicURL, tenant string, policy *auth.Policy) (*Authn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || (host != "localhost" && !net.ParseIP(host).IsLoopback()) {
		return nil, fmt.Errorf("-auth=tailscale requires a loopback listener, not %q", addr)
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("-auth=tailscale requires an HTTPS Serve origin as -public-url")
	}
	if tenant == "" {
		return nil, fmt.Errorf("-auth=tailscale requires a tenant")
	}
	return &Authn{Secure: true, tailscale: &tailscaleAuth{tenant: tenant, host: u.Host, policy: policy}}, nil
}

func (t *tailscaleAuth) authenticate(r *http.Request) (auth.Principal, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() || r.Host != t.host || r.Header.Get("Authorization") != "" {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	login, err := identityHeader(r, "Tailscale-User-Login")
	if err != nil {
		return auth.Principal{}, err
	}
	name, err := identityHeader(r, "Tailscale-User-Name")
	if err != nil {
		return auth.Principal{}, err
	}
	return auth.TailscalePrincipal(t.tenant, login, name, t.policy)
}

func identityHeader(r *http.Request, key string) (string, error) {
	values := r.Header.Values(key)
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", auth.ErrUnauthenticated
	}
	s, err := new(mime.WordDecoder).DecodeHeader(values[0])
	if err != nil {
		return "", auth.ErrUnauthenticated
	}
	return s, nil
}

func (a *Authn) tailscaleLogin(w http.ResponseWriter, r *http.Request) {
	if _, err := a.Authenticate(r); err != nil {
		http.Error(w, "open Collegium through its Tailscale Serve URL from a user-owned tailnet device", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, safeReturn(r.URL.Query().Get("return_to")), http.StatusFound)
}
