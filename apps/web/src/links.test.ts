import assert from "node:assert/strict";
import { test } from "node:test";
import type { Alert } from "./api.ts";
import { alertNamed, alertUrl, consoleUrl, inboxItemUrl } from "./links.ts";

test("item addresses: relative on the console, absolute under a public URL", () => {
  const cases: [string, string][] = [
    [inboxItemUrl("m-1"), "/?view=inbox&message=m-1"],
    [alertUrl("al-3"), "/?view=alerts&alert=al-3"],
    [inboxItemUrl("m 1&x"), "/?view=inbox&message=m+1%26x"],
    [alertUrl("al-3", "https://collegium.example.com"), "https://collegium.example.com/?view=alerts&alert=al-3"],
    [inboxItemUrl("m-1", "https://collegium.example.com//"), "https://collegium.example.com/?view=inbox&message=m-1"],
    [consoleUrl("alerts", {}, "https://c.example.com/console/"), "https://c.example.com/console/?view=alerts"],
  ];
  for (const [got, want] of cases) assert.equal(got, want);
});

test("alertNamed: only an alert in the person's own list is found", () => {
  const mine = [{ id: "al-1" }, { id: "al-2" }] as Alert[];
  const cases: [string | null, string | null][] = [
    ["al-2", "al-2"],
    ["al-99", null],
    ["", null],
    [null, null],
    ["al-1' or 1=1", null],
  ];
  for (const [id, want] of cases) assert.equal(alertNamed(mine, id)?.id ?? null, want, String(id));
});
