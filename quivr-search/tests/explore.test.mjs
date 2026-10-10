import assert from "node:assert/strict";
import test from "node:test";
import { createExplorer } from "../explore.mjs";

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

// Exercise the facade with raw engine answers. It selects the histogram,
// filters and batches; the fixture only supplies schema and interval buckets.
async function counted(field, predicates, buckets, window) {
  const sent = [];
  const explorer = createExplorer({
    upstream: async (path, method, body) => {
      if (path !== "/v0/facets")
        return { status: 200, data: { effective_retrieval: { fields: [
          { name: "issued_at", type: "datetime", roles: ["filter"], source_pointer: "/issued_at" },
        ] } } };
      sent.push(body);
      return { status: 200, data: {
        items: body.fields.map((f) => ({ field: f.field, buckets: buckets[f.interval] || [] })),
      } };
    },
    picked: async () => ["c1"],
    demo: () => "c1",
  });
  const params = { field, metadata: JSON.stringify(predicates), ...(window ? { window: `${window.gte},${window.lte}` } : {}) };
  const out = await explorer.facets(new URLSearchParams(params));
  // Another facet keeps the selected date but leaves its own predicate out.
  if (predicates.length) await explorer.facets(new URLSearchParams({ ...params, field: language.name }));
  const histogram = out.fields.find((f) => f.field === field);
  const requests = (name) => sent
    .filter((b) => b.fields.some((f) => f.field === name))
    .map((b) => [b.fields.find((f) => f.field === name).interval, b.filter?.metadata || []]);
  return {
    interval: histogram.interval,
    values: histogram.values.map((v) => `${v.value} ${v.count}`),
    asked: requests(field),
    languageAsked: requests(language.name),
    ...(out.total === undefined ? {} : { total: out.total }),
  };
}

// Requests for distinct counts may start in either order; membership matters.
function sameCounts(actual, want, name) {
  const { asked, languageAsked, ...result } = actual;
  const { asked: expected, languageAsked: other = [], ...output } = want;
  const order = (requests) => requests.toSorted((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b)));
  assert.deepEqual(result, output, name);
  assert.deepEqual(order(asked), order(expected), `${name}: date requests`);
  assert.deepEqual(languageAsked, other, `${name}: language requests`);
}

const at = (value, count = 1) => ({ value, count });
// n months from January of a year, the i-th counting i + 1 documents.
const months = (first, n) =>
  Array.from({ length: n }, (_, i) => {
    const d = new Date(Date.UTC(first, i, 1));
    return at(d.toISOString().replace(".000", ""), i + 1);
  });
const en = { field: language.name, any_of: ["en"] };

test("a date's histogram steps by the period picked, or by the span of its documents", async () => {
  const year = { field: "issued_at", gte: "2026-01-01T00:00:00.000Z", lte: "2026-12-31T23:59:59.999Z" };
  const day = { field: "issued_at", gte: "2026-10-05T00:00:00.000Z", lte: "2026-10-05T23:59:59.999Z" };
  const month = { field: "issued_at", gte: "2026-10-01T00:00:00.000Z", lte: "2026-10-31T23:59:59.999Z" };
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
      predicates: [en, year],
      buckets: { month: [at("2026-03-01T00:00:00Z")] },
      want: {
        interval: "month", values: ["2026-03 1"],
        asked: [["month", [en, year]]], languageAsked: [[undefined, [year]]],
      },
    },
    {
      name: "a day picked: the days of its month, the other facets under the day",
      predicates: [day],
      buckets: { day: [at("2026-10-05T00:00:00Z"), at("2026-10-20T00:00:00Z")] },
      want: {
        interval: "day", values: ["2026-10-05 1", "2026-10-20 1"],
        asked: [["day", [month]]], languageAsked: [[undefined, [day]]],
      },
    },
  ];
  for (const c of cases) sameCounts(await counted("issued_at", c.predicates, c.buckets), c.want, c.name);
});

// The timeline (THE-1204) draws the documents around the range it picks: it
// leaves the range out, keeps the span it shows, and counts the documents
// every filter keeps.
test("the timeline counts outside its range, within its window, and totals what every filter keeps", async () => {
  const range = { field: date.name, gte: "2026-10-03T00:00:00.000Z", lte: "2026-10-04T23:59:59.999Z" };
  const window = { gte: "2026-09-20T00:00:00.000Z", lte: "2026-10-10T23:59:59.999Z" };
  const days = { day: [at("2026-10-01T00:00:00Z", 2), at("2026-10-03T00:00:00Z", 3)] };
  const cases = [
    {
      name: "nothing picked: its months, refined to days, and their sum as the total",
      predicates: [],
      buckets: { month: [at("2026-10-01T00:00:00Z", 5)], ...days },
      want: { interval: "day", values: ["2026-10-01 2", "2026-10-03 3"], asked: [["month", []], ["day", []]], total: 5 },
    },
    {
      name: "a range picked: counted without it, the total from its years under every filter",
      predicates: [en, range],
      buckets: { month: [at("2026-10-01T00:00:00Z", 5)], year: [at("2026-01-01T00:00:00Z", 3)], ...days },
      want: {
        interval: "day", values: ["2026-10-01 2", "2026-10-03 3"], total: 3,
        asked: [["month", [en]], ["year", [en, range]], ["day", [en]]],
        languageAsked: [[undefined, [range]]],
      },
    },
    {
      name: "a window shown: counted within it, by the step its length asks",
      predicates: [range], window,
      buckets: { year: [at("2026-01-01T00:00:00Z", 3)], ...days },
      want: {
        interval: "day", values: ["2026-10-01 2", "2026-10-03 3"], total: 3,
        asked: [["day", [{ field: date.name, ...window }]], ["year", [range]]],
        languageAsked: [[undefined, [range]]],
      },
    },
  ];
  for (const c of cases) sameCounts(await counted(date.name, c.predicates, c.buckets, c.window), c.want, c.name);
});

test("a corpus's own fields are counted apart, at most 16 at a time, and only the common count names exclusions", async () => {
  const own = Array.from({ length: 20 }, (_, i) => ({ name: `f${i}`, type: "string", roles: ["filter"], source_pointer: `/f${i}` }));
  const sent = [];
  const explorer = createExplorer({
    upstream: async (path, method, body) => {
      if (path !== "/v0/facets") return { status: 200, data: { effective_retrieval: { fields: own } } };
      sent.push(body.fields.map((f) => f.field));
      // A field the corpus's index does not serve yet excludes it.
      const missing = body.fields.some((f) => f.field === "f19");
      return { status: 200, data: {
        items: body.fields.map((f) => ({ field: f.field, buckets: missing || f.interval ? [] : [at("x", 3)] })),
        ...(missing ? { excluded_corpora: [{ corpus_id: "c1", fields: ["f19"] }] } : {}),
      } };
    },
    picked: async () => ["c1"],
    demo: () => "c1",
  });
  const out = await explorer.facets(new URLSearchParams());
  const common = sent.filter((names) => names.some((name) => name.startsWith("metadata.")));
  assert.equal(common.length, 1);
  assert.ok(common[0].every((name) => name.startsWith("metadata.")), "common and own fields counted apart");
  const batches = sent.filter((names) => !names.some((name) => name.startsWith("metadata.")));
  assert.ok(batches.every((names) => names.length <= 16), JSON.stringify(batches));
  assert.deepEqual(batches.flat().toSorted(), own.map((f) => f.name).toSorted(), "each own field counted once");
  assert.equal(out.excluded_corpora, undefined);
  assert.deepEqual(out.fields.find((f) => f.field === language.name).values, [{ value: "x", count: 3 }]);
});

// The browser asks one field at a time (THE-1387). A fast count may come from
// the engine's stored counts or a sample: the facade relays how, the oldest
// date for stored counts and any estimate as approximate, and asks the
// engine for that field alone.
test("one field is counted alone, once, fast when asked, and says how its counts were made", async () => {
  const sent = [];
  // The timeline's counts are dated apart: the earliest instant dates them,
  // though its text sorts after a later one's.
  const answers = [
    { as_of: "2026-10-09T05:00:00Z" },
    { as_of: "2026-10-09T04:00:00.500Z", approximate: true, sample_fraction: 0.05 },
    { as_of: "2026-10-09T04:00:00Z" },
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
  // A field asked alone names the corpora its count excluded.
  const excluded = [{ corpus_id: "c1", fields: ["desk"] }];
  const lone = createExplorer({
    upstream: async (path, method, body) =>
      path === "/v0/facets"
        ? { status: 200, data: { items: body.fields.map((f) => ({ field: f.field, buckets: [] })), excluded_corpora: excluded } }
        : { status: 200, data: { name: "Example corpus", effective_retrieval: { fields: [{ name: "desk", type: "string", roles: ["filter"], source_pointer: "/desk" }] } } },
    picked: async () => ["c1"],
    demo: () => "c1",
  });
  assert.deepEqual((await lone.facets(new URLSearchParams({ field: "desk" }))).excluded_corpora, excluded);
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
