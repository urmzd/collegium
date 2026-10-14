package github

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/urmzd/mandatum/pkg/address"
	"github.com/urmzd/mandatum/pkg/connector"
	"strconv"
)

func (c *Connector) runChecksAction() connector.Action {
	return connector.Action{Name: "github_run_checks", Description: "Rerun the workflow explicitly named in this user's /check <run-id> request. Uses the authorized commit and attempt, which cannot be changed by arguments. Afterwards use github_workflow_status to observe real results.", Schema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Invoke: c.runChecks}
}

func (c *Connector) runChecks(ctx context.Context, target address.Address, raw json.RawMessage) (connector.Result, error) {
	ref, err := c.authorizeMutation(ctx, target, "check")
	if err != nil {
		return connector.Result{}, err
	}
	var args struct{}
	if err := decodeArguments(raw, &args); err != nil {
		return connector.Result{}, err
	}
	id, err := strconv.ParseInt(target.Params["write_workflow"], 10, 64)
	if err != nil || id <= 0 {
		return connector.Result{}, fmt.Errorf("github: missing authorized workflow")
	}
	attempt, err := strconv.Atoi(target.Params["write_attempt"])
	if err != nil || attempt <= 0 {
		return connector.Result{}, fmt.Errorf("github: missing authorized attempt")
	}
	api, ok := c.cfg.API.(WorkflowAPI)
	if !ok {
		return connector.Result{}, fmt.Errorf("%w: GitHub workflow execution", connector.ErrUnsupported)
	}
	run, err := api.RerunWorkflow(ctx, ref, id, target.Params["head_sha"], attempt)
	if err != nil {
		return connector.Result{}, err
	}
	data, _ := json.Marshal(run)
	summary := fmt.Sprintf("Observed workflow %d attempt %d: %s (%s). %s", run.ID, run.Attempt, run.Status, run.Conclusion, run.URL)
	if run.Requested {
		summary = fmt.Sprintf("Requested a rerun of workflow %d after attempt %d. Execution has not been verified; poll github_workflow_status. %s", run.ID, run.Attempt, run.URL)
	}
	return connector.Result{Summary: summary, Data: data, Address: refAddress(ref, "pull").WithParam("head_sha", run.HeadSHA)}, nil
}
