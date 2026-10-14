package platform

import (
	"testing"

	githubflow "github.com/urmzd/collegium/pkg/integrations/github"
	"github.com/urmzd/mandatum/pkg/registry"
	"github.com/urmzd/mandatum/pkg/runtime"
)

func TestConnectorToolsBelongOnlyToTheirInstallationTenant(t *testing.T) {
	peer, err := githubflow.New(githubflow.Config{API: githubflow.NewFake(), Owners: []string{"acme"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{Workspace: t.TempDir(), Connections: func(registry.Store) ([]Connection, error) { return []Connection{{Tenant: "acme", Peer: peer}}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{"acme", "other", ""} {
		catalog, err := p.connectorTools(runtime.Request{Tenant: tenant, Source: "github://acme/repo/issues/1", RunID: "run"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = catalog.Lookup("github_read_context")
		if (err == nil) != (tenant == "acme") {
			t.Fatalf("tenant %q tool lookup: %v", tenant, err)
		}
	}
}
