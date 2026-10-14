# GitHub and Slack trial

Collegium owns installation configuration, agent definitions, review and command
policy, and the run history shown in its console. Mandatum supplies signed
webhooks, API capabilities, routing and execution. Vultus `go/github` supplies
the review and guide Markdown. Existing web/Slack components remain in vultus.

## Local setup

Keep sibling checkouts of `collegium`, `mandatum`, and `vultus`. The Go modules
temporarily use explicit local replacements until coordinated releases exist.
Use `go test ./... -race` in collegium and `go test ./...` in vultus/go to verify
the composition. Run `go run ./examples/github-review` for a token-free preview.

Provide `ANTHROPIC_API_KEY` and `COLLEGIUM_AGENT_MODEL` for a model available to
your Anthropic account. The example specs use the supported `anthropic` provider.
Startup replaces the `YOUR_MODEL_ID` placeholder using `COLLEGIUM_AGENT_MODEL`.
An offline test is not evidence that a paid model has been exercised.

```sh
go run ./cmd/collegium serve -auth=dev -tenant=trial \
  -webhook-addr=127.0.0.1:8383 \
  -agent-specs=examples/github-review/agent.json,examples/github-review/fixer.json
# In another terminal:
make web
```

The console uses localhost:5173 and the API localhost:8080. External runs appear
on the same Runs page as console runs. Only tenant `trial` can see or use these
installations; inbound payloads cannot choose a tenant. One GitHub installation
and one Slack installation per process are supported. Stores are still in memory.

Forward **only port 8383**, whose handler exposes signed webhook endpoints and
readiness. `/api`, `/auth` and the console are absent there. Never forward the
dev-auth console/API listener. Stop the forwarder and process after the trial.

## One credential item

Keep the GitHub, Slack and model credentials together in one 1Password Secure
Note named `Collegium integrations`, with concealed custom fields. Reuse an
existing item when one exists. Copy values through 1Password's UI or CLI input,
not command-line arguments, source files, terminal output or chat.

| Field | Runtime use |
|---|---|
| `anthropic_api_key` | `ANTHROPIC_API_KEY` |
| `github_app_id`, `github_installation_id`, `github_private_key` | Mint a repository-scoped installation access token |
| `github_token` | `COLLEGIUM_GITHUB_TOKEN`, a current installation token |
| `github_webhook_secret` | `COLLEGIUM_GITHUB_WEBHOOK_SECRET` |
| `slack_bot_token` | `COLLEGIUM_SLACK_TOKEN` |
| `slack_signing_secret` | `COLLEGIUM_SLACK_SIGNING_SECRET` |
| `slack_team_id`, `slack_bot_user_id` | Workspace and agent routing |

The server accepts a token; it does not yet mint or refresh GitHub installation
tokens. Refresh expired tokens before a trial. App IDs/private keys alone do
not configure the connector. Keep non-secret model, repository, bot mappings and
forwarding URLs alongside the item's instructions. No credentials were created
or consolidated by this migration; a live trial needs the actual item and targets.

Use 1Password `op run --env-file` with a local reference-only environment file:

```dotenv
ANTHROPIC_API_KEY=op://Private/Collegium integrations/anthropic_api_key
COLLEGIUM_GITHUB_TOKEN=op://Private/Collegium integrations/github_token
COLLEGIUM_GITHUB_WEBHOOK_SECRET=op://Private/Collegium integrations/github_webhook_secret
COLLEGIUM_SLACK_TOKEN=op://Private/Collegium integrations/slack_bot_token
COLLEGIUM_SLACK_SIGNING_SECRET=op://Private/Collegium integrations/slack_signing_secret
```

Then start the command above through `op run --env-file=<local-reference-file> --`.
Do not commit that local file or capture the injected environment in logs.

## GitHub installation

Use GitHub's [manifest registration flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest)
with [collegium's manifest](../examples/github-review/app-manifest.json).
Replace its app name, webhook URL and callback URL.

1. Submit JSON in the `manifest` form field to `https://github.com/settings/apps/new`,
   with random `state`. Organization apps use `/organizations/ORG/settings/apps/new`.
2. Validate the returned `state` before accepting the callback's `code`.
3. Within one hour, exchange the code through
   `POST https://api.github.com/app-manifests/{code}/conversions`.
4. Save the returned credentials into the consolidated 1Password item, preserving
   Slack/Anthropic fields, then install the app on the selected repository.

The callback receiver must keep credentials out of browser responses and logs.
The template's localhost callback is a placeholder, not a receiver started by
`collegium serve`. Registration callback and 1Password-writing automation are not
implemented here. The transcript's temporary setup page is not bundled, and its
passkey prompt does not establish that an app was created.

| Setting | Value |
|---|---|
| Webhook URL | Your forwarder's public URL, targeting `/connectors/github` on port 8383 |
| Events | `issues`, `issue_comment`, `pull_request`, `pull_request_review`, `pull_request_review_comment` |
| Review permissions | Issues write, Pull requests write, Checks write, Contents read, Metadata read |
| Optional fix/check permissions | Contents write and Actions write; Actions read for observing CI |

A public third-party relay such as smee receives the webhook payload. Use an
approved sandbox repository for a trial and relay only GitHub deliveries to the
GitHub endpoint. Slack URL verification requires a request/response-capable
forwarder, not a GitHub-style asynchronous relay.

```sh
export COLLEGIUM_GITHUB_OWNERS='your-account'
export COLLEGIUM_GITHUB_BOTS='your-app[bot]=github-agent'
export COLLEGIUM_GITHUB_AUTO_LABELS='{"your-account/sandbox":{"agent:auto":"github-agent"}}'
export COLLEGIUM_GITHUB_CHECKS=true
export COLLEGIUM_GITHUB_GUIDES=true
```

Applying the configured label starts a review. Subsequent commits update its
guide and start separate reviews. Mention the app for questions. For writes,
map the app to `github-fixer`, update the label mapping accordingly, and enable
`COLLEGIUM_GITHUB_MUTATIONS=true`. Only explicit `/fix` or `/check <run-id>`
requests from repository writers receive a run-bound grant. See [GitHub](github.md).

## Slack installation

Create or update the app using [the manifest](../examples/slack/manifest.json),
replace `YOUR_HOST` with the forwarder URL, install it, and invite its bot to the
trial channel. Configure the bot token/signing secret through the credential
item. Set `COLLEGIUM_SLACK_TEAM`, `COLLEGIUM_SLACK_BOTS` (bot-user-id=agent-name)
and `COLLEGIUM_SLACK_DEFAULT_AGENT`. Team is required; another team's signed
events or commands are refused.

`COLLEGIUM_SLACK_SESSIONS=true` enables agent-session progress. Optional
`COLLEGIUM_SLACK_COMMANDS` is a JSON array of command names, descriptions and
argument hints. Advertise only tasks supported by the chosen agent; registering
`create-pr` does not itself add a PR-creation tool. GitHub mutations cannot be
authorized by a Slack command. Cross-source GitHub reads require exact repository
grants in `COLLEGIUM_GITHUB_READ_REPOSITORIES`.

Session and code-channel availability depends on Slack's workspace features.
Permanent feature/permission refusals are logged; normal threaded answers remain
usable. No stop button, code-channel creation or native streaming is advertised.
See [Slack](slack.md) for status/error behavior and the remaining limitations.

## Acceptance evidence

The automated suite checks signed ingress, correct tenant visibility in the
console API, guide updates, distinct reviews, thread replies, atomic fix commits,
workflow reruns and pending states, and Slack lifecycle/command policy using
fake or in-process APIs. A live trial additionally needs a real bot reply, the
same run in the console, actual model usage, and observed GitHub/Slack status.
Do not call it live-validated until those results have been collected.
