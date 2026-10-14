package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/urmzd/collegium/pkg/authz"
)

// ScopePrefix namespaces this platform's scopes at the identity provider, so a
// machine is issued "collegium:runs:write" rather than a bare "runs:write" that
// another API on the same authorization server might also define.
const ScopePrefix = "collegium:"

// OIDCConfig describes the identity provider. It works with any OpenID Connect
// provider. For Okta, Issuer is the authorization server, for example
// https://example.okta.com/oauth2/default.
type OIDCConfig struct {
	// Issuer is the provider's issuer URL. Discovery and keys come from it.
	Issuer string
	// ClientID is this platform's web client. ID tokens must be issued to it.
	ClientID string
	// Audience is what an access token must be issued for. Bearer tokens
	// with any other audience are rejected.
	Audience string
	// Tenant is the scope every caller is confined to, unless TenantClaim
	// names a claim that carries it.
	Tenant string
	// TenantClaim optionally names a claim holding the caller's tenant.
	TenantClaim string
	// GroupsClaim names the claim listing a person's groups. Empty means
	// "groups".
	GroupsClaim string
	// Policy decides which roles a person's claims earn and what each role
	// allows. Nil means DefaultPolicy, which grants no role by itself.
	Policy *Policy
	// GroupRoles maps a provider group to a role. Each entry becomes a
	// grant added to Policy when the verifier is made, so a role it names
	// must be declared there. A group not granted anything grants nothing.
	GroupRoles map[string]Role
}

// Identity is what a policy's grants are matched against, taken from a
// verified token's claims. The email is included only when the token says
// it is verified, so "email domain" cannot be met by an address nobody
// proved they own.
func (c OIDCConfig) Identity(subject string, claims map[string]any) (authz.Identity, error) {
	tenant, err := c.tenant(claims)
	if err != nil {
		return authz.Identity{}, err
	}
	groups := c.GroupsClaim
	if groups == "" {
		groups = "groups"
	}
	id := authz.Identity{Subject: subject, Tenant: tenant, Groups: strs(claims[groups]), Claims: claims}
	if verified, _ := claims["email_verified"].(bool); verified {
		id.Email = str(claims["email"])
	}
	return id, nil
}

func (c OIDCConfig) tenant(claims map[string]any) (string, error) {
	if c.TenantClaim == "" {
		return c.Tenant, nil
	}
	t := str(claims[c.TenantClaim])
	if t == "" {
		return "", fmt.Errorf("%w: token has no %q claim", ErrUnauthenticated, c.TenantClaim)
	}
	return t, nil
}

func (c OIDCConfig) validate() error {
	switch {
	case c.Issuer == "":
		return errors.New("auth: oidc needs an issuer")
	case !strings.HasPrefix(c.Issuer, "https://") && !isLoopbackURL(c.Issuer):
		return fmt.Errorf("auth: issuer %q must be https", c.Issuer)
	case c.ClientID == "":
		return errors.New("auth: oidc needs a client id")
	case c.Audience == "":
		return errors.New("auth: oidc needs an audience, or any token from the issuer would be accepted")
	case c.Tenant == "" && c.TenantClaim == "":
		return errors.New("auth: oidc needs a tenant or a tenant claim")
	}
	return nil
}

func isLoopbackURL(u string) bool {
	return strings.HasPrefix(u, "http://127.0.0.1:") || strings.HasPrefix(u, "http://localhost:")
}

// Verifier turns tokens from the identity provider into principals.
type Verifier struct {
	cfg OIDCConfig
	// policy is cfg.Policy with cfg.GroupRoles added as grants.
	policy   *Policy
	provider *oidc.Provider
	id       *oidc.IDTokenVerifier
	access   *oidc.IDTokenVerifier
}

// NewVerifier discovers the provider. It fails if the provider is unreachable,
// because a server that cannot verify tokens must not start.
func NewVerifier(ctx context.Context, cfg OIDCConfig) (*Verifier, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	policy := cfg.Policy
	if policy == nil {
		policy = DefaultPolicy()
	}
	// Before discovery, so a bad table fails without a network call.
	policy, err := policy.WithGroupRoles(cfg.GroupRoles)
	if err != nil {
		return nil, err
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: discover %s: %w", cfg.Issuer, err)
	}
	return &Verifier{
		cfg:      cfg,
		policy:   policy,
		provider: provider,
		id:       provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		access:   provider.Verifier(&oidc.Config{ClientID: cfg.Audience}),
	}, nil
}

// Policy is what the verifier assigns roles with: the configured policy plus
// the GroupRoles grants.
func (v *Verifier) Policy() *Policy { return v.policy }

// Provider exposes the discovered endpoints for the sign-in flow.
func (v *Verifier) Provider() *oidc.Provider { return v.provider }

// IDToken verifies a sign-in's ID token and returns the person. nonce must be
// the value this server sent with the authorization request.
func (v *Verifier) IDToken(ctx context.Context, raw, nonce string) (Principal, error) {
	tok, err := v.id.Verify(ctx, raw)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	if nonce == "" || tok.Nonce != nonce {
		return Principal{}, fmt.Errorf("%w: nonce mismatch", ErrUnauthenticated)
	}
	var claims map[string]any
	if err := tok.Claims(&claims); err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	return v.person(tok.Subject, claims)
}

// Bearer verifies an access token presented on an API request. A token with
// groups is a person. A token without is a machine, and gets only its scopes.
func (v *Verifier) Bearer(ctx context.Context, raw string) (Principal, error) {
	tok, err := v.access.Verify(ctx, raw)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	var claims map[string]any
	if err := tok.Claims(&claims); err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	if _, isPerson := claims[v.cfg.GroupsClaim]; isPerson {
		return v.person(tok.Subject, claims)
	}
	tenant, err := v.cfg.tenant(claims)
	if err != nil {
		return Principal{}, err
	}
	// A machine is never given roles, so the policy cannot widen it. It
	// gets exactly the scopes on its token.
	p := Principal{Subject: tok.Subject, Tenant: tenant, Kind: Machine, Name: str(claims["cid"])}
	for _, s := range scopes(claims) {
		name, ok := strings.CutPrefix(s, ScopePrefix)
		if !ok {
			continue
		}
		for _, perm := range Permissions() {
			if string(perm) == name {
				p.Scopes = append(p.Scopes, perm)
			}
		}
	}
	return p, nil
}

func (v *Verifier) person(subject string, claims map[string]any) (Principal, error) {
	id, err := v.cfg.Identity(subject, claims)
	if err != nil {
		return Principal{}, err
	}
	p := Principal{
		Subject: subject,
		Tenant:  id.Tenant,
		Kind:    User,
		Name:    str(claims["name"]),
		Email:   str(claims["email"]),
		Roles:   v.policy.Roles(id),
		policy:  v.policy,
	}
	return p, nil
}

// scopes reads Okta's "scp" array or the standard space-separated "scope".
func scopes(claims map[string]any) []string {
	if s := strs(claims["scp"]); len(s) > 0 {
		return s
	}
	return strings.Fields(str(claims["scope"]))
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strs(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{x}
	}
	return nil
}
