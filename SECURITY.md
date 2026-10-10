# Security Policy

## Reporting a Vulnerability

Report security issues privately through
[GitHub Security Advisories](https://github.com/urmzd/open-agent-platform/security/advisories/new)
rather than in an issue. Please include a description, the affected commit, and
a reproduction if you have one.

## Supported versions

| Version | Supported |
|---------|-----------|
| 0.x     | Yes       |

## Security boundaries

### `pkg/call`

The edge between two runs carries the budget lease and the tree a run belongs
to. It travels in `Tag.Meta`, which triggers also write, so:

- `EdgeOf` honors lineage only when `Tag.Origin` is `agent`. Every other tag is
  a root, whatever its `Meta` claims.
- The server must set `Origin` itself and never accept it from the wire, the
  same way opentag sets `Tenant`.
- An agent tag with a missing or malformed edge is an error, not a root.
- The zero `Limits` allows no calls.

A change to `pkg/call` needs tests proving these still hold.
