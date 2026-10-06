// The demo's numbers, counted by the facade over every article of the corpus
// (quivr-search/catalog.mjs), never over the feed's latest articles. While
// the facade builds its index, `building` says the numbers are not whole yet.
import { request } from "./search";
import { scoped } from "./feed";
import { dayOf, localInstant } from "./moments";

const HOUR = 3600000;

interface Progress {
  building: boolean;
}

export interface FeedStats extends Progress {
  /** Articles that pass the whole filter in the period. */
  total: number;
  /** Every facet but the read state: Tout, and its unread part, Non lus. */
  all: number;
  unread: number;
  /** Every facet but the sources, per Source Namespace. */
  sources: Record<string, number>;
  /** Every facet but the alerts, per alert, and caught by any of them. */
  alerts: Record<string, number>;
  any_alert: number;
  /** The whole filter between each two bounds asked, newest first. */
  buckets: number[];
}

export interface FeedStatsQuery {
  after?: string;
  before?: string;
  buckets: string[];
  sources: string[];
  muted: string[];
  alerts: string[];
  read: "all" | "unread";
  since: string;
  read_ids: string[];
}

export const fetchFeedStats = (query: FeedStatsQuery, scope: string, signal?: AbortSignal) =>
  request<FeedStats>(scoped("/demo/feed/stats", scope), query, signal);

export interface Topic {
  label: string;
  count: number;
}

export const fetchTopics = (
  query: { after: string; before: string; sources: string[]; muted: string[]; alerts: string[] },
  scope: string,
  signal?: AbortSignal,
) => {
  const params = new URLSearchParams({ after: query.after, before: query.before });
  for (const s of query.sources) params.append("source", s);
  for (const s of query.muted) params.append("muted", s);
  for (const a of query.alerts) params.append("alert", a);
  return request<Progress & { items: Topic[] }>(scoped(`/demo/feed/topics?${params}`, scope), undefined, signal);
};

export interface SourceNumbers {
  /** Articles of the source in all. */
  all: number;
  /** Of which an alert caught. */
  caught: number;
  /** Per day between the bounds asked, newest first. */
  days: number[];
  /** When its oldest article arrived, in milliseconds. */
  first: number;
}

export const fetchSourceStats = (bounds: string[], signal?: AbortSignal) =>
  request<Progress & { sources: Record<string, SourceNumbers> }>(
    `/demo/sources/stats?${new URLSearchParams({ bounds: bounds.join(",") })}`,
    undefined,
    signal,
  );

/** The last seven local days as bounds, newest first: tomorrow's midnight down to six days ago. */
export function weekBounds(now: number) {
  const at = new Date(now);
  return Array.from({ length: 8 }, (_, i) =>
    localInstant(new Date(at.getFullYear(), at.getMonth(), at.getDate() + 1 - i).getTime()),
  );
}

/**
 * A local day hour by hour, as bounds newest first, up to the current hour
 * today, and the hour of the day each bucket starts at. Bounds step by real
 * hours from midnight, so a day that skips or repeats an hour when the clocks
 * change still gets one bucket per hour.
 */
export function hourBounds(day: string, now: number) {
  const [y, m, d] = day.split("-").map(Number);
  const midnight = new Date(y, m - 1, d).getTime();
  const next = new Date(y, m - 1, d + 1).getTime();
  const today = dayOf(now) === day;
  const starts: number[] = [];
  for (let t = midnight; t < next && (!today || t <= now); t += HOUR) starts.push(t);
  const end = Math.min(next, starts[starts.length - 1] + HOUR);
  starts.reverse();
  return {
    bounds: [end, ...starts].map(localInstant),
    hours: starts.map((t) => new Date(t).getHours()),
    length: today ? new Date(now).getHours() + 1 : 24,
  };
}
