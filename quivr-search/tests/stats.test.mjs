import { test } from "node:test";
import assert from "node:assert/strict";
import { feedStats, sourceStats } from "../stats.mjs";

// The rules of the demo's counts (stats.mjs) on 1,000 synthetic articles,
// well past the feed's 300: article i arrived i % 10 days ago, from source
// i % 4, and alert "a" caught every fifth one, alert "b" every seventh.
const NOW = Date.parse("2026-10-05T12:00:00Z");
const HOUR = 3600000;
const DAY = 24 * HOUR;
const rows = Array.from({ length: 1000 }, (_, i) => ({
  record_id: `r${i}`,
  version_id: `v${i}`,
  namespace: `source-${i % 4}`,
  at: NOW - (i % 10) * DAY - HOUR,
}));
const matched = new Map();
for (let i = 0; i < 1000; i++) {
  const caught = [...(i % 5 ? [] : ["a"]), ...(i % 7 ? [] : ["b"])];
  if (caught.length) matched.set(`r${i}`, caught);
}
// Ten days as bounds, newest first: bar k holds [NOW - (k+1) days, NOW - k days).
const days = Array.from({ length: 11 }, (_, k) => NOW - k * DAY);

test("every article counts, and each facet's counts leave that facet out", () => {
  const all = feedStats(rows, { buckets: days }, matched);
  assert.equal(all.total, 1000);
  assert.deepEqual(all.buckets, new Array(10).fill(100));
  assert.deepEqual({ ...all.sources }, { "source-0": 250, "source-1": 250, "source-2": 250, "source-3": 250 });
  // 200 caught by a, 143 by b, 29 by both.
  assert.deepEqual([{ ...all.alerts }, all.any_alert], [{ a: 200, b: 143 }, 314]);

  // Source 1 over the last three days: odd articles, so only day 1 holds
  // any. The bars keep their own days, for the source on each.
  const picked = feedStats(
    rows,
    { after: NOW - 3 * DAY, before: NOW, buckets: days, sources: ["source-1"] },
    matched,
  );
  assert.equal(picked.total, 50);
  assert.deepEqual(picked.buckets, [0, 50, 0, 50, 0, 50, 0, 50, 0, 50]);
  // Its menu still counts every source of the period, for picking another.
  assert.equal(Object.values(picked.sources).reduce((a, b) => a + b, 0), 300);
  assert.equal(picked.sources["source-1"], 50);
  // An alert narrows the sources' counts but not its own menu.
  const caught = feedStats(rows, { alerts: ["b"] }, matched);
  assert.deepEqual([caught.total, caught.alerts.a, caught.any_alert], [143, 200, 314]);
});

test("unread follows the browser's first visit and what it read, and muted sources leave unless picked", () => {
  // Days 0 to 2 arrived after the first visit; ten of them were read.
  const read = rows.filter((r) => r.at > NOW - 2.5 * DAY).slice(0, 10);
  const query = {
    since: NOW - 2.5 * DAY,
    readIds: read.map((r) => `${r.record_id}:${r.version_id}`),
  };
  const all = feedStats(rows, query, matched);
  assert.deepEqual([all.all, all.unread, all.total], [1000, 290, 1000]);
  const unread = feedStats(rows, { ...query, read: "unread" }, matched);
  assert.deepEqual([unread.total, unread.all, unread.unread], [290, 1000, 290]);
  // A new Version of a read article is unread again.
  const corrected = rows.map((r) => (r === read[0] ? { ...r, version_id: "v-new" } : r));
  assert.equal(feedStats(corrected, query, matched).unread, 291);

  const muted = feedStats(rows, { muted: ["source-2"] }, matched);
  assert.equal(muted.total, 750);
  assert.equal(muted.sources["source-2"], undefined);
  const both = feedStats(rows, { muted: ["source-2"], sources: ["source-2"] }, matched);
  assert.equal(both.total, 250);
});

test("a bar holds its lower bound, not its upper one", () => {
  const edge = [
    { record_id: "x", version_id: "x", namespace: "s", at: NOW },
    { record_id: "y", version_id: "y", namespace: "s", at: NOW - DAY },
    { record_id: "z", version_id: "z", namespace: "s", at: NOW + DAY },
  ];
  const s = feedStats(edge, { buckets: [NOW + DAY, NOW, NOW - DAY] }, new Map());
  assert.deepEqual(s.buckets, [1, 1]);
});

test("each source counts all its articles, those caught, its days and its oldest", () => {
  const s = sourceStats(rows, days.slice(0, 8), matched);
  assert.deepEqual(Object.keys(s).sort(), ["source-0", "source-1", "source-2", "source-3"]);
  // Source 0 holds articles 0, 4, 8…: 50 of them are multiples of 20, 36 of
  // 28, and 8 of both.
  assert.equal(s["source-0"].all, 250);
  assert.equal(s["source-0"].caught, 50 + 36 - 8);
  // Even days only, 50 each, over the last seven.
  assert.deepEqual(s["source-0"].days, [50, 0, 50, 0, 50, 0, 50]);
  assert.equal(s["source-0"].first, NOW - 8 * DAY - HOUR);
});
