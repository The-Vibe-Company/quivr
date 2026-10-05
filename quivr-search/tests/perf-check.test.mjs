// The rules perf/measure.mjs applies to its numbers (perf/check.mjs).
import { test } from "node:test";
import assert from "node:assert/strict";
import { breaches, worstStatus } from "../perf/check.mjs";

const budgets = {
  pages: { lcp_ms: 2000, cls: 0.1, js_kb: 130 },
  inp_ms: 200,
  endpoints: { "GET /demo/feed": 150 },
};
const page = { lcp_ms: 900, cls: 0.02, js_kb: 100 };
const report = (over = {}) => ({
  pages: { Fil: page },
  inp_ms: 150,
  endpoints: { "GET /demo/feed": { status: 200, p95_ms: 40 } },
  ...over,
});

test("a refusal among successful samples is the endpoint's status", () => {
  assert.equal(worstStatus([200, 200, 503, 200]), 503);
  assert.equal(worstStatus([200, 304, 200]), 304);
  assert.equal(worstStatus([200, 404, 503]), 404);
});

test("numbers within every budget break none", () => {
  assert.deepEqual(breaches(report(), budgets, { local: true }), []);
});

test("each page, INP and endpoint limit is checked; endpoint times only on the local stack", () => {
  const slow = report({
    pages: { Fil: { lcp_ms: 2500, cls: 0.2, js_kb: 140 } },
    inp_ms: 250,
    endpoints: { "GET /demo/feed": { status: 200, p95_ms: 400 } },
  });
  assert.deepEqual(breaches(slow, budgets, { local: true }), [
    "Fil: LCP 2500 ms > 2000",
    "Fil: CLS 0.2 > 0.1",
    "Fil: JavaScript 140 KB > 130",
    "INP 250 ms > 200",
    "GET /demo/feed: p95 400 ms > 150",
  ]);
  assert.ok(!breaches(slow, budgets, { local: false }).some((b) => b.includes("p95")));
});

test("an endpoint answering an error fails the run wherever it runs, budgeted or not", () => {
  const failing = report({
    endpoints: {
      "GET /demo/feed": { status: 503, p95_ms: 5 },
      "POST /demo/alerts/preview": { status: 504, p95_ms: 5 },
    },
  });
  assert.deepEqual(breaches(failing, budgets, { local: false }), [
    "GET /demo/feed: HTTP 503",
    "POST /demo/alerts/preview: HTTP 504",
  ]);
});
