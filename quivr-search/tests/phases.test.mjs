import { test } from "node:test";
import assert from "node:assert/strict";
import { createPhaseWindow, parseServerTiming } from "../phases.mjs";

// The engine's Server-Timing header (literal, as it leaves the wire) becomes
// per-phase nearest-rank percentiles over the searches kept.
const nullMap = (entries) => Object.assign(Object.create(null), entries);

test("phases read the engine's Server-Timing and keep only the last searches", () => {
  const parsed = parseServerTiming("routing;dur=1.5, plugin;desc=x;dur=3, encode;dur=oops, cache, index;dur=-1");
  assert.equal(Object.getPrototypeOf(parsed), null);
  assert.deepEqual(parsed, nullMap({ routing: 1.5, plugin: 3 }));
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

test("parseServerTiming honors W3C quoted strings, case-insensitive dur, blank dur, and prototype-safe keys", () => {
  const parsed = parseServerTiming(
    [
      'blank;dur=',
      'spaces;dur="   "',
      'upper;DUR=2.5',
      'quoted;dur="3.25"',
      'zero;dur=0',
      'desc;desc="a, fake;dur=99; \\"still,inside\\";dur=88";dur=4.5',
      '__proto__;dur=1',
      'constructor;dur=2',
    ].join(", "),
  );
  assert.equal(Object.getPrototypeOf(parsed), null);
  assert.deepEqual(
    parsed,
    nullMap({
      upper: 2.5,
      quoted: 3.25,
      zero: 0,
      desc: 4.5,
      ["__proto__"]: 1,
      constructor: 2,
    }),
  );
  assert.equal(Object.prototype.dur, undefined);
  assert.equal(parsed.blank, undefined);
  assert.equal(parsed.spaces, undefined);
  assert.equal(parsed.fake, undefined);

  // Null-prototype metrics with constructor/__proto__ names summarize cleanly.
  const window = createPhaseWindow(5);
  window.record('__proto__;dur=1, constructor;dur=2');
  assert.deepEqual(window.summary(), {
    searches: 1,
    phases: [
      { phase: "__proto__", p50_ms: 1, p95_ms: 1 },
      { phase: "constructor", p50_ms: 2, p95_ms: 2 },
    ],
  });
});
