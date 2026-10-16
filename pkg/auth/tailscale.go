package auth

import (
	"strings"

	"github.com/urmzd/collegium/pkg/authz"
)

// TailscalePrincipal maps a login authenticated by Tailscale Serve to a user.
// The caller must verify the proxy before calling this. A login is namespaced
// to avoid collisions with OIDC subjects. No groups or verified email claims
// are inferred from a login name.
func TailscalePrincipal(tenant, login, name string, policy *Policy) (Principal, error) {
	if tenant == "" || login == "" || strings.ContainsAny(login, " \t\r\n,") {
		return Principal{}, ErrUnauthenticated
	}
	if policy == nil {
		policy = DefaultPolicy()
	}
	id := authz.Identity{Subject: "tailscale:" + login, Tenant: tenant}
	held := policy.c.Held(id)
	var roles []Role
	for _, h := range held {
		roles = append(roles, Role(h.Role))
	}
	return Principal{Subject: id.Subject, Tenant: tenant, Kind: User, Name: name,
		Roles: roles, policy: policy, held: held}, nil
}
