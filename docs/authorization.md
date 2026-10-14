# Authorization

Sign-in establishes who a caller is. A policy decides what they may do. The
policy maps verified claims to roles, and roles to permissions, and every
`Principal.Can` is answered by evaluating it. The language lives in
`pkg/authz`; `pkg/auth` supplies the permissions and applies it.

## Grammar

```ebnf
policy    = { statement } ;
statement = role | grant ;
role      = "role" IDENT [ "extends" IDENT { "," IDENT } ] "{" { rule } "}" ;
rule      = ( "allow" | "deny" ) actions "on" resources ;
actions   = "*" | IDENT { "," IDENT } ;          (* read, write *)
resources = "*" | IDENT { "," IDENT } ;          (* agents, runs, schedules, ... *)
grant     = "grant" IDENT { "," IDENT } "to" subject ;
subject   = "group" STRING
          | "subject" STRING
          | "email" "domain" STRING
          | "claim" ( IDENT | STRING ) "=" STRING
          | "anyone" ;

IDENT     = letter_or_underscore { letter | digit | "_" | "-" } ;
STRING    = '"' { any character but '"', "\" or a newline | '\"' | "\\" } '"' ;
```

`#` starts a comment to the end of the line. The words of the language are
reserved and cannot name a role, action or resource. A claim's name can be any
word, or a string for a name such as `"https://example.com/roles"`.

## Semantics

- **Default deny.** A permission is held only if a rule allows it.
- **Deny wins.** A deny beats every allow, wherever either comes from: the same
  role, a role it extends, a role that extends it, or another role held.
- **Inheritance.** A role holds its own rules and every rule of the roles it
  extends, transitively.
- **Grants.** A person holds every role of every grant whose subject matches
  their verified claims. `group` matches one entry of the groups claim.
  `subject` matches `sub`. `email domain` matches the part after `@`, ignoring
  case, and only when `email_verified` is `true`. `claim` matches a claim
  whose value is exactly that string; a missing claim, a list, a number or a
  boolean never matches. `anyone` matches any person with a subject and a
  tenant, never the zero `Principal`.
- **Machines are not decided by the policy.** A machine gets exactly the scopes
  on its token. It holds no roles, so no grant, `anyone` included, widens it.
- **Errors at load time.** A syntax error, an unknown role in `extends` or a
  grant, a cycle, a role declared twice, an unknown action or resource, or a
  policy with no roles stops the server from starting. Actions and resources
  are those of `auth.Permissions()`. A policy is limited to 256 roles, 4096
  rules, 4096 grants and 32 levels of inheritance.

## Example

```
role viewer   { allow read on * }
role operator extends viewer {
  allow write on runs, schedules, alerts, evals, artifacts
}
role admin    { allow * on * }
role auditor  extends viewer { deny read on billing }

grant viewer   to anyone
grant operator to group "eng-oncall"
grant admin    to group "platform-admins"
grant auditor  to claim department = "finance"
```

Someone in `platform-admins` whose `department` is `finance` holds viewer,
admin and auditor, and cannot read billing: the auditor's deny wins.

## Configure

```sh
export COLLEGIUM_AUTHZ_POLICY=/etc/collegium/policy.authz
collegium serve ...
```

Without it, the built-in policy (`pkg/auth/default.authz`) declares `viewer`,
`operator` and `admin` with exactly the permissions in
[security](security.md#roles-and-scopes), and grants nothing itself. The
`-admin-group`, `-operator-group` and `-viewer-group` flags become grants of
those roles. With a policy file, the file decides, and a group flag is added
as a grant only if it is set on the command line.

## Debug with Explain

`authz.Policy.Explain(identity, permission)` returns the outcome (`allowed`,
`denied` or `no match`), the rule that decided, the role it reached the caller
through, the grant that gave that role, and every role held.
`auth.Policy.Explain(principal, permission)` does the same for a signed-in
`Principal`. A sign-in keeps the person's roles and, for each, the grant of
the policy that gave it, so the explanation names the grant. It does not keep
the claims or the token. A `Principal` made any other way names the rule and
not the grant.

A signed-in person asks about themselves with
`GET /api/access/explain?permission=agents:write`. It takes a permission and
no subject, so nobody can ask about someone else, and any signed-in caller may
call it. The console shows the answer under Access, reached from "Why can I do
this?" on the home page.

From the command line, with the same environment and flags as `serve`:

```sh
collegium authz check policy.authz
COLLEGIUM_AUTHZ_POLICY=policy.authz \
  collegium authz explain -claims id-token-claims.json billing:read
```

```
billing:read: denied
  rule:  deny read on billing (policy.authz:6:32)
  role:  auditor
  grant: grant auditor to claim department = "finance" (policy.authz:11:1)
  a deny overrides every allow
  holds: viewer (anyone), admin (group "platform-admins"), auditor (claim department = "finance")
```

`-claims` is a JSON file of a token's claims, such as a decoded ID token's
payload.

## Sign in locally

`make dev-signin` runs the server with `-auth=mock`: a development identity
provider on `127.0.0.1:8081` with a page of personas and no passwords. Sign-in
goes through the same flow, verifier and policy as a real provider. The
built-in personas are a viewer, an operator, an admin and someone in no
mapped group, and a machine `mock-ci` (secret `mock-ci-secret`) with
`collegium:runs:read` and `collegium:runs:write`:

```sh
curl -s -u mock-ci:mock-ci-secret -d grant_type=client_credentials http://127.0.0.1:8081/token
```

`-mock-personas` reads others from a JSON file of
`{"people": [...], "machines": [...]}`, with the fields of `mockidp.Person`
and `mockidp.Machine`. The provider refuses to start unless selected with
`-auth=mock`, refuses any address but `127.0.0.1` or `localhost`, refuses a
redirect that is not loopback, and the server itself must listen on loopback.
