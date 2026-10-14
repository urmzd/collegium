# Security

How the platform decides who is calling, what they may do, and what it refuses.
Report vulnerabilities as described in [SECURITY.md](../SECURITY.md).

## Callers

| Caller | How it authenticates | What it gets |
|--------|----------------------|--------------|
| A person in a browser | OpenID Connect authorization code flow with PKCE, run by the server. The browser holds an opaque session cookie and never a token. | Roles, from the policy's grants |
| A person in a CLI | An access token as `Authorization: Bearer` | Roles, from the policy's grants |
| A machine | A client-credentials access token as `Authorization: Bearer` | Exactly the scopes on the token |

Every caller becomes a `Principal`, and every route asks it one question:
`Can(permission)`.

## Roles and scopes

Roles and who holds them are set by an [authorization policy](authorization.md).
The built-in one declares these three, and the group flags grant them:

| Role | Permissions |
|------|-------------|
| `viewer` | `agents:read`, `runs:read`, `schedules:read`, `alerts:read`, `knowledge:read`, `evals:read`, `billing:read`, `artifacts:read`, `clients:read` |
| `operator` | viewer, plus `runs:write`, `schedules:write`, `alerts:write`, `evals:write`, `artifacts:write` |
| `admin` | operator, plus `agents:write`, `knowledge:write`, `billing:write`, `clients:write` |

Writing knowledge is an admin permission because a document an agent is grounded
in changes what that agent says. Writing clients is one because a client hands
the workspace's agents a credential to act with outside it.

A webhook from Slack or GitHub is not a `Principal`. It is authorized by the
signing secret of the [client](clients.md) it is for, and takes its tenant from
that client. No client secret is returned by the API or written to a log.

A client may reference a secret in the server's environment instead of writing
its value. The environment is shared by every tenant, so a reference is read
for a tenant only if the operator listed that variable for that tenant with
`-secret-refs`. A tenant not listed may reference nothing. The check is made
where the secret is read, and a refusal is the same whatever the reason. See
[clients](clients.md#which-variables-a-workspace-may-reference).

A machine has no role. It is issued scopes named `collegium:<permission>`, for
example `collegium:runs:write`. A scope without the `collegium:` prefix, or one that names
no permission, grants nothing.

## Rules the code holds

- **Default deny.** The zero `Principal` can do nothing. An unmapped group, an
  unknown role and an unknown scope each grant nothing. A deny in the policy
  beats every allow.
- **A bad policy stops the server.** One that does not parse or compile is an
  error at start, never at decision time.
- **Tenant is never taken from a request.** It comes from the deployment or a
  verified token claim. A body that carries a `tenant` field is rejected.
- **Another tenant's objects do not exist.** Reads, deletes, calls and stops
  across tenants return 404, never 403, so a response cannot confirm that
  something exists.
- **A run id carries its tenant.** The runtime derives it from the tenant and
  the tag id together, so a trigger's id, chosen outside, cannot name or join
  another tenant's run. A stop request is honoured only by a run of the tenant
  it was made under, and the platform only makes one for a run the caller's
  tenant holds.
- **A run's trigger id is visible to its workspace.** `tag_id` on a run is the
  id it was accepted with: a Slack or GitHub event or delivery id, a schedule
  occurrence, or for the API `api-<length of subject>-<subject>-<key>`, which
  includes the subject of whoever started it and the `Idempotency-Key` they
  sent. Anyone with `runs:read` in the workspace can read it. Do not put
  secrets in an idempotency key.
- **An ID token is not an API credential.** Bearer tokens must be issued for
  the API audience. A token issued to the web client is refused.
- **Sessions are server-side.** The cookie is a random 256-bit id, `HttpOnly`,
  `SameSite=Lax`, and `Secure` with the `__Host-` prefix whenever the public
  URL is https. Signing out deletes the session on the server.
- **Sign-in is bound to the browser that started it.** State is checked against
  a cookie, the nonce is checked in the ID token, and the code exchange carries
  the PKCE verifier.
- **Redirects stay on this site.** `return_to` must be a local path.
- **Cross-origin writes are refused** before they reach a handler, using Go's
  `http.CrossOriginProtection`.
- **A surface answers only for people it was given.** A Slack app built on the
  API answers a question, or asks for a run, for a Slack person (`act_as`) only
  through a Slack client that names the app's own credential as its `machine`
  and maps that person in `people`. Anyone unmapped is refused (403) and the
  app does not ask. To answer, the mapped person's own grant (what the policy
  grants their subject) must include `runs:write`, as well as the
  credential's. The answer is recorded as the mapped principal, with the
  credential as `Via`: a credential never approves a call in a run it raised
  as itself, whoever it names. Mapping a person is `clients:write`.
- **The machine holds its people's votes.** Whoever holds the credential a
  Slack client names as `machine` can answer every question as any person the
  client maps, within what each may answer, and raise runs as them. The Slack
  app is trusted to say truthfully which Slack user clicked; the platform
  cannot check it. Keep that credential as you would those people's sign-ins,
  map only people who use that Slack workspace, and remove a mapping when
  someone leaves.
- **Lineage cannot be forged.** Only the platform's call path mints a tag with
  origin `agent`. A trigger's tag claiming it is dropped.
- **An agent calls only who its spec allows.** The caller's pinned revision
  must list the target in `access.spawn`, then depth, fan-out and budget are
  checked. The zero limits allow no calls.
- **Conversation keys are believed only from the API.** A run is placed in a
  thread, a branch or a project only when the platform's own API raised it. A
  trigger's tag carrying those keys is a conversation of its own.
- **Continuing a conversation checks the tenant.** `after` must name a run in
  the caller's tenant, or the request is a 404.
- **Preferences are the caller's own.** Those routes take no subject, so there
  is no way to name someone else.
- **A profile picture is what its bytes say it is.** Only PNG, JPEG and WebP are
  accepted, by signature, up to 512 KB. It is served with the type the server
  determined, `nosniff`, and a sandboxing content security policy. SVG and HTML
  are refused.
- **Reserved origins cannot be claimed.** A trigger's tag saying it came from
  the API, the platform, an eval, a person's run-now or an agent is dropped.
- **Housekeeping output is untrusted.** A title or a memory note is a model's
  words about text a person supplied. A title is cut to one line of 80
  characters. A note is one line of at most 300 characters, at most three per
  turn, stored as plain text that is shown on the agent and can be removed.
  A housekeeping run never starts another.
- **An agent's memory never displaces a person's note.** When an agent's notes
  are full, only notes the memory agent wrote make room.
- **A proposed memory is not a memory.** What the memory agent writes is held
  as pending and is not sent to any agent until a person approves it, unless
  the workspace turned approval off. This is saige's default for memory writes.
- **Memory refuses personal data.** Every note, from a person or an agent,
  passes saige's memory policy, which rejects text its detector finds personal
  data in.
- **Project memory reaches only that project.** A turn is sent a project's
  shared notes only when its conversation is filed in that project.
- **Evals are bounded.** A suite holds at most 50 cases, one run of a suite is
  open at a time, cases run one after another, and a case is given two minutes.
- **Only trusted origins name a schedule.** A run is tied to a schedule only
  when the cron trigger or the platform's own run-now raised it.
- **Queries are bounded.** Analytics and the journal take a window of at most
  30 days, a capped number of buckets and a capped number of rows.
- **Errors do not leak.** Unrecognized errors are logged and reported without
  detail. The identity provider's error text is never echoed.
- **Secrets come from the environment**, never from flags.
- **Every change is logged** with subject, kind, tenant, method and path.
  Denials are logged with the permission that was missing.

## Dev auth

`-auth=dev` makes every request an admin so the console can be previewed
without an identity provider. The server refuses to start in this mode on any
address that is not loopback. Never put it behind a proxy.

`-auth=mock` runs a development identity provider in process, so sign-in,
roles and machine tokens can be tried without a real one. Anyone who reaches
it can sign in as any persona, so it is loopback only: the server and the
provider must both listen on loopback, every redirect must be loopback, and
its issuer is a loopback `http` URL, which the verifier accepts for nothing
else. It logs a warning at start and its page says what it is. See
[authorization](authorization.md#sign-in-locally).

## Configure sign-in (Okta)

1. Create an **OIDC web application**. Sign-in redirect URI:
   `<public-url>/auth/callback`. Note the client ID and secret.
2. On the **authorization server** (for example `default`), set the audience
   to a value of your choice, such as `api://collegium`.
3. Add a **groups claim** named `groups` to both ID and access tokens, filtered
   to the platform's groups.
4. Create the groups `collegium-admins`, `collegium-operators` and `collegium-viewers`, and
   assign people.
5. For each machine, create an **API services application** and grant it only
   the `collegium:` scopes it needs. Add those scopes to the authorization server.

```sh
export COLLEGIUM_OIDC_ISSUER="https://<org>.okta.com/oauth2/default"
export COLLEGIUM_OIDC_CLIENT_ID="<web app client id>"
export COLLEGIUM_OIDC_CLIENT_SECRET="<from your secret store>"
export COLLEGIUM_OIDC_AUDIENCE="api://collegium"
collegium serve -public-url https://collegium.example.com
```

Any OpenID Connect provider works the same way. Group names, the groups claim
and a per-caller tenant claim are flags: see `serve -h`.

## Files agents make

- An agent has the file tools only if its spec names them. They act on one
  directory per conversation, opened as an `os.Root`, so no path a model
  writes leaves it: not `..`, not an absolute path, not a symbolic link.
- A published file's type comes from its bytes, not from its name or from
  what the agent said it was.
- A download checks the caller's tenant and `artifacts:read` on every
  request. It is served as an attachment of an opaque type with `nosniff` and
  a sandboxing content security policy. Only plain text, PNG, JPEG, GIF, WebP
  and PDF are ever shown inline, and only when asked. HTML and SVG never are:
  a file written from a stranger's text would otherwise run as this
  application.
- Deleting needs `artifacts:write`. Publishes, downloads, deletes and run
  exports are logged with who and when.
- The platform's own agents cannot be created or revised through the API.

## The inbox

Every inbox route asks only for `runs:read`. What a caller sees and may do is
decided per message:

| Kind | To see | To settle |
|------|--------|-----------|
| Alert, reached limit | `alerts:read` | `alerts:write` to acknowledge, `billing:write` to raise a limit |
| Failed run | `runs:read` | `runs:write` |
| Proposed memory | `agents:read` | `agents:write` |
| A question a run waits on (approval, choice, reply) | `runs:read`, and addressed to them | `runs:write`, and the runtime agrees they may answer |

A message a caller may not see answers 404, not 403, and is left out of their
count. A question is addressed to the people its run's pinned spec lets answer
it (`approvers` or `answerers`, by subject or `role:<name>`), and never to the
person who asked for an approval's run or its tree. Someone who can see the run
but may not answer is refused with 403 on `answerQuestion`, and nothing is
recorded. See [People in the loop](architecture.md#people-in-the-loop).

Addressing routes a question; it does not keep it confidential. The question,
its prompt and an approval's tool arguments are also on the run
(`GET /api/runs/{id}`, `question`), on its event stream (`lifecycle.parked`,
and `lifecycle.child.parked` on its caller's), and in its manifest, all
readable by anyone in the workspace with `runs:read`. Do not have an agent put
in a question what everyone who can read runs may not see.

Who may not approve: the person whose request raised the run, and the person
who started the tree it is in, by every name they go by: their subject, the
trigger's actor id, and for a run a Slack connector raised, the principal the
run's client maps that Slack user to. The platform checks this itself before
it asks the runtime, because mandatum's `AnswerRequest` takes no requester
alias. A GitHub connector's actor is a login no client maps, so a person
approving a call their own GitHub event raised is not detected. The right is checked before saying whether a part is still open. A
change is pushed to the browser as an id only, and only to callers who may see
that kind.

## Not built yet

- Limits are checked under a lock in one process, and the usage ledger is in
  memory. Two servers could both admit a run at the limit, and a restart
  forgets what was used. Spend limits are recorded but not enforced, because a
  run reports no cost.
- Sessions and schedules are in memory, so a restart signs everyone out.
- Knowledge is not yet used by runs. The runtime's retriever is not told which
  tenant or agent is asking, and wiring it before that changes would let one
  tenant's agent read another's documents.
- Memory written by the memory agent can carry whatever a conversation
  persuaded it to write, into later conversations. It is visible and removable,
  and off by default, but nothing reviews a note before it is used.
- Dictation uses the browser's speech recognition. In Chrome that sends audio
  to the browser vendor's service, not to this server.
- No connections. Nothing stores a third-party credential yet, and that should
  wait for an encrypted, durable store rather than be held in memory.
- No token revocation check beyond expiry, and no refresh. A session lasts 8
  hours.
- No rate limiting.
- No local accounts. Without an identity provider the only modes are dev auth
  and the mock provider, both on loopback.
- The audit trail is the server log, not a store.
- A call cannot yet narrow the callee's access to the caller's grant.
- Sign-in has been tested against in-process issuers, not a live Okta org.
