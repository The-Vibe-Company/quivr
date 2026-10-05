// What an alert's sheet shows about it: how often it catches something, the
// trend, where and when. The core dates neither Matches nor Subscriptions, so
// times come from the facade's index of every article (when Quivr accepted
// each one it caught: exact for the articles the feed read, to the hour for
// older ones) and from the date the facade noted when it created the alert.
import type { FeedItem } from "./feed";
import { daily } from "./moments";

const HOUR = 3600000;
const DAY = 24 * HOUR;
const dayMonth = new Intl.DateTimeFormat("fr-FR", { day: "numeric", month: "short" });

/** An article the index or the feed dates. */
export type Dated = Pick<FeedItem, "record_id" | "version_id" | "namespace" | "received_at" | "published_at">;

export const arrivedAt = (item: Pick<FeedItem, "received_at" | "published_at">) =>
  item.received_at || item.published_at || "";

export interface Bar {
  key: string;
  /** "30 sept." or "14 h", for the tooltip and the axis. */
  label: string;
  count: number;
  /** The last seven days, drawn darker. */
  recent: boolean;
}

export interface Growth {
  week: number;
  before: number;
  /** Percentage change, or null when the week before caught nothing. */
  change: number | null;
}

export interface AlertStats {
  /** Where the numbers start: its creation, else the oldest article indexed. */
  since: number | null;
  /** Whether `since` is the creation date the facade noted. */
  dated: boolean;
  /** Created less than seven days ago. */
  young: boolean;
  /** The oldest article indexed: counts start no earlier. */
  feedSince: number | null;
  /** Caught in the last seven days (or since creation when younger). */
  week: number;
  /**
   * Against the seven days before, once the alert is two weeks old and the
   * index reaches that far back: a shorter one would count nothing before.
   */
  growth: Growth | null;
  /** Caught per day since `since`; null when nothing was caught. */
  perDay: number | null;
  /** Share of the articles that arrived since `since` it caught. */
  share: number | null;
  /** Hour by hour over the last day for a young alert, else day by day. */
  mode: "hours" | "days";
  bars: Bar[];
  /** Seven-day moving average, per bar (days mode, from two weeks of bars). */
  average: number[] | null;
  sources: { namespace: string; count: number }[];
  /** Catches per hour of the day, 0 h to 23 h. */
  hours: number[];
}

export function alertStats({
  createdAt,
  caught,
  oldest,
  arrived: arrivedSince,
  now,
  total,
}: {
  createdAt?: string;
  /** The articles the alert caught, dated, newest first. */
  caught: Dated[];
  /** When the oldest article indexed arrived, if any. */
  oldest: number | null;
  /** Articles that arrived since `since` (its creation, else `oldest`), as the facade counts them. */
  arrived: number;
  now: number;
  /** Matches counted by the facade. */
  total: number;
}): AlertStats {
  const times = caught.map((i) => Date.parse(arrivedAt(i))).filter((t) => !Number.isNaN(t));
  const created = createdAt ? Date.parse(createdAt) : NaN;
  const dated = !Number.isNaN(created);
  // An alert older than the dates noted is measured over the index's window.
  const since = dated ? created : oldest;
  const age = since === null ? 0 : Math.max(0, now - since);
  const between = (from: number, to: number) => times.filter((t) => t > from && t <= to).length;

  const week = between(now - 7 * DAY, now);
  const feedSince = oldest;
  const growth = since !== null && age >= 14 * DAY && feedSince !== null && feedSince <= now - 14 * DAY
    ? (() => {
        const before = between(now - 14 * DAY, now - 7 * DAY);
        return { week, before, change: before ? Math.round(((week - before) / before) * 100) : null };
      })()
    : null;

  // Since its creation every Match counts; otherwise only what the index dates.
  const count = dated ? total : times.length;
  const perDay = count && since !== null ? count / Math.max(age / DAY, 1 / 24) : null;
  const share = since !== null && arrivedSince ? Math.min(1, between(since - 1, now) / arrivedSince) : null;

  let mode: AlertStats["mode"] = "days";
  let bars: Bar[];
  let average: number[] | null = null;
  if (since === null || age < 2 * DAY) {
    mode = "hours";
    const top = new Date(now).setMinutes(0, 0, 0);
    bars = Array.from({ length: 24 }, (_, i) => {
      const start = top - (23 - i) * HOUR;
      return {
        key: String(start),
        label: `${new Date(start).getHours()} h`,
        count: between(start - 1, start + HOUR - 1),
        recent: true,
      };
    });
  } else {
    const n = Math.min(30, Math.max(7, Math.ceil(age / DAY) + 1));
    const iso = times.map((t) => new Date(t).toISOString());
    // Six more days before the first bar, for its moving average.
    const days = daily(iso, now, n + 6);
    bars = days.slice(6).map((d, i) => ({
      key: d.day,
      label: dayMonth.format(new Date(`${d.day}T12:00:00`)),
      count: d.count,
      recent: i >= n - 7,
    }));
    if (n >= 14)
      average = bars.map((_, i) => days.slice(i, i + 7).reduce((sum, d) => sum + d.count, 0) / 7);
  }

  const bySource = new Map<string, number>();
  for (const item of caught) bySource.set(item.namespace, (bySource.get(item.namespace) || 0) + 1);
  const hours = new Array<number>(24).fill(0);
  for (const t of times) hours[new Date(t).getHours()] += 1;

  return {
    since,
    dated,
    young: dated && age < 7 * DAY,
    feedSince,
    week,
    growth,
    perDay,
    share,
    mode,
    bars,
    average,
    sources: [...bySource].map(([namespace, count]) => ({ namespace, count })).sort((a, b) => b.count - a.count),
    hours,
  };
}

const PERIODS: [string, number[]][] = [
  ["la nuit", [23, 0, 1, 2, 3, 4]],
  ["le matin", [5, 6, 7, 8, 9, 10, 11]],
  ["l’après-midi", [12, 13, 14, 15, 16, 17]],
  ["le soir", [18, 19, 20, 21, 22]],
];

/** "le soir" when most catches arrive then. */
export function busiestPeriod(hours: number[]) {
  const sum = hours.reduce((a, b) => a + b, 0);
  if (sum < 3) return null;
  for (const [label, at] of PERIODS)
    if (at.reduce((n, h) => n + hours[h], 0) / sum >= 0.6) return label;
  return null;
}

/** The first day of the run of days with a catch that ends today or yesterday. */
export function streakStart(bars: Bar[]) {
  let i = bars.length - 1;
  if (i >= 0 && !bars[i].count) i -= 1;
  const end = i;
  while (i >= 0 && bars[i].count) i -= 1;
  return end - i >= 3 ? bars[i + 1] : null;
}
