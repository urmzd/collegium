package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/urmzd/collegium/pkg/call"
	"github.com/urmzd/collegium/pkg/runs"
	"github.com/urmzd/duraturo"
	"github.com/urmzd/mandatum/pkg/agentrt"
	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/mandatum/pkg/runtime"
)

// prepareCall resolves references once, before accepting the child. The tag
// stores the resolved bytes as well as their provenance, so retries never read
// newer conversation turns, changed artifacts, or a different result.
func (p *Platform) prepareCall(ctx context.Context, tenant string, input call.Input) (call.Prepared, error) {
	if err := input.Validate(); err != nil {
		return call.Prepared{}, err
	}
	out := call.Prepared{Input: input}
	size := 0
	for _, m := range input.Messages {
		var parts []string
		for _, part := range m.Content {
			text, err := p.resolveCallPart(ctx, tenant, part)
			if err != nil {
				return call.Prepared{}, err
			}
			size += len(text) + 2
			if size > call.MaxInputBytes {
				return call.Prepared{}, fmt.Errorf("%w: resolved input exceeds %d bytes", call.ErrInvalid, call.MaxInputBytes)
			}
			parts = append(parts, text)
		}
		out.Messages = append(out.Messages, call.ResolvedMessage{Role: m.Role, Text: strings.Join(parts, "\n\n")})
	}
	return out, out.Validate()
}
func (p *Platform) resolveCallPart(ctx context.Context, tenant string, part call.Part) (string, error) {
	switch v := part.(type) {
	case call.TextPart:
		return v.Text, nil
	case call.RunResultPart:
		r, err := p.Runs.Get(tenant, v.Run)
		if err != nil {
			return "", err
		}
		if r.Status != runs.Completed {
			return "", fmt.Errorf("%w: referenced result is not completed", call.ErrInvalid)
		}
		return r.Answer, nil
	case call.ConversationRange:
		var selected []runs.Run
		id := v.Through
		for len(selected) < call.MaxReferenceTurns {
			r, err := p.Runs.Get(tenant, id)
			if err != nil {
				return "", err
			}
			if r.Thread != v.Thread || r.Parent != "" {
				return "", fmt.Errorf("%w: range is outside the named conversation branch", call.ErrInvalid)
			}
			if !r.Status.Terminal() {
				return "", fmt.Errorf("%w: referenced conversation turn has not ended", call.ErrInvalid)
			}
			selected = append(selected, r)
			if id == v.From {
				slices.Reverse(selected)
				var b strings.Builder
				for _, turn := range selected {
					fmt.Fprintf(&b, "User: %s\nAssistant (%s, %s): %s\n", turn.Text, turn.Agent, turn.Status, turn.Answer)
					if b.Len() > call.MaxInputBytes {
						return "", fmt.Errorf("%w: conversation range is too large", call.ErrInvalid)
					}
				}
				return b.String(), nil
			}
			id = r.Prev
			if id == "" {
				break
			}
		}
		return "", fmt.Errorf("%w: range endpoints must share a branch of at most %d turns", call.ErrInvalid, call.MaxReferenceTurns)
	case call.ArtifactPart:
		a, err := p.Artifacts.Get(tenant, v.ID)
		if err != nil {
			return "", err
		}
		if a.Version != v.Version {
			return "", fmt.Errorf("%w: artifact version does not match", call.ErrInvalid)
		}
		if a.Size > call.MaxInputBytes {
			return "", fmt.Errorf("%w: artifact is too large", call.ErrInvalid)
		}
		r, _, err := p.Blobs.Get(ctx, tenant, a.Key)
		if err != nil {
			return "", err
		}
		defer r.Close()
		b, err := io.ReadAll(io.LimitReader(r, call.MaxInputBytes+1))
		if err != nil {
			return "", err
		}
		if len(b) > call.MaxInputBytes || !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
			return "", fmt.Errorf("%w: artifact must be bounded UTF-8 text", call.ErrInvalid)
		}
		return string(b), nil
	default:
		return "", fmt.Errorf("%w: unsupported input part", call.ErrInvalid)
	}
}

// callExecutor adapts explicit helper messages to mandatum's existing history
// and request fields. It composes the sandbox; it never owns an agent loop.
type callExecutor struct{ inner runtime.Executor }

func (e callExecutor) Execute(ctx context.Context, req runtime.Request, emit func(context.Context, agentrt.Chunk) error) (runtime.Outcome, error) {
	var format call.ResponseFormat
	if req.Origin == call.Origin {
		raw := req.Meta[call.MetaInput]
		if raw != "" {
			var prepared call.Prepared
			if err := call.StrictDecode([]byte(raw), &prepared); err != nil {
				return runtime.Outcome{}, err
			}
			if err := prepared.Validate(); err != nil {
				return runtime.Outcome{}, err
			}
			edge, err := call.EdgeOf(envelope.Tag{Origin: req.Origin, Meta: req.Meta})
			if err != nil {
				return runtime.Outcome{}, err
			}
			format = prepared.Input.ResponseFormat
			req.Instructions = format.Instruction()
			if edge.Mode != call.Handoff {
				req.Instructions += "\nYou are a helper. Return your result to the calling agent, which owns the user-facing answer. Do not present a second user-facing answer unless asked to draft text. Background results are recorded for later inspection."
			}
			req.History = nil
			for _, m := range prepared.Messages[:len(prepared.Messages)-1] {
				req.History = append(req.History, agentrt.Message{Role: agentrt.Role(m.Role), Text: m.Text})
			}
			req.Text = prepared.Messages[len(prepared.Messages)-1].Text
			// Provenance belongs in the journal, not in the model's prompt.
			req.Meta = nil
		}
	}
	if len(req.Revision.Spec.Access.Spawn) > 0 {
		req.Instructions += "\nYou own the answer to your caller. Use call_agent for bounded helper work with explicit input messages and a return format. Incorporate useful results into one answer; do not narrate and repeat each helper's full answer."
	}
	// Buffer contracted outputs until validation succeeds. An invalid helper
	// answer must not appear as accepted text or a successful tool result.
	var buffered []agentrt.Chunk
	send := emit
	if format != nil {
		size := 0
		send = func(ctx context.Context, c agentrt.Chunk) error {
			if c.Kind == "delta.text" {
				size += len(c.Payload)
				if size > call.MaxOutputChars*6 {
					return fmt.Errorf("%w: helper output exceeds stream limit", call.ErrInvalid)
				}
				buffered = append(buffered, c)
				return nil
			}
			if emit != nil {
				return emit(ctx, c)
			}
			return nil
		}
	}
	out, err := e.inner.Execute(ctx, req, send)
	if err != nil {
		return out, err
	}
	if format != nil {
		if utf8.RuneCountInString(out.Text) > call.MaxOutputChars {
			return runtime.Outcome{}, duraturo.NonRetryable(fmt.Errorf("%w: helper output exceeds limit", call.ErrInvalid))
		}
		if err := format.ValidateOutput(out.Text); err != nil {
			return runtime.Outcome{}, duraturo.NonRetryable(err)
		}
		for _, c := range buffered {
			if emit != nil {
				if err := emit(ctx, c); err != nil {
					return runtime.Outcome{}, err
				}
			}
		}
	}
	return out, nil
}

func freezeInput(tagMeta map[string]string, prepared call.Prepared) error {
	b, err := json.Marshal(prepared)
	if err != nil {
		return err
	}
	tagMeta[call.MetaInput] = string(b)
	return nil
}

func sameCallInput(existing runs.Run, req call.Request, input call.Input) bool {
	if existing.Agent != req.Agent || existing.Mode != req.Mode || existing.Lease != req.Lease {
		return false
	}
	raw := existing.Tag().Meta[call.MetaInput]
	if raw == "" {
		return req.Input == nil && existing.Text == req.Text
	}
	var frozen call.Prepared
	if err := call.StrictDecode([]byte(raw), &frozen); err != nil {
		return false
	}
	a, err := json.Marshal(input)
	if err != nil {
		return false
	}
	b, err := json.Marshal(frozen.Input)
	return err == nil && string(a) == string(b)
}
