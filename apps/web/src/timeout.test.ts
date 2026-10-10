import assert from "node:assert/strict";
import { test } from "node:test";
import { REQUEST_LIMIT, within } from "./timeout.ts";

test("within: an answer in time is passed through, and so is a failure", async () => {
  assert.equal(await within(Promise.resolve(7), 50), 7);
  await assert.rejects(within(Promise.reject(new Error("no")), 50), /no/);
});

test("within: a request that never answers settles as a failure a person can retry", async () => {
  const started = Date.now();
  await assert.rejects(within(new Promise(() => {}), 30), /took too long/);
  assert.ok(Date.now() - started < 500);
  assert.ok(REQUEST_LIMIT >= 5_000);
});
