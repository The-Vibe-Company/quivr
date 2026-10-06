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
  return { interval: histogram.interval, values: histogram.values.map((v) => v.value), asked };
}

const at = (value, count = 1) => ({ value, count });
const months = (first, n) =>
  Array.from({ length: n }, (_, i) => {
    const d = new Date(Date.UTC(first, i, 1));
    return at(d.toISOString().replace(".000", ""));
  });
const picked = (period) => ({ field: date.name, ...periodBounds(period) });
const en = { field: language.name, any_of: ["en"] };

test("a date's histogram steps by the period picked, or by the span of its documents", async () => {
  const cases = [
    {
      name: "unpicked, within two months: counted again by day",
      predicates: [],
      buckets: { month: months(2026, 2), day: [at("2026-01-03T00:00:00Z"), at("2026-02-09T00:00:00Z")] },
      want: { interval: "day", values: ["2026-01-03", "2026-02-09"], asked: [["month", []], ["day", []]] },
    },
    {
      name: "unpicked, a few years: by month",
      predicates: [],
      buckets: { month: months(2024, 30) },
      want: { interval: "month", values: months(2024, 30).map((b) => b.value.slice(0, 7)), asked: [["month", []]] },
    },
    {
      name: "unpicked, over three years: the months summed by year",
      predicates: [],
      buckets: { month: months(2020, 60) },
      want: { interval: "year", values: ["2020", "2021", "2022", "2023", "2024"], asked: [["month", []]] },
    },
    {
      name: "unpicked, months cut at the engine's bound: counted again by year",
      predicates: [],
      buckets: { month: months(2000, 100), year: [at("2000-01-01T00:00:00Z"), at("2008-01-01T00:00:00Z")] },
      want: { interval: "year", values: ["2000", "2008"], asked: [["month", []], ["year", []]] },
    },
    {
      name: "a year picked: its months, under its own filter",
      predicates: [en, picked("2026")],
      buckets: { month: [at("2026-03-01T00:00:00Z")] },
      want: {
        interval: "month",
        values: ["2026-03"],
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
        values: ["2026-10-05", "2026-10-20"],
        asked: [
          [undefined, ["published_at 2026-10-05T00:00:00.000Z"]],
          ["day", ["published_at 2026-10-01T00:00:00.000Z"]],
        ],
      },
    },
  ];
  for (const c of cases) assert.deepEqual(await counted(c.predicates, c.buckets), c.want, c.name);
});
