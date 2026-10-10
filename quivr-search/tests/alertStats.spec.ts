import { test, expect } from "@playwright/test";
import { alertStats, busiestPeriod, streakStart } from "../src/lib/alertStats";
import type { FeedItem } from "../src/lib/feed";

// The owner of an alert's sheet numbers (THE-1017): which window they cover,
// when growth is shown, and the sentence's helpers. The sheet only renders
// these.

const NOW = Date.parse("2026-10-04T15:00:00");
const HOUR = 3600000;
const DAY = 24 * HOUR;
let n = 0;
/** An article that arrived `ago` milliseconds before NOW. */
const at = (ago: number, namespace = "Dépêches"): FeedItem => ({
  record_id: `r${++n}`,
  version_id: `v${n}`,
  namespace,
  title: "",
  excerpt: "",
  received_at: new Date(NOW - ago).toISOString(),
});
/**
 * The sheet's numbers for an alert. The facade gives the oldest article it
 * indexed and how many arrived since the alert started (or since the oldest);
 * server.test.mjs owns how it counts them.
 */
const run = (
  createdAt: string | undefined,
  caught: FeedItem[],
  index: { oldest: number | null; arrived: number },
  total = caught.length,
) => alertStats({ createdAt, caught, ...index, now: NOW, total });

test("une alerte récente se lit heure par heure, depuis sa création", () => {
  const caught = [at(1 * HOUR), at(2 * HOUR), at(2.5 * HOUR)];
  const s = run(new Date(NOW - 5 * HOUR).toISOString(), caught, { oldest: NOW - 4 * HOUR, arrived: 5 });
  expect(s.mode).toBe("hours");
  expect(s.bars).toHaveLength(24);
  expect(s.young).toBe(true);
  expect(s.week).toBe(3);
  expect(s.growth).toBeNull();
  // Three articles in five hours, and three of the five that arrived since.
  expect(s.perDay).toBeCloseTo(3 / (5 / 24));
  expect(s.share).toBeCloseTo(3 / 5);
});

test("une alerte sans date se mesure depuis le plus ancien article indexé, pas depuis sa première prise", () => {
  const caught = [at(1 * HOUR)];
  const s = run(undefined, caught, { oldest: NOW - 30 * HOUR, arrived: 3 }, 9);
  expect(s.dated).toBe(false);
  expect(s.young).toBe(false);
  expect(s.since).toBe(NOW - 30 * HOUR);
  // Only what the index dates counts, not the facade's total of 9.
  expect(s.perDay).toBeCloseTo(1 / (30 / 24));
  expect(s.share).toBeCloseTo(1 / 3);
});

test("la croissance ne compare deux semaines que si l’index les couvre", () => {
  const created = new Date(NOW - 20 * DAY).toISOString();
  const caught = [at(1 * DAY), at(2 * DAY), at(9 * DAY)];
  // An index of a few hours: nothing before is known, so no growth.
  expect(run(created, caught.slice(0, 1), { oldest: NOW - 3 * HOUR, arrived: 2 }).growth).toBeNull();
  // An index of three weeks: 2 this week against 1 the week before.
  const s = run(created, caught, { oldest: NOW - 21 * DAY, arrived: 3 });
  expect(s.feedSince).toBe(NOW - 21 * DAY);
  expect(s.growth).toEqual({ week: 2, before: 1, change: 100 });
  expect(s.mode).toBe("days");
  // A bar per day since its creation, today included, with a 7-day average.
  expect(s.bars).toHaveLength(21);
  expect(s.average).toHaveLength(21);
});

test("une baisse et une semaine vide se lisent comme telles", () => {
  const created = new Date(NOW - 30 * DAY).toISOString();
  const caught = [at(8 * DAY), at(9 * DAY), at(10 * DAY), at(1 * DAY)];
  const s = run(created, caught, { oldest: NOW - 25 * DAY, arrived: 5 });
  expect(s.growth).toEqual({ week: 1, before: 3, change: -67 });
  const quiet = run(created, [at(9 * DAY)], { oldest: NOW - 25 * DAY, arrived: 2 });
  expect(quiet.growth).toEqual({ week: 0, before: 1, change: -100 });
});

test("sans rien attrapé, aucune fréquence ni part n’est inventée", () => {
  const s = run(new Date(NOW - 3 * DAY).toISOString(), [], { oldest: NOW - HOUR, arrived: 1 });
  expect(s.perDay).toBeNull();
  expect(s.share).toBe(0);
  expect(s.sources).toEqual([]);
  expect(run(undefined, [], { oldest: null, arrived: 0 }).since).toBeNull();
});

test("la phrase repère une série de jours et le moment où les articles arrivent", () => {
  const bar = (count: number) => ({ key: "", label: `j${count}`, count, recent: true });
  expect(streakStart([bar(0), bar(1), bar(2), bar(3)])?.label).toBe("j1");
  // Today may still be empty: the run ends yesterday.
  expect(streakStart([
    { key: "2026-10-01", label: "1 oct.", count: 1, recent: true },
    { key: "2026-10-02", label: "2 oct.", count: 1, recent: true },
    { key: "2026-10-03", label: "3 oct.", count: 1, recent: true },
    { key: "2026-10-04", label: "4 oct.", count: 0, recent: true },
  ])?.label).toBe("1 oct.");
  expect(streakStart([bar(1), bar(0), bar(1), bar(1)])).toBeNull();
  const hours = new Array(24).fill(0);
  hours[19] = 3;
  hours[20] = 2;
  expect(busiestPeriod(hours)).toBe("le soir");
  hours[8] = 5;
  expect(busiestPeriod(hours)).toBeNull();
  expect(busiestPeriod(new Array(24).fill(0))).toBeNull();
});
