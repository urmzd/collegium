import assert from "node:assert/strict";
import { test } from "node:test";
import type { AgentSpec } from "./api.ts";
import { approvable, namesOf, withApproval, withAsking, withNames } from "./people.ts";

const spec: AgentSpec = {
  name: "deployer",
  model: "offline",
  provider: "offline",
  system_prompt: "x",
  tools: ["file_write", "ask_person"],
  access: { spawn: ["helper"] },
};

test("what can wait for approval is what the agent holds, and calls to the agents it may call", () => {
  assert.deepEqual(approvable(spec), ["file_write", "tag_helper"]);
});

test("approvals, asking and who may answer are written to the spec, and left out when empty", () => {
  const on = withApproval(spec, "file_write", true);
  assert.deepEqual(on.approvals, ["file_write"]);
  assert.equal(withApproval(on, "file_write", false).approvals, undefined);
  assert.deepEqual(withAsking({ ...spec, tools: [] }, true).tools, ["ask_person"]);
  assert.deepEqual(withAsking(spec, false).tools, ["file_write"]);
  assert.deepEqual(withNames(spec, "approvers", "ana, role:operator\nana").approvers, ["ana", "role:operator"]);
  assert.equal(withNames(spec, "answerers", " , ").answerers, undefined);
  assert.deepEqual(namesOf("a,\n b"), ["a", "b"]);
});
