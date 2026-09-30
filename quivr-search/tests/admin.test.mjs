// The Admin tab's rules (admin.mjs): how each step of a document is judged
// and what the KPIs and the per-hour chart count. The facade routes are
// covered in server.test.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  flow,
  fromRollups,
  limits,
  summarize,
  withRollups,
} from "../admin.mjs";

const now = Date.parse("2026-09-30T12:30:00Z");
const at = (ms) => new Date(now + ms).toISOString();
const doc = (state, steps) => ({ version_id: "v", state, steps });
const LIMIT = {
  received: 250,
  cut: 250,
  searchable: 250,
  vectors: 1000,
  alerts: 1000,
};
const states = (d) =>
  flow(d, LIMIT, now)
    .map((c) => c.state)
    .join(" ");

test("each step is done, slow, running, to do, not recorded or stopped by a quarantine", () => {
  const cases = [
    {
      name: "a document through every step, the cut above its limit",
      doc: doc("retrieval_ready", {
        accepted_at: at(-5000),
        materialized_at: at(-4900),
        segmented_at: at(-4000),
        retrieval_ready_at: at(-3900),
        enriched_at: at(-3000),
        evaluated_at: at(-2000),
      }),
      want: "done slow done done slow",
    },
    {
      name: "just received: reading runs, the rest waits",
      doc: doc("received", { accepted_at: at(-300) }),
      want: "run todo todo todo todo",
    },
    {
      name: "searchable: vectors and alert decisions run side by side",
      doc: doc("retrieval_ready", {
        accepted_at: at(-900),
        materialized_at: at(-800),
        segmented_at: at(-700),
        retrieval_ready_at: at(-600),
      }),
      want: "done done done run run",
    },
    {
      name: "no alert decided long after searchable: not recorded",
      doc: doc("retrieval_ready", {
        accepted_at: at(-600_000),
        materialized_at: at(-599_900),
        segmented_at: at(-599_800),
        retrieval_ready_at: at(-599_700),
        enriched_at: at(-599_000),
      }),
      want: "done done done done none",
    },
    {
      name: "quarantined while cut: that step failed, later ones never came",
      doc: doc("quarantined", {
        accepted_at: at(-900),
        materialized_at: at(-800),
        quarantined_at: at(-500),
      }),
      want: "done error todo todo todo",
    },
    {
      name: "a later step without this one's time (history)",
      doc: doc("retrieval_ready", {
        accepted_at: at(-900),
        retrieval_ready_at: at(-100),
      }),
      want: "none none slow run run",
    },
  ];
  for (const c of cases) assert.equal(states(c.doc), c.want, c.name);
  // Without an alert to decide, a searchable document is not late while the
  // decision may still come.
  const quiet = doc("retrieval_ready", {
    accepted_at: at(-30_300),
    materialized_at: at(-30_200),
    segmented_at: at(-30_100),
    retrieval_ready_at: at(-30_000),
    enriched_at: at(-29_000),
  });
  assert.equal(states(quiet), "done done done done run");
  assert.equal(summarize([quiet], now, LIMIT).stuck, 0);
  const running = flow(cases[1].doc, LIMIT, now)[0];
  assert.equal(running.since, at(-300), "a running step waits from its cause");
  assert.equal(flow(cases[0].doc, LIMIT, now)[1].ms, 900);
});

test("a step is slow above its own p95 over the day, never below its floor", () => {
  const cut = (ms) =>
    doc("retrieval_ready", {
      accepted_at: at(-10_000),
      materialized_at: at(-9000),
      segmented_at: at(-9000 + ms),
    });
  // 20 cuts of 100..2000 ms: the p95 is the 19th.
  const many = Array.from({ length: 20 }, (_, i) => cut((i + 1) * 100));
  assert.equal(limits(many).cut, 1900);
  // Too few to trust a p95: three times the median.
  const five = [200, 300, 400, 500, 600].map(cut);
  assert.equal(limits(five).cut, 1200);
  // Fast steps keep the floor, so 90 ms beside 80 ms ones is not slow.
  assert.equal(limits(Array.from({ length: 20 }, () => cut(80))).cut, 250);
  // No history yet (a fresh corpus): nothing is called slow.
  const first = limits([cut(1600)]);
  assert.equal(first.cut, null);
  assert.equal(
    flow(cut(1600), first, now)[1].state,
    "done",
    "a first document is not slower than a usual that does not exist",
  );
});

test("the KPIs count the last ten minutes, the last hour and each hour of the day", () => {
  const docs = [
    // Searchable in 1 s, 2 s and 4 s within the hour; one waits past its limit.
    doc("retrieval_ready", {
      accepted_at: at(-60_000),
      materialized_at: at(-59_900),
      segmented_at: at(-59_500),
      retrieval_ready_at: at(-59_000),
      enriched_at: at(-58_000),
      evaluated_at: at(-58_000),
    }),
    doc("retrieval_ready", {
      accepted_at: at(-120_000),
      materialized_at: at(-119_900),
      segmented_at: at(-119_500),
      retrieval_ready_at: at(-118_000),
      enriched_at: at(-117_000),
      evaluated_at: at(-117_000),
    }),
    doc("retrieval_ready", {
      accepted_at: at(-1_800_000),
      materialized_at: at(-1_799_900),
      segmented_at: at(-1_799_500),
      retrieval_ready_at: at(-1_796_000),
      enriched_at: at(-1_795_000),
      evaluated_at: at(-1_795_000),
    }),
    doc("materialized", {
      accepted_at: at(-20_000),
      materialized_at: at(-19_900),
    }),
    doc("quarantined", {
      accepted_at: at(-40_000),
      materialized_at: at(-39_900),
      quarantined_at: at(-39_000),
    }),
    // Yesterday afternoon: counted in its hour, not in the last hour.
    doc("retrieval_ready", {
      accepted_at: at(-20 * 3_600_000),
      retrieval_ready_at: at(-20 * 3_600_000 + 30_000),
    }),
  ];
  const stats = summarize(docs, now, LIMIT);
  assert.equal(stats.per_minute, 0.4, "4 documents in 10 minutes");
  assert.equal(stats.searchable_p95_ms, 4000);
  assert.equal(stats.searchable_day_p95_ms, 30_000);
  assert.equal(stats.waiting, 1);
  assert.equal(
    stats.stuck,
    1,
    "the document cut 20 s ago still waits to be searchable",
  );
  assert.deepEqual(stats.waiting_by_step, {
    received: { count: 0 },
    cut: { count: 1, oldest_since: at(-19_900) },
    searchable: { count: 0 },
    vectors: { count: 0 },
    alerts: { count: 0 },
  });
  assert.equal(stats.errors, 1);
  assert.equal(stats.hours.length, 24);
  assert.equal(stats.hours.at(-1).start, "2026-09-30T12:00:00.000Z");
  assert.deepEqual(
    stats.hours.map((h) => [h.count, h.errors]).filter(([n]) => n),
    [
      [1, 0],
      [5, 1],
    ],
  );
  assert.equal(stats.counted_from, undefined);
  assert.equal(
    summarize(docs, now, LIMIT, now - 3_600_000).counted_from,
    "2026-09-30T11:30:00.000Z",
  );
});

test("with the engine's rollups, sparse buckets add up per hour", () => {
  const list = (items) => ({
    window: "24h",
    resolution_seconds: 900,
    from: at(-86_400_000),
    to: at(0),
    items,
  });
  const point = (ms, count, errors = 0) => ({
    start: at(ms),
    count,
    errors,
    p50_ms: 1,
    p95_ms: 1,
  });
  const hour = list([
    {
      step: "accepted_to_searchable",
      summary: { count: 90, errors: 0, p95_ms: 1800 },
      points: [],
    },
  ]);
  const day = list([
    {
      step: "accepted_to_searchable",
      summary: { count: 12, errors: 0, p95_ms: 2400 },
      // 12:00 and 12:15 share an hour; 09:45 has its own.
      points: [point(-1_800_000, 5), point(-900_000, 4), point(-9_900_000, 3)],
    },
    {
      step: "baseline",
      summary: { count: 12, errors: 2 },
      points: [point(-900_000, 4, 2)],
    },
  ]);
  const stats = fromRollups(hour, day, now);
  assert.equal(stats.per_minute, 1.5, "90 made searchable in an hour");
  assert.equal(stats.searchable_p95_ms, 1800);
  assert.equal(stats.searchable_day_p95_ms, 2400);
  assert.deepEqual(
    stats.hours
      .map((h) => [h.start.slice(11, 16), h.count, h.errors])
      .filter(([, n]) => n),
    [
      ["09:00", 3, 0],
      ["12:00", 9, 2],
    ],
  );
});

test("the header prefers the rollups, but not over a document they do not count yet", () => {
  const own = {
    per_minute: 0.1,
    searchable_p95_ms: 2200,
    searchable_day_p95_ms: 2200,
    waiting: 0,
    hours: [],
    counted_from: at(0),
  };
  const counted = {
    per_minute: 1.5,
    per_minute_window: "1h",
    searchable_p95_ms: 1800,
    searchable_day_p95_ms: 2400,
    hours: [],
    hours_of: "searchable",
  };
  assert.deepEqual(
    withRollups(own, counted),
    { ...counted, waiting: 0 },
    "the rollups win, and a capped scan's limit no longer applies",
  );
  // The rollups have not flushed the document the flow already shows.
  const lagging = {
    ...counted,
    per_minute: 0,
    searchable_p95_ms: null,
    searchable_day_p95_ms: null,
  };
  const merged = withRollups(own, lagging);
  assert.equal(merged.per_minute, 0.1);
  assert.equal(merged.per_minute_window, "10min");
  assert.equal(merged.searchable_p95_ms, 2200);
  assert.equal(withRollups(own, null).hours_of, "received");
});
