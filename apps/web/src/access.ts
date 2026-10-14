/**
 * What a person may do, and why, in words. Pure: the platform decides, this
 * only phrases the decision for the home page and the Access panel.
 */
import type { HomeAccess } from "@vultus/core";
import type { AccessDecision, Me } from "./api.ts";

/** Every permission, in the order the platform lists them. */
export const PERMISSIONS = [
  "agents:read",
  "agents:write",
  "runs:read",
  "runs:write",
  "schedules:read",
  "schedules:write",
  "alerts:read",
  "alerts:write",
  "knowledge:read",
  "knowledge:write",
  "evals:read",
  "evals:write",
  "billing:read",
  "billing:write",
  "artifacts:read",
  "artifacts:write",
  "clients:read",
  "clients:write",
] as const;

/** What each permission lets a person do, after "can". */
export const doing: Record<string, string> = {
  "agents:read": "see agents",
  "agents:write": "change an agent",
  "runs:read": "see runs and conversations",
  "runs:write": "run agents",
  "schedules:read": "see schedules",
  "schedules:write": "manage schedules",
  "alerts:read": "see alerts",
  "alerts:write": "acknowledge alerts and set their rules",
  "knowledge:read": "search what agents know",
  "knowledge:write": "change what agents know",
  "evals:read": "see evals",
  "evals:write": "run and change evals",
  "billing:read": "see limits and usage",
  "billing:write": "set limits",
  "artifacts:read": "see stored files",
  "artifacts:write": "delete stored files",
  "clients:read": "see connections",
  "clients:write": "manage connections",
};

// The changes worth naming on the home page, most telling first. A viewer
// is told they cannot run agents; an operator, that they cannot change one.
const NOTABLE = ["runs:write", "agents:write", "schedules:write", "knowledge:write", "billing:write", "clients:write"];
const DENY_WORDS: Record<string, string> = { "agents:write": "change what an agent is" };

/** "a, b and c". */
export function listed(items: string[]): string {
  if (items.length <= 1) return items[0] ?? "";
  return `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}

/** The permission "Why can I do this?" explains: something the person may change, or failing that, read. */
export function explainedPermission(me: Me): string {
  const held = new Set(me.permissions ?? []);
  return NOTABLE.find((p) => held.has(p)) ?? PERMISSIONS.find((p) => held.has(p)) ?? "runs:read";
}

/** A console link that opens the Access panel on one permission. */
export function explainUrl(permission: string): string {
  return `/?${new URLSearchParams({ view: "access", permission })}`;
}

/**
 * The home page's access block. The role is the one that decided the
 * explained permission, so the group that earned it is the right one to name.
 */
export function accessOf(me: Me, decision: AccessDecision | null, policyUrl?: string): HomeAccess {
  const who = me.principal;
  const held = new Set(me.permissions ?? []);
  const permission = decision?.permission ?? explainedPermission(me);
  const role = (decision?.allowed ? decision.role : undefined) || who.roles?.[0];
  const group = decision?.held.find((h) => h.role === role)?.group;
  const writes = NOTABLE.filter((p) => held.has(p)).map((p) => doing[p] as string);
  const everything = PERMISSIONS.every((p) => held.has(p));
  const reads = PERMISSIONS.filter((p) => p.endsWith(":read"));
  const allows = everything
    ? "do everything in this workspace"
    : writes.length > 0
      ? listed(writes.slice(0, 2))
      : reads.every((p) => held.has(p))
        ? "see everything in this workspace"
        : held.size > 0
          ? listed(reads.filter((p) => held.has(p)).map((p) => doing[p] as string).slice(0, 2))
          : undefined;
  const missing = NOTABLE.find((p) => !held.has(p));
  return {
    ...(role ? { role } : {}),
    ...(group ? { group } : {}),
    ...(who.subject === "dev" ? {} : { via: "your company's identity provider" }),
    ...(allows && role ? { allows } : {}),
    ...(missing && role ? { denies: DENY_WORDS[missing] ?? doing[missing] } : {}),
    explainUrl: explainUrl(permission),
    ...(policyUrl ? { policyUrl } : {}),
  };
}

/** One line of the explanation: a label and what it says. `code` lines are policy text. */
export interface Step {
  label: string;
  text: string;
  code?: boolean;
}

/** "write on agents" from "agents:write". */
export function asked(permission: string): string {
  const [resource, action] = permission.split(":");
  return `${action} on ${resource}`;
}

/**
 * The explanation, as the Access board lays it out: a question, a one-line
 * answer, then each step from sign-in to the rule.
 */
export function explained(d: AccessDecision, name: string): { question: string; answer: string; steps: Step[]; fix?: string } {
  const first = name.split(/\s+/)[0] || "you";
  const question = `Can ${first} ${doing[d.permission] ?? asked(d.permission)}?`;
  if (d.outcome === "scope") {
    return {
      question,
      answer: "Yes. The token this caller signed in with carries the scope for it.",
      steps: [{ label: "Asked for", text: asked(d.permission), code: true }],
    };
  }
  const roles = d.held.map((h) => h.role);
  const steps: Step[] = [
    { label: "Signed in", text: roles.length > 0 ? `${name}, holding ${listed(roles)}` : `${name}, holding no role` },
  ];
  const grant = d.grant ?? d.held.find((h) => h.role === d.role)?.grant;
  if (d.outcome === "no match") {
    for (const h of d.held) if (h.grant) steps.push({ label: "Granted", text: h.grant, code: true });
    steps.push({
      label: "Asked for",
      text: `${asked(d.permission)}, which no rule ${roles.length > 0 ? "in these roles " : ""}covers`,
    });
    return {
      question,
      answer: roles.length > 0 ? "No. Nothing in the policy allows it." : "No. No grant gives you a role yet.",
      steps,
      fix: roles.length > 0 ? "An admin can grant it by adding a rule to the access policy." : "An admin can add a grant to the access policy.",
    };
  }
  if (grant) steps.push({ label: "Granted", text: grant, code: true });
  if (d.path && d.path.length > 1) steps.push({ label: "Through", text: d.path.join(" extends "), code: true });
  if (d.rule) steps.push({ label: d.allowed ? "Allows" : "Denies", text: d.rule, code: true });
  steps.push({ label: "Asked for", text: `${asked(d.permission)}, which the rule above ${d.allowed ? "allows" : "denies"}` });
  return d.allowed
    ? { question, answer: `Yes. You are ${/^[aeiou]/i.test(d.role ?? "") ? "an" : "a"} ${d.role}, and the ${d.role} role allows it.`, steps }
    : {
        question,
        answer: "No. A rule denies it, and a deny beats every allow.",
        steps,
        fix: "An admin can change the rule in the access policy.",
      };
}
