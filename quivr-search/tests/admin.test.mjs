// The Admin tab's rules (admin.mjs): how each step of a document is judged,
// what the KPIs and the per-hour chart count, and the documents stored per
// day. The facade routes are
// covered in server.test.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  createAdmin,
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
  // With no alert covering its corpus (the core says not_applicable), no
  // decision is coming: the step says so at once, not after a timer, and
  // nothing waits on it. An evaluated step keeps its duration.
  const unwatched = { ...quiet, evaluation: "not_applicable" };
  assert.equal(states(unwatched), "done done done done none");
  assert.equal(flow(unwatched, LIMIT, now)[4].reason, "no_alert");
  assert.equal(summarize([unwatched], now, LIMIT).waiting_by_step.alerts.count, 0);
  assert.equal(
    states({ ...cases[1].doc, evaluation: "not_applicable" }),
    "run todo todo todo none",
  );
  assert.equal(states({ ...quiet, evaluation: "applicable" }), "done done done done run");
  // A quarantine or a withdrawal stops the document first, as in its timeline.
  assert.equal(
    states({ ...cases[4].doc, evaluation: "not_applicable" }),
    "done error todo todo todo",
  );
  assert.equal(
    states({ ...cases[0].doc, evaluation: "not_applicable" }),
    "done slow done done slow",
  );
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

test("a version is replaced once a newer one took its place, not while it is still building", async () => {
  const version = (version_id, record_id, ago, state, is_current) => ({
    version_id,
    record_id,
    corpus_id: "demo",
    state,
    is_current,
    steps: { accepted_at: at(-ago) },
  });
  const docs = [
    // The core reports a Version still building as not current.
    version("v_building", "r1", 1_000, "received", false),
    version("v_shown", "r1", 60_000, "retrieval_ready", true),
    // Searchable, but a newer Version of its Record was desired meanwhile.
    version("v_finished", "r2", 50_000, "retrieval_ready", false),
    version("v_newer", "r2", 40_000, "retrieval_ready", true),
    // Overtaken while it waited.
    version("v_overtaken", "r3", 30_000, "received", false),
    version("v_latest", "r3", 20_000, "retrieval_ready", true),
  ];
  const upstream = async (path) => {
    const url = new URL(path, "http://core");
    const timeline = url.pathname.match(/^\/v0\/admin\/documents\/(\w+)\/timeline$/);
    if (timeline)
      return {
        status: 200,
        data: { document: docs.find((d) => d.version_id === timeline[1]), steps: [] },
      };
    if (url.pathname === "/v0/admin/documents")
      return { status: 200, data: { items: docs } };
    return { status: 404, data: { code: "not_found" } };
  };
  const admin = createAdmin({ upstream, corpus: "demo", clock: () => now });
  const { documents, stats } = await admin.snapshot();
  assert.deepEqual(
    Object.fromEntries(documents.map((d) => [d.version_id, d.replaced])),
    {
      v_building: false,
      v_shown: false,
      v_finished: true,
      v_newer: false,
      v_overtaken: true,
      v_latest: false,
    },
  );
  assert.equal(stats.waiting, 2, "the counts do not look at the label");
  for (const [id, replaced] of [
    ["v_building", false],
    ["v_finished", true],
  ])
    assert.equal(
      (await admin.timeline(id)).data.document.replaced,
      replaced,
      id,
    );
});

// The engine's count API over synthetic Records: one acceptance time each,
// or null for a Record without a current Version. Each count answers on a
// later turn, so reads overlap as they would against the core.
function counting(times) {
  const calls = [];
  const flight = { now: 0, max: 0 };
  const upstream = async (path) => {
    calls.push(path);
    flight.max = Math.max(flight.max, ++flight.now);
    await new Promise((resolve) => setImmediate(resolve));
    flight.now--;
    const url = new URL(path, "http://core");
    assert.equal(url.pathname, "/v0/records/count");
    assert.equal(url.searchParams.get("corpus_id"), "demo");
    const after = url.searchParams.get("accepted_after");
    const before = url.searchParams.get("accepted_before");
    const count = times.filter((t) =>
      after || before
        ? t !== null &&
          (!after || Date.parse(t) >= Date.parse(after)) &&
          (!before || Date.parse(t) < Date.parse(before))
        : true,
    ).length;
    return { status: 200, data: { count } };
  };
  return { upstream, calls, flight };
}
const history = (admin, tz) =>
  admin.history(new URL(`http://demo/demo/admin/history?tz=${tz}`));

test("documents are counted per local day since the first one, empty days at zero", async () => {
  const { upstream, calls } = counting([
    "2026-10-23T23:30:00+02:00",
    // Both on the day the clocks go back, a 25-hour day in Paris.
    "2026-10-25T00:30:00+02:00",
    "2026-10-25T23:30:00+01:00",
    "2026-10-27T10:00:00+01:00",
    null,
  ]);
  const clock = () => Date.parse("2026-10-27T12:00:00Z");
  const admin = createAdmin({ upstream, corpus: "demo", clock });
  const paris = await history(admin, "Europe/Paris");
  assert.deepEqual(paris, {
    time_zone: "Europe/Paris",
    total: 5,
    undated: 1,
    first_day: "2026-10-23",
    today: "2026-10-27",
    truncated: false,
    days: [
      { day: "2026-10-23", count: 1 },
      { day: "2026-10-24", count: 0 },
      { day: "2026-10-25", count: 2 },
      { day: "2026-10-26", count: 0 },
      { day: "2026-10-27", count: 1 },
    ],
  });
  assert.ok(
    calls.includes(
      "/v0/records/count?corpus_id=demo&accepted_after=2026-10-25T00%3A00%3A00%2B02%3A00&accepted_before=2026-10-26T00%3A00%3A00%2B01%3A00",
    ),
    "each bound carries its own offset",
  );
  const utc = await history(admin, "utc");
  assert.equal(utc.time_zone, "UTC");
  assert.deepEqual(
    utc.days.map((d) => d.count),
    [1, 1, 1, 0, 1],
  );
  await assert.rejects(history(admin, "Mars/Olympus"), { status: 422 });

  // Where the clocks skip midnight, the day starts at the change.
  const skipped = counting(["2026-09-06T12:00:00-03:00"]);
  await history(
    createAdmin({
      upstream: skipped.upstream,
      corpus: "demo",
      clock: () => Date.parse("2026-09-06T18:00:00Z"),
    }),
    "America/Santiago",
  );
  assert.ok(
    skipped.calls.includes(
      "/v0/records/count?corpus_id=demo&accepted_after=2026-09-06T01%3A00%3A00-03%3A00&accepted_before=2026-09-07T00%3A00%3A00-03%3A00",
    ),
    skipped.calls.join("\n"),
  );
});

test("past days are reread less often than today, and history is capped", async () => {
  let now = Date.parse("2026-10-27T12:00:00Z");
  const { upstream, calls, flight } = counting([
    "2026-10-20T10:00:00Z",
    "2026-10-27T10:00:00Z",
  ]);
  const admin = createAdmin({ upstream, corpus: "demo", clock: () => now });
  await history(admin, "UTC");
  assert.equal(flight.max, 4, "at most four counts in flight");
  calls.length = 0;
  now += 60_000;
  await history(admin, "UTC");
  assert.deepEqual(calls, [
    "/v0/records/count?corpus_id=demo",
    "/v0/records/count?corpus_id=demo&accepted_before=2026-10-28T00%3A00%3A00%2B00%3A00",
    "/v0/records/count?corpus_id=demo&accepted_after=2026-10-27T00%3A00%3A00%2B00%3A00&accepted_before=2026-10-28T00%3A00%3A00%2B00%3A00",
  ]);
  calls.length = 0;
  now += 15 * 60_000;
  await history(admin, "UTC");
  assert.ok(calls.length > 8, "every day is reread once the cache expires");

  const old = counting(["2025-01-01T10:00:00Z", "2026-10-27T10:00:00Z"]);
  const capped = await history(
    createAdmin({ upstream: old.upstream, corpus: "demo", clock: () => now }),
    "UTC",
  );
  assert.equal(capped.truncated, true);
  assert.equal(capped.days.length, 366);
  assert.equal(capped.days.at(-1).day, "2026-10-27");
  assert.ok(old.calls.length < 400, `${old.calls.length} calls`);

  // A day the core fails to count stops the days still waiting.
  const failing = counting(["2025-12-01T10:00:00Z"]);
  const broken = async (path) =>
    path.includes("accepted_after=2025-12-01")
      ? { status: 503, data: {} }
      : failing.upstream(path);
  await assert.rejects(
    history(createAdmin({ upstream: broken, corpus: "demo", clock: () => now }), "UTC"),
    { status: 503 },
  );
  // Let any count still queued reach the core before counting the reads.
  for (let turn = 0; turn < 3 || failing.flight.now; turn++)
    await new Promise((resolve) => setImmediate(resolve));
  const dayReads = failing.calls.filter((c) => c.includes("accepted_after"));
  assert.ok(dayReads.length < 10, `${dayReads.length} day reads after the failure`);
});
