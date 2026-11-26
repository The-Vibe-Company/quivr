import { test } from "node:test";
import assert from "node:assert/strict";
import { createPhaseWindow, parseServerTiming } from "../phases.mjs";

// The engine's Server-Timing header (literal, as it leaves the wire) becomes
// per-phase nearest-rank percentiles over the searches kept.
test("phases read the engine's Server-Timing and keep only the last searches", () => {
  assert.deepEqual(
    parseServerTiming("routing;dur=1.5, plugin;desc=x;dur=3, encode;dur=oops, cache, index;dur=-1"),
    { routing: 1.5, plugin: 3 },
  );
  const window = createPhaseWindow(20);
  window.record("routing;dur=999");
  for (let i = 1; i <= 20; i++) window.record(`routing;dur=${i}, hydrate;dur=${i * 2}`);
  window.record(null);
  assert.deepEqual(window.summary(), {
    searches: 20,
    phases: [
      { phase: "routing", p50_ms: 10, p95_ms: 19 },
      { phase: "hydrate", p50_ms: 20, p95_ms: 38 },
    ],
  });
  assert.deepEqual(createPhaseWindow().summary(), { searches: 0, phases: [] });
});
