package github

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/urmzd/mandatum/pkg/address"
	"github.com/urmzd/mandatum/pkg/connector"
	transport "github.com/urmzd/mandatum/pkg/connectors/github"
	"github.com/urmzd/mandatum/pkg/connectors/mention"
	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/mandatum/pkg/runtime"
	"io"
	"strconv"
	"strings"
)

func (c *Connector) mutationAction() connector.Action {
	return connector.Action{Name: "github_apply_changes", Description: "Apply an explicitly requested /fix as one atomic commit to this PR's pinned head. Requires a signed request from a repository writer. Read files before editing. If the branch moved, ask for a new request. This does not run tests.", Schema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string","description":"Short commit message."},"files":{"type":"array","minItems":1,"maxItems":20,"items":{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string","description":"Complete replacement text, not a diff."},"delete":{"type":"boolean"}},"required":["path"],"additionalProperties":false}}},"required":["message","files"],"additionalProperties":false}`), Invoke: c.applyChanges}
}

func (c *Connector) mutationMAC(target address.Address) []byte {
	// A separate protocol prefix prevents using a webhook signature as a grant,
	// even when a deployment uses its webhook secret as the signing key.
	fields := []string{"mandatum.github.mutation.v1", target.Workspace, strings.Join(target.Path, "/"), target.Params["head_sha"], target.Params["write_actor"], target.Params["write_run"], target.Params["write_operation"], target.Params["write_workflow"], target.Params["write_attempt"]}
	encoded, _ := json.Marshal(fields)
	mac := hmac.New(sha256.New, c.cfg.MutationSecret)
	_, _ = mac.Write(encoded)
	return mac.Sum(nil)
}

func (c *Connector) grantMutation(ctx context.Context, tag *envelope.Tag) error {
	if !c.cfg.Mutations || tag.Source.Path[1] != "pull" || tag.Actor.Bot {
		return nil
	}
	event := tag.Meta["github_event"]
	if event != "issue_comment" && event != "pull_request_review_comment" {
		return nil
	}
	command := strings.Fields(tag.Text)
	if len(command) == 0 || (command[0] != "/fix" && command[0] != "/check") {
		return nil
	}
	// The grant names the run this tag becomes: the runtime's id for the
	// tenant and the delivery id together, which is what the run's tools are
	// bound to. Refuse synthetic or malformed delivery ids.
	if !mention.Valid(tag.ID) || !transport.ValidSHA(tag.Source.Params["head_sha"]) {
		return fmt.Errorf("github: fix request needs delivery ID and pinned head")
	}
	ref, err := c.Reference(tag.Source)
	if err != nil {
		return err
	}
	allowed, err := c.cfg.API.(MutationAPI).CanWrite(ctx, ref, tag.Actor.ID)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	} // The agent can still explain the denied capability.
	operation := strings.TrimPrefix(command[0], "/")
	source := tag.Source.WithParam("write_actor", tag.Actor.ID).WithParam("write_run", runtime.RunID(c.cfg.Tenant, tag.ID)).WithParam("write_operation", operation)
	if operation == "check" {
		if len(command) < 2 {
			return nil
		}
		id, err := strconv.ParseInt(command[1], 10, 64)
		if err != nil || id <= 0 {
			return nil
		}
		api, ok := c.cfg.API.(WorkflowAPI)
		if !ok {
			return fmt.Errorf("github: workflow execution unavailable")
		}
		run, err := api.WorkflowRun(ctx, ref, id)
		if err != nil {
			return err
		}
		if run.HeadSHA != source.Params["head_sha"] {
			return nil
		}
		source = source.WithParam("write_workflow", strconv.FormatInt(id, 10)).WithParam("write_attempt", strconv.Itoa(run.Attempt))
	}
	tag.Source = source.WithParam("write_grant", hex.EncodeToString(c.mutationMAC(source)))
	return nil
}

func (c *Connector) authorizeMutation(ctx context.Context, target address.Address, operation string) (Ref, error) {
	ref, err := c.Reference(target)
	if err != nil {
		return Ref{}, err
	}
	if !c.cfg.Mutations || target.Path[1] != "pull" || !transport.ValidSHA(target.Params["head_sha"]) || target.Params["write_operation"] != operation || connector.RunID(ctx) == "" || target.Params["write_run"] != connector.RunID(ctx) {
		return Ref{}, fmt.Errorf("github: mutation requires an authorized %s request in this run", operation)
	}
	signature, err := hex.DecodeString(target.Params["write_grant"])
	if err != nil || !hmac.Equal(signature, c.mutationMAC(target)) {
		return Ref{}, fmt.Errorf("github: invalid mutation grant")
	}
	allowed, err := c.cfg.API.(MutationAPI).CanWrite(ctx, ref, target.Params["write_actor"])
	if err != nil {
		return Ref{}, err
	}
	if !allowed {
		return Ref{}, fmt.Errorf("github: requester no longer has repository write permission")
	}
	return ref, nil
}

func (c *Connector) applyChanges(ctx context.Context, target address.Address, raw json.RawMessage) (connector.Result, error) {
	ref, err := c.authorizeMutation(ctx, target, "fix")
	if err != nil {
		return connector.Result{}, err
	}
	var args ChangeRequest
	if err := decodeArguments(raw, &args); err != nil {
		return connector.Result{}, err
	}
	if err := transport.ValidateChanges(args); err != nil {
		return connector.Result{}, err
	}
	result, err := c.cfg.API.(MutationAPI).CommitChanges(ctx, ref, target.Params["head_sha"], target.Params["write_run"], args)
	if err != nil {
		return connector.Result{}, err
	}
	data, _ := json.Marshal(result)
	return connector.Result{Summary: fmt.Sprintf("Created commit %s: %s. Tests have not been run by this action.", result.SHA, result.URL), Address: refAddress(ref, "pull").WithParam("head_sha", result.SHA), Data: data}, nil
}

func decodeArguments(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("github: arguments: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("github: expected one arguments object")
	}
	return nil
}
