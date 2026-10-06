import assert from "node:assert/strict";
import test from "node:test";
import { countFacets, periodBounds } from "../explore.mjs";

// How the Explorer counts a date (THE-1184): the step of its histogram and
// the filters each count keeps, against a fake engine that answers each
// interval with the buckets given.
const date = { name: "metadata.published_at", type: "datetime" };
const language = { name: "metadata.language", type: "string" };

async function counted(predicates, buckets) {
  const sent = [];
  const out = await countFacets({
    ids: ["c1"],
    fields: [language, date],
    predicates,
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
  return { interval: histogram.interval, values: histogram.values.map((v) => `${v.value} ${v.count}`), asked };
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
