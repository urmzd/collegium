# Security

How the platform decides who is calling, what they may do, and what it refuses.
Report vulnerabilities as described in [SECURITY.md](../SECURITY.md).

## Callers

| Caller | How it authenticates | What it gets |
|--------|----------------------|--------------|
| A person in a browser | OpenID Connect authorization code flow with PKCE, run by the server. The browser holds an opaque session cookie and never a token. | Roles, from identity provider groups |
| A person in a CLI | An access token as `Authorization: Bearer` | Roles, from the token's groups claim |
| A machine | A client-credentials access token as `Authorization: Bearer` | Exactly the scopes on the token |

Every caller becomes a `Principal`, and every route asks it one question:
`Can(permission)`.

## Roles and scopes

| Role | Permissions |
|------|-------------|
| `viewer` | `agents:read`, `runs:read`, `schedules:read`, `alerts:read`, `knowledge:read` |
| `operator` | viewer, plus `runs:write`, `schedules:write`, `alerts:write` |
| `admin` | operator, plus `agents:write`, `knowledge:write` |

Writing knowledge is an admin permission because a document an agent is grounded
in changes what that agent says.

A machine has no role. It is issued scopes named `oap:<permission>`, for
example `oap:runs:write`. A scope without the `oap:` prefix, or one that names
no permission, grants nothing.

## Rules the code holds

- **Default deny.** The zero `Principal` can do nothing. An unmapped group, an
  unknown role and an unknown scope each grant nothing.
- **Tenant is never taken from a request.** It comes from the deployment or a
  verified token claim. A body that carries a `tenant` field is rejected.
- **Another tenant's objects do not exist.** Reads, deletes and calls across
  tenants return 404, never 403, so a response cannot confirm that something
  exists.
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
- **Lineage cannot be forged.** Only the platform's call path mints a tag with
  origin `agent`. A trigger's tag claiming it is dropped.
- **An agent calls only who its spec allows.** The caller's pinned revision
  must list the target in `access.spawn`, then depth, fan-out and budget are
  checked. The zero limits allow no calls.
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

## Configure sign-in (Okta)

1. Create an **OIDC web application**. Sign-in redirect URI:
   `<public-url>/auth/callback`. Note the client ID and secret.
2. On the **authorization server** (for example `default`), set the audience
   to a value of your choice, such as `api://oap`.
3. Add a **groups claim** named `groups` to both ID and access tokens, filtered
   to the platform's groups.
4. Create the groups `oap-admins`, `oap-operators` and `oap-viewers`, and
   assign people.
5. For each machine, create an **API services application** and grant it only
   the `oap:` scopes it needs. Add those scopes to the authorization server.

```sh
export OAP_OIDC_ISSUER="https://<org>.okta.com/oauth2/default"
export OAP_OIDC_CLIENT_ID="<web app client id>"
export OAP_OIDC_CLIENT_SECRET="<from your secret store>"
export OAP_OIDC_AUDIENCE="api://oap"
open-agent-platform serve -public-url https://oap.example.com
```

Any OpenID Connect provider works the same way. Group names, the groups claim
and a per-caller tenant claim are flags: see `serve -h`.

## Not built yet

- Sessions and schedules are in memory, so a restart signs everyone out.
- Knowledge is not yet used by runs. The runtime's retriever is not told which
  tenant or agent is asking, and wiring it before that changes would let one
  tenant's agent read another's documents.
- No connections. Nothing stores a third-party credential yet, and that should
  wait for an encrypted, durable store rather than be held in memory.
- No token revocation check beyond expiry, and no refresh. A session lasts 8
  hours.
- No rate limiting.
- The audit trail is the server log, not a store.
- A call cannot yet narrow the callee's access to the caller's grant.
- Sign-in has been tested against an in-process issuer, not a live Okta org.
