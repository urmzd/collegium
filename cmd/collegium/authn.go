package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/urmzd/collegium/internal/httpapi"
	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/auth/mockidp"
)

// policyEnv names the file holding the authorization policy. Unset means the
// built-in policy. See docs/authorization.md.
const policyEnv = "COLLEGIUM_AUTHZ_POLICY"

// mockAudience is the API audience the mock provider issues access tokens for.
const mockAudience = "api://collegium-dev"

// groupFlags are the -admin-group, -operator-group and -viewer-group flags,
// which predate the policy language and become grants.
type groupFlags struct {
	admin, operator, viewer *string
}

func addGroupFlags(fs *flag.FlagSet) groupFlags {
	return groupFlags{
		admin:    fs.String("admin-group", "collegium-admins", "identity provider group granted the admin role"),
		operator: fs.String("operator-group", "collegium-operators", "identity provider group granted the operator role"),
		viewer:   fs.String("viewer-group", "collegium-viewers", "identity provider group granted the viewer role"),
	}
}

// loadPolicy reads the policy named by policyEnv, or returns the built-in
// one, and the group table to add to it. Without a policy file the group
// flags apply with their defaults, as they always have. With one, the file
// says who gets what, and a group flag applies only if it was set on the
// command line. Any problem stops the server.
func loadPolicy(fs *flag.FlagSet, g groupFlags, getenv func(string) string) (*auth.Policy, map[string]auth.Role, error) {
	all := map[string]auth.Role{*g.admin: auth.Admin, *g.operator: auth.Operator, *g.viewer: auth.Viewer}
	path := strings.TrimSpace(getenv(policyEnv))
	if path == "" {
		return auth.DefaultPolicy(), all, nil
	}
	pol, err := auth.LoadPolicyFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", policyEnv, err)
	}
	set := map[string]auth.Role{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "admin-group":
			set[*g.admin] = auth.Admin
		case "operator-group":
			set[*g.operator] = auth.Operator
		case "viewer-group":
			set[*g.viewer] = auth.Viewer
		}
	})
	return pol, set, nil
}

// authConfig is what the sign-in modes are built from.
type authConfig struct {
	mode, addr, publicURL       string
	tenant, tenantClaim, groups string
	policy                      *auth.Policy
	groupRoles                  map[string]auth.Role
	mockAddr, mockPersonas      string
	getenv                      func(string) string
	log                         *slog.Logger
}

// newAuthn builds the authenticator for a mode. stop releases what it started.
func newAuthn(ctx context.Context, c authConfig) (authn *httpapi.Authn, stop func(), err error) {
	stop = func() {}
	switch c.mode {
	case "dev":
		if !loopback(c.addr) {
			return nil, stop, fmt.Errorf("-auth=dev grants everyone admin, so it only listens on loopback, not %q", c.addr)
		}
		if !c.policy.HasRole(auth.Admin) {
			return nil, stop, fmt.Errorf("-auth=dev signs everyone in as admin, and the policy declares no admin role")
		}
		c.log.Warn("dev auth: every request is an admin. Do not expose this server.")
		dev := auth.Principal{
			Subject: "dev", Tenant: c.tenant, Kind: auth.User, Name: "Developer", Roles: []auth.Role{auth.Admin},
		}.WithPolicy(c.policy)
		return &httpapi.Authn{Dev: &dev}, stop, nil

	case "tailscale":
		if c.tenantClaim != "" {
			return nil, stop, errors.New("-auth=tailscale uses -tenant, not -tenant-claim")
		}
		authn, err := httpapi.NewTailscaleAuthn(c.addr, c.publicURL, c.tenant, c.policy)
		return authn, stop, err

	case "oidc", "okta":
		v, err := auth.NewVerifier(ctx, auth.OIDCConfig{
			Issuer:      c.getenv("COLLEGIUM_OIDC_ISSUER"),
			ClientID:    c.getenv("COLLEGIUM_OIDC_CLIENT_ID"),
			Audience:    c.getenv("COLLEGIUM_OIDC_AUDIENCE"),
			Tenant:      c.tenant,
			TenantClaim: c.tenantClaim,
			GroupsClaim: c.groups,
			Policy:      c.policy,
			GroupRoles:  c.groupRoles,
		})
		if err != nil {
			return nil, stop, fmt.Errorf("%w (set COLLEGIUM_OIDC_ISSUER, COLLEGIUM_OIDC_CLIENT_ID, COLLEGIUM_OIDC_CLIENT_SECRET and COLLEGIUM_OIDC_AUDIENCE, or use -auth=dev or -auth=mock locally)", err)
		}
		authn, err := httpapi.NewAuthn(v, c.getenv("COLLEGIUM_OIDC_CLIENT_ID"), c.getenv("COLLEGIUM_OIDC_CLIENT_SECRET"), c.publicURL,
			[]string{"openid", "profile", "email", c.groups})
		return authn, stop, err

	case "mock":
		return mockAuthn(ctx, c)
	}
	return nil, stop, fmt.Errorf("-auth must be oidc, okta, tailscale, mock or dev, not %q", c.mode)
}

// mockAuthn starts the development identity provider on loopback and signs in
// against it through the same verifier and sign-in flow as a real provider.
func mockAuthn(ctx context.Context, c authConfig) (*httpapi.Authn, func(), error) {
	stop := func() {}
	if !loopback(c.addr) {
		return nil, stop, fmt.Errorf("-auth=mock lets anyone sign in as anyone, so it only listens on loopback, not %q", c.addr)
	}
	people, machines := mockidp.DefaultPeople(), mockidp.DefaultMachines()
	if c.mockPersonas != "" {
		var err error
		if people, machines, err = readPersonas(c.mockPersonas); err != nil {
			return nil, stop, err
		}
	}
	tenantClaim := c.tenantClaim
	if tenantClaim == "" {
		// Personas may name their own tenant, so the tenant comes from a
		// claim the mock always sets. A persona without one gets -tenant.
		tenantClaim = "tenant"
	}
	idp, err := mockidp.Start(mockidp.Config{
		Enabled:      true,
		Addr:         c.mockAddr,
		ClientID:     mockidp.DevClientID,
		ClientSecret: mockidp.DevClientSecret,
		RedirectURIs: []string{strings.TrimSuffix(c.publicURL, "/") + "/auth/callback"},
		Audience:     mockAudience,
		Tenant:       c.tenant,
		TenantClaim:  tenantClaim,
		GroupsClaim:  c.groups,
		People:       people,
		Machines:     machines,
		Log:          c.log,
	})
	if err != nil {
		return nil, stop, err
	}
	stop = func() { _ = idp.Close() }
	v, err := auth.NewVerifier(ctx, auth.OIDCConfig{
		Issuer:      idp.Issuer(),
		ClientID:    mockidp.DevClientID,
		Audience:    mockAudience,
		TenantClaim: tenantClaim,
		GroupsClaim: c.groups,
		Policy:      c.policy,
		GroupRoles:  c.groupRoles,
	})
	if err != nil {
		stop()
		return nil, func() {}, err
	}
	authn, err := httpapi.NewAuthn(v, mockidp.DevClientID, mockidp.DevClientSecret, c.publicURL,
		[]string{"openid", "profile", "email", c.groups})
	if err != nil {
		stop()
		return nil, func() {}, err
	}
	c.log.Warn("mock sign-in: COLLEGIUM_OIDC_* is ignored. Sign in with no password as any persona.",
		"sign_in", strings.TrimSuffix(c.publicURL, "/")+"/auth/login", "issuer", idp.Issuer(), "audience", mockAudience)
	return authn, stop, nil
}

// personas is the file -mock-personas reads.
type personas struct {
	People   []mockidp.Person  `json:"people"`
	Machines []mockidp.Machine `json:"machines"`
}

func readPersonas(path string) ([]mockidp.Person, []mockidp.Machine, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("-mock-personas: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p personas
	if err := dec.Decode(&p); err != nil {
		return nil, nil, fmt.Errorf("-mock-personas %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("-mock-personas %s: more than one JSON value", path)
	}
	return p.People, p.Machines, nil
}

// runAuthz is "collegium authz": checking a policy file, and explaining a
// decision for a token's claims the way the server would make it.
func runAuthz(args []string, stdout io.Writer, getenv func(string) string) error {
	const usage = "usage: collegium authz check FILE | authz explain [-claims FILE] [-tenant-claim C] [-groups-claim C] [-*-group G] PERMISSION"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "check":
		if len(args) != 2 {
			return errors.New(usage)
		}
		pol, err := auth.LoadPolicyFile(args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s: ok, roles %s, %d grants\n", args[1], strings.Join(pol.Compiled().RoleNames(), ", "), len(pol.Compiled().Grants()))
		return nil
	case "explain":
		fs := flag.NewFlagSet("authz explain", flag.ContinueOnError)
		claimsFile := fs.String("claims", "", "JSON file of a token's claims, such as a decoded ID token's payload. Empty means no caller")
		tenant := fs.String("tenant", "default", "as for serve")
		tenantClaim := fs.String("tenant-claim", "", "as for serve")
		groupsClaim := fs.String("groups-claim", "groups", "as for serve")
		groups := addGroupFlags(fs)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New(usage)
		}
		perm := auth.Permission(fs.Arg(0))
		if !isPermission(perm) {
			return fmt.Errorf("unknown permission %q", perm)
		}
		pol, groupRoles, err := loadPolicy(fs, groups, getenv)
		if err != nil {
			return err
		}
		if pol, err = pol.WithGroupRoles(groupRoles); err != nil {
			return err
		}
		claims := map[string]any{}
		if *claimsFile != "" {
			raw, err := os.ReadFile(*claimsFile)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &claims); err != nil {
				return fmt.Errorf("%s: %w", *claimsFile, err)
			}
		}
		sub, _ := claims["sub"].(string)
		cfg := auth.OIDCConfig{Tenant: *tenant, TenantClaim: *tenantClaim, GroupsClaim: *groupsClaim}
		id, err := cfg.Identity(sub, claims)
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, pol.Compiled().Explain(id, string(perm)))
		return nil
	}
	return errors.New(usage)
}

func isPermission(p auth.Permission) bool {
	for _, q := range auth.Permissions() {
		if q == p {
			return true
		}
	}
	return false
}
