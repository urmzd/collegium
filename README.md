<p align="center">
  <h1 align="center">open-agent-platform</h1>
  <p align="center">
    Spec-defined agents in sandboxes that call each other over one bus, with governance, human approvals, evals and telemetry built in.
  </p>
</p>

> ## Status
>
> **Early. One package is built.** `pkg/call` is implemented and tested.
> Everything else is planned. See the
> [status table](docs/architecture.md#status).

## What it is

The composition of [opentag](https://github.com/urmzd/opentag) (bus and
connectors), [saige](https://github.com/urmzd/saige) (agent runtime and evals),
[dispatch](https://github.com/urmzd/dispatch) (sandboxes) and
[duraturo](https://github.com/urmzd/duraturo) (durability), with agent-ui as
the component library for web and Slack.

The idea that holds it together: **an agent calls another agent by raising a
tag at it.** A subagent, a handoff and a delegation are the same call with a
different mode. Each call is its own run in its own sandbox, so branches are
detached, compact alone, and can spread across nodes. Budgets, access and depth
limits are checked on the edge between the two runs.

Read [docs/architecture.md](docs/architecture.md) for the full design.

## Use

```go
child, err := call.New(parent, call.Request{
	Key:   toolCallID,
	Agent: "coder",
	Text:  "fix the failing test",
	Mode:  call.Subagent,
	Lease: 250_000, // micro-USD
}, state, call.Limits{MaxDepth: 3, MaxFanOut: 8}, time.Now())
if err != nil {
	// errors.Is(err, call.ErrDepth | call.ErrFanOut | call.ErrBudget)
}
```

`child` is an opentag `envelope.Tag`. Publishing it starts the run.

## Develop

| Task | Command |
|------|---------|
| Set up hooks and modules | `make init` |
| Format, lint and test | `make check` |
| Test only | `make test` |

## License

[Apache-2.0](LICENSE)
