// The Veille feed through the demo facade: a snapshot and a live SSE stream
// the facade builds from the core change feed. No core credential or cursor
// reaches the browser.
import { request } from "./search";

export interface FeedItem {
  record_id: string;
  version_id: string;
  /** Source namespace: a Connector Instance's, or HAND_NAMESPACE. */
  namespace: string;
  title: string;
  excerpt: string;
  /** When the facade saw this Version arrive. */
  received_at?: string;
  /** The source's own publication date, when it has one (RSS items). */
  published_at?: string;
}

/** Texts added from this web app use this namespace. */
export const HAND_NAMESPACE = "web-demo";

export const fetchFeed = (signal?: AbortSignal) =>
  request<{ items: FeedItem[]; live: boolean }>(
    "/demo/feed",
    undefined,
    signal,
  );

export const FEED_STREAM = "/demo/feed/stream";

const sortKey = (item: FeedItem) => item.received_at || item.published_at || "";

/** Newest first; items with no known date last. */
export const newestFirst = (a: FeedItem, b: FeedItem) =>
  sortKey(b).localeCompare(sortKey(a)) ||
  a.record_id.localeCompare(b.record_id);
