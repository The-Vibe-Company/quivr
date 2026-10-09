import assert from "node:assert/strict";
import test from "node:test";
import { countFacets, createExplorer, periodBounds } from "../explore.mjs";

// How the Explorer counts a date (THE-1184): the step of its histogram and
// the filters each count keeps, against a fake engine that answers each
// interval with the buckets given.
const date = { name: "metadata.published_at", type: "datetime" };
const language = { name: "metadata.language", type: "string" };

// The facade owns relaying the engine's marker, including an empty result,
// and translating a remaining vector refusal without calling it invalid input.
test("Explorer search preserves degradation and explains unsupported_search", async () => {
  for (const response of [
    { status: 200, data: { items: [], retrieval_profile: { name: "default", version: "v", degraded: [{ reason: "vectors_unavailable", corpus_ids: ["corpus_a"] }] } } },
    { status: 200, data: { items: [], retrieval_profile: { name: "default", version: "v" } } },
    { status: 422, data: { code: "unsupported_search" } },
  ]) {
    const explorer = createExplorer({
      upstream: async (path) => path === "/v0/search" ? response : { status: 200, data: { name: "Example corpus" } },
      picked: async () => ["corpus_a"],
      demo: () => "corpus_a",
    });
    if (response.status === 422) {
      await assert.rejects(explorer.page(new URLSearchParams({ q: "harbour" })), (error) =>
        error.code === "unsupported_search" && error.message === "La recherche par sens est indisponible. Réessayez par mots-clés.");
    } else {
      const page = await explorer.page(new URLSearchParams({ q: "harbour" }));
      assert.deepEqual(page.retrieval_profile, response.data.retrieval_profile);
      assert.deepEqual(page.items, []);
    }
  }
});

async function counted(predicates, buckets, timeline) {
  const sent = [];
  const out = await countFacets({
    ids: ["c1"],
    fields: [language, date],
    predicates,
    timeline,
    count: async (body) => {
      sent.push(body);
      return {
        items: body.fields.map((f) => ({ field: f.field, buckets: f.interval ? buckets[f.interval] || [] : [] })),
      };
    },
  });
  const histogram = out.fields.find((f) => f.field === date.name);
  // Each request: the date's step in it, and the filters it kept.
  const asked = sent.map((b) => [
    b.fields.find((f) => f.field === date.name)?.interval,
    (b.filter?.metadata || []).map((p) => `${p.field.slice(9)} ${p.gte || ""}`.trim()),
  ]);
  return {
    interval: histogram.interval,
    values: histogram.values.map((v) => `${v.value} ${v.count}`),
    asked,
    ...(out.total === undefined ? {} : { total: out.total }),
  };
}

const at = (value, count = 1) => ({ value, count });
// n months from January of a year, the i-th counting i + 1 documents.
const months = (first, n) =>
  Array.from({ length: n }, (_, i) => {
    const d = new Date(Date.UTC(first, i, 1));
    return at(d.toISOString().replace(".000", ""), i + 1);
  });
const picked = (period) => ({ field: date.name, ...periodBounds(period) });
const en = { field: language.name, any_of: ["en"] };

test("a date's histogram steps by the period picked, or by the span of its documents", async () => {
  const cases = [
    {
      name: "unpicked, within two months: counted again by day",
      predicates: [],
      buckets: { month: months(2026, 2), day: [at("2026-01-03T00:00:00Z"), at("2026-02-09T00:00:00Z")] },
      want: { interval: "day", values: ["2026-01-03 1", "2026-02-09 1"], asked: [["month", []], ["day", []]] },
    },
    {
      name: "unpicked, a few years: by month",
      predicates: [],
      buckets: { month: months(2024, 30) },
      want: { interval: "month", values: months(2024, 30).map((b) => `${b.value.slice(0, 7)} ${b.count}`), asked: [["month", []]] },
    },
    {
      name: "unpicked, over three years: the months summed by year",
      predicates: [],
      buckets: { month: months(2020, 60) },
      want: { interval: "year", values: ["2020 78", "2021 222", "2022 366", "2023 510", "2024 654"], asked: [["month", []]] },
    },
    {
      name: "unpicked, months cut at the engine's bound: counted again by year",
      predicates: [],
      buckets: { month: months(2000, 100), year: [at("2000-01-01T00:00:00Z"), at("2008-01-01T00:00:00Z")] },
      want: { interval: "year", values: ["2000 1", "2008 1"], asked: [["month", []], ["year", []]] },
    },
    {
      name: "a year picked: its months, under its own filter",
      predicates: [en, picked("2026")],
      buckets: { month: [at("2026-03-01T00:00:00Z")] },
      want: {
        interval: "month",
        values: ["2026-03 1"],
        asked: [
          ["month", ["language", "published_at 2026-01-01T00:00:00.000Z"]],
          [undefined, ["published_at 2026-01-01T00:00:00.000Z"]],
        ],
      },
    },
    {
      name: "a day picked: the days of its month, the other facets under the day",
      predicates: [picked("2026-10-05")],
      buckets: { day: [at("2026-10-05T00:00:00Z"), at("2026-10-20T00:00:00Z")] },
      want: {
        interval: "day",
        values: ["2026-10-05 1", "2026-10-20 1"],
        asked: [
          [undefined, ["published_at 2026-10-05T00:00:00.000Z"]],
          ["day", ["published_at 2026-10-01T00:00:00.000Z"]],
        ],
      },
    },
  ];
  for (const c of cases) assert.deepEqual(await counted(c.predicates, c.buckets), c.want, c.name);
});

// The timeline (THE-1204) draws the documents around the range it picks: it
// leaves the range out, keeps the span it shows, and counts the documents
// every filter keeps.
test("the timeline counts outside its range, within its window, and totals what every filter keeps", async () => {
  const range = { field: date.name, gte: "2026-10-03T00:00:00.000Z", lte: "2026-10-04T23:59:59.999Z" };
  const window = { gte: "2026-09-20T00:00:00.000Z", lte: "2026-10-10T23:59:59.999Z" };
  const timeline = (w) => ({ field: date.name, ...(w ? { window: w } : {}) });
  const days = { day: [at("2026-10-01T00:00:00Z", 2), at("2026-10-03T00:00:00Z", 3)] };
  const cases = [
    {
      name: "nothing picked: its months, refined to days, and their sum as the total",
      predicates: [],
      timeline: timeline(),
      buckets: { month: [at("2026-10-01T00:00:00Z", 5)], ...days },
      want: { interval: "day", values: ["2026-10-01 2", "2026-10-03 3"], asked: [["month", []], ["day", []]], total: 5 },
    },
    {
      name: "a range picked: counted without it, the total from its years under every filter",
      predicates: [en, range],
      timeline: timeline(),
      buckets: { month: [at("2026-10-01T00:00:00Z", 5)], year: [at("2026-01-01T00:00:00Z", 3)], ...days },
      want: {
        interval: "day",
        values: ["2026-10-01 2", "2026-10-03 3"],
        asked: [
          [undefined, ["published_at 2026-10-03T00:00:00.000Z"]],
          ["month", ["language"]],
          ["year", ["language", "published_at 2026-10-03T00:00:00.000Z"]],
          ["day", ["language"]],
        ],
        total: 3,
      },
    },
    {
      name: "a window shown: counted within it, by the step its length asks",
      predicates: [range],
      timeline: timeline(window),
      buckets: { year: [at("2026-01-01T00:00:00Z", 3)], ...days },
      want: {
        interval: "day",
        values: ["2026-10-01 2", "2026-10-03 3"],
        asked: [
          [undefined, ["published_at 2026-10-03T00:00:00.000Z"]],
          ["day", ["published_at 2026-09-20T00:00:00.000Z"]],
          ["year", ["published_at 2026-10-03T00:00:00.000Z"]],
        ],
        total: 3,
      },
    },
  ];
  for (const c of cases) assert.deepEqual(await counted(c.predicates, c.buckets, c.timeline), c.want, c.name);
});

test("a corpus's own fields are counted apart, 16 at a time, and only the common count names exclusions", async () => {
  const own = Array.from({ length: 20 }, (_, i) => ({ name: `f${i}`, type: "string" }));
  const sent = [];
  const out = await countFacets({
    ids: ["c1"],
    fields: [language, ...own],
    predicates: [],
    count: async (body) => {
      sent.push(body.fields.map((f) => f.field));
      // A field the corpus's index does not serve yet excludes it.
      const missing = body.fields.some((f) => f.field === "f19");
      return {
        items: body.fields.map((f) => ({ field: f.field, buckets: missing ? [] : [at("x", 3)] })),
        ...(missing ? { excluded_corpora: [{ corpus_id: "c1", fields: ["f19"] }] } : {}),
      };
    },
  });
  assert.deepEqual(sent.map((names) => names.length), [1, 16, 4]);
  assert.equal(out.excluded_corpora, undefined);
  assert.deepEqual(out.fields[0].values, [{ value: "x", count: 3 }]);
});

// The browser asks one field at a time (THE-1387). A fast count may come from
// the engine's stored counts or a sample: the facade relays how, the oldest
// date for stored counts and any estimate as approximate, and asks the
// engine for that field alone.
test("one field is counted alone, once, fast when asked, and says how its counts were made", async () => {
  const sent = [];
  const answers = [
    { as_of: "2026-10-09T05:00:00Z" },
    { as_of: "2026-10-09T04:00:00Z", approximate: true, sample_fraction: 0.05 },
  ];
  const explorer = createExplorer({
    upstream: async (path, method, body) => {
      if (path !== "/v0/facets")
        return { status: 200, data: { name: "Example corpus", effective_retrieval: { fields: [{ name: "desk", type: "string", roles: ["filter"], source_pointer: "/desk" }] } } };
      sent.push(body);
      const marker = answers[sent.length - 1] || {};
      return { status: 200, data: { items: body.fields.map((f) => ({ field: f.field, buckets: f.interval ? [at("2026-10-01T00:00:00Z", 4)] : [at("fr", 3)] })), ...marker } };
    },
    picked: async () => ["c1"],
    demo: () => "c1",
  });
  const ask = (params) => explorer.facets(new URLSearchParams(params));
  const one = await ask({ field: "metadata.language", accuracy: "fast" });
  assert.deepEqual(sent.map((b) => [b.fields.map((f) => f.field), b.accuracy]), [[["metadata.language"], "fast"]]);
  assert.deepEqual(one, { fields: [{ field: "metadata.language", type: "string", values: [at("fr", 3)] }], as_of: "2026-10-09T05:00:00Z" });
  // The timeline also totals its documents; a range picked counts the total apart.
  const range = [{ field: date.name, gte: "2026-10-01T00:00:00.000Z", lte: "2026-10-01T23:59:59.999Z" }];
  const timeline = await ask({ field: date.name, accuracy: "fast", metadata: JSON.stringify(range) });
  assert.ok(sent.slice(1).every((b) => b.fields.length === 1 && b.fields[0].field === date.name && b.accuracy === "fast"));
  assert.deepEqual([timeline.total, timeline.as_of, timeline.approximate], [4, "2026-10-09T04:00:00Z", true]);
  // Exact counts carry no marker and send none.
  const exact = await ask({ field: "metadata.language" });
  assert.equal(sent.at(-1).accuracy, undefined);
  assert.deepEqual([exact.as_of, exact.approximate], [undefined, undefined]);
  // A corpus's own field is counted once too.
  const before = sent.length;
  await ask({ field: "desk" });
  assert.deepEqual(sent.slice(before).map((b) => b.fields.map((f) => f.field)), [["desk"]]);
  for (const params of [{ field: "metadata.unknown" }, { field: "metadata.language", accuracy: "approximate" }])
    await assert.rejects(ask(params), (error) => error.status === 422, JSON.stringify(params));
});
