package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/urmzd/mandatum/pkg/agentrt"
	"github.com/urmzd/mandatum/pkg/runtime"
	saigetypes "github.com/urmzd/saige/agent/types"

	"github.com/urmzd/collegium/internal/platform"
)

// holding is a model that says something and then holds its call open until
// it is cancelled.
type holding struct{}

func (holding) Stream(ctx context.Context, _ saigetypes.Request) (<-chan saigetypes.Delta, error) {
	out := make(chan saigetypes.Delta)
	go func() {
		defer close(out)
		for _, d := range []saigetypes.Delta{
			saigetypes.PartStart{Index: 0, Kind: saigetypes.KindText},
			saigetypes.PartDelta{Index: 0, Text: "So far"},
		} {
			select {
			case out <- d:
			case <-ctx.Done():
				return
			}
		}
		<-ctx.Done()
	}()
	return out, nil
}

// POST /api/runs/{id}/stop does not wait for the run. A run still working is
// answered 202 with stopping set, and reads stopped, with its partial answer
// and cause, once the runtime confirms it. Another tenant gets a 404, and a
// second stop is a 200 that changes nothing.
func TestStoppingARunIsAcceptedThenConfirmed(t *testing.T) {
	f := newFixtureWith(t, platform.Config{AgentOptions: []agentrt.Option{
		agentrt.WithProvider(agentrt.ProviderOffline, func(agentrt.Spec) (saigetypes.Provider, error) { return holding{}, nil }),
	}})
	all := []string{"collegium:agents:write", "collegium:runs:read", "collegium:runs:write"}
	acme, globex := f.machine(t, "acme", all...), f.machine(t, "globex", all...)
	if res, _ := f.do(t, nil, "POST", "/api/agents", acme, helperSpec); res.StatusCode != http.StatusCreated {
		t.Fatalf("create agent = %d", res.StatusCode)
	}
	_, run := f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"hi"}`)
	id := run["id"].(string)
	read := func() map[string]any {
		_, out := f.do(t, nil, "GET", "/api/runs/"+id, acme, "")
		return out
	}
	deadline := time.Now().Add(10 * time.Second)
	for read()["status"] != "running" {
		if time.Now().After(deadline) {
			t.Fatalf("run never started: %v", read())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if res, _ := f.do(t, nil, "POST", "/api/runs/"+id+"/stop", globex, ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("another tenant's stop = %d, want 404", res.StatusCode)
	}
	if out := read(); out["stopping"] == true {
		t.Fatal("another tenant's stop reached the run")
	}

	res, out := f.do(t, nil, "POST", "/api/runs/"+id+"/stop", acme, "")
	switch {
	case res.StatusCode == http.StatusAccepted && out["stopping"] == true && out["status"] != "stopped":
	case res.StatusCode == http.StatusOK && out["status"] == "stopped":
	default:
		t.Fatalf("stop = %d %v", res.StatusCode, out)
	}
	for out = read(); out["status"] != "stopped"; out = read() {
		if time.Now().After(deadline) {
			t.Fatalf("run never stopped: %v", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if out["answer"] != "So far" || out["cause"] != "Stopped by svc-acme" || out["stopping"] != nil {
		t.Fatalf("stopped run = %v", out)
	}
	if res, again := f.do(t, nil, "POST", "/api/runs/"+id+"/stop", acme, ""); res.StatusCode != http.StatusOK || again["status"] != "stopped" {
		t.Fatalf("second stop = %d %v", res.StatusCode, again)
	}
}

// A runtime that cannot interrupt a run fails closed: 503 with an error body,
// and the run is left stopping, so it starts nothing more.
func TestAStopTheRuntimeCannotCarryIsA503(t *testing.T) {
	f := newFixtureWith(t, platform.Config{
		AgentOptions: []agentrt.Option{
			agentrt.WithProvider(agentrt.ProviderOffline, func(agentrt.Spec) (saigetypes.Provider, error) { return holding{}, nil }),
		},
		Stops: func(context.Context, runtime.StopRequest) error { return runtime.ErrStopUnavailable },
	})
	all := []string{"collegium:agents:write", "collegium:runs:read", "collegium:runs:write"}
	acme := f.machine(t, "acme", all...)
	if res, _ := f.do(t, nil, "POST", "/api/agents", acme, helperSpec); res.StatusCode != http.StatusCreated {
		t.Fatalf("create agent = %d", res.StatusCode)
	}
	_, run := f.do(t, nil, "POST", "/api/runs", acme, `{"agent":"helper","text":"hi"}`)
	id := run["id"].(string)
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, out := f.do(t, nil, "GET", "/api/runs/"+id, acme, "")
		if out["status"] == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never started: %v", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
	res, out := f.do(t, nil, "POST", "/api/runs/"+id+"/stop", acme, "")
	if res.StatusCode != http.StatusServiceUnavailable || out["error"] == nil {
		t.Fatalf("stop = %d %v, want 503 with an error", res.StatusCode, out)
	}
	if _, now := f.do(t, nil, "GET", "/api/runs/"+id, acme, ""); now["stopping"] != true || now["status"] != "running" {
		t.Fatalf("after a refused stop the run is %v", now)
	}
}

// A keyed run's trigger id cannot be read two ways: the subject's length says
// where it ends, and a run started without a key is a shape no keyed id has.
func TestAnAPITagIDIsUnambiguous(t *testing.T) {
	if APITagID("a", "b-c") == APITagID("a-b", "c") {
		t.Fatal("two callers and keys gave one trigger id")
	}
	if got := APITagID("svc-acme", "slack-evt-1"); got != "api-8-svc-acme-slack-evt-1" {
		t.Fatalf("APITagID = %q", got)
	}
}
