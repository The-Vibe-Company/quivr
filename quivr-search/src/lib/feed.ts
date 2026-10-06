// The Veille feed through the demo facade: a snapshot and a live SSE stream
// the facade builds from the core change feed. No core credential or cursor
// reaches the browser.
import { request } from "./search";

export interface FeedItem {
  record_id: string;
  version_id: string;
  /** The corpus it belongs to. */
  corpus_id?: string;
  /** Source namespace: a Connector Instance's, or HAND_NAMESPACE. */
  namespace: string;
  title: string;
  excerpt: string;
  /** When the facade saw this Version arrive. */
  received_at?: string;
  /** The source's own publication date, when it has one (RSS items). */
  published_at?: string;
  /** The original article on its site (http/https only), when the source gives one. */
  link?: string;
  /** When the facade saw a new Version of an article it already had. */
  updated_at?: string;
  /** The Version a correction replaced, still readable. */
  previous_version_id?: string;
}

/** Texts added from this web app use this namespace. */
export const HAND_NAMESPACE = "web-demo";

/**
 * A facade path for the corpora the feed follows: `scope` is their ids
 * joined by commas, or "" for the demo corpus alone.
 */
export const scoped = (path: string, scope: string) =>
  scope ? `${path}${path.includes("?") ? "&" : "?"}corpora=${encodeURIComponent(scope)}` : path;

export const fetchFeed = (scope: string, signal?: AbortSignal) =>
  request<{ items: FeedItem[]; live: boolean }>(
    scoped("/demo/feed", scope),
    undefined,
    signal,
  );

export const feedStream = (scope: string) => scoped("/demo/feed/stream", scope);

type Dates = Pick<FeedItem, "record_id" | "received_at" | "published_at">;
const sortKey = (item: Dates) => item.received_at || item.published_at || "";

/** Newest first; items with no known date last. */
export const newestFirst = (a: Dates, b: Dates) =>
  sortKey(b).localeCompare(sortKey(a)) ||
  a.record_id.localeCompare(b.record_id);

/** One page of a period, newest first, read from Quivr by the facade. */
export const fetchFeedPage = (
  bounds: { after: string; before: string },
  cursor: string | undefined,
  scope: string,
  signal?: AbortSignal,
) =>
  request<{ items: FeedItem[]; next_cursor?: string }>(
    scoped(`/demo/feed/page?${new URLSearchParams({ ...bounds, ...(cursor ? { cursor } : {}) })}`, scope),
    undefined,
    signal,
  );

/**
 * Articles in Quivr per period, from instants newest first: the total, one
 * count between each two bounds, how many are older than the last, and
 * when they were counted (the facade keeps them a minute).
 */
export const fetchFeedDays = (bounds: string[], scope: string, signal?: AbortSignal) =>
  request<{ total: number; days: number[]; older: number; as_of: string }>(
    scoped(`/demo/feed/days?${new URLSearchParams({ bounds: bounds.join(",") })}`, scope),
    undefined,
    signal,
  );
