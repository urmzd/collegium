# Security Policy

## Reporting a Vulnerability

Report security issues privately through
[GitHub Security Advisories](https://github.com/urmzd/collegium/security/advisories/new)
rather than in an issue. Please include a description, the affected commit, and
a reproduction if you have one.

## Supported versions

| Version | Supported |
|---------|-----------|
| 0.x     | Yes       |

## Security boundaries

The rules the code holds are listed in [docs/security.md](docs/security.md).
A change to `pkg/auth`, `pkg/call`, `internal/httpapi/session.go` or the
tenant checks in `internal/httpapi` needs tests proving those rules still hold.
