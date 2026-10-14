// Package authz is a small policy language for deciding what a caller may do.
//
// A policy declares roles, which allow or deny actions on resources, and
// grants, which give roles to the identities whose verified claims match:
//
//	role viewer   { allow read on * }
//	role operator extends viewer { allow write on runs, schedules }
//	grant viewer   to anyone
//	grant operator to group "eng-oncall"
//
// # Pure
//
// Parse and Compile read text and nothing else. A compiled Policy answers two
// questions, (policy, identity) -> roles and (policy, roles) -> permissions,
// without I/O and without a clock. Every mistake in a policy is an error from
// Parse or Compile, never a surprise at decision time.
//
// # Default deny
//
// A permission is held only if some rule allows it and no rule denies it. A
// deny wins wherever it comes from: the role itself, a role it extends, a role
// that extends it, or another role the caller holds. The zero Identity matches
// no grant, and an unknown role grants nothing.
//
// This package knows nothing about the platform. The set of permissions that
// exist is passed to Compile, so the caller's list stays the only list.
package authz
