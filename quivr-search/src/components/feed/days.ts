// The feed's days as Quivr stores them: how many articles each day holds,
// counted by the engine, and the articles of one picked day, loaded page by
// page. The live feed only holds the latest articles; these reach the rest.
import { useCallback, useEffect, useRef, useState } from "react";
import { APIError } from "../../lib/search";
import {
  fetchFeedDays,
  fetchFeedPage,
  newestFirst,
  type FeedItem,
} from "../../lib/feed";
import { dayBounds, dayOf, shiftDay } from "../../lib/moments";

const CHUNK = 14;
// Days are counted back to the first article, at most about a year.
const MAX_CHUNKS = 27;
const REFRESH_MS = 60000;

export interface DayCounts {
  total: number;
  /** Every day from today back to the first article, newest first. */
  days: [string, number][];
  /** When Quivr counted them. */
  asOf: string;
}

/**
 * Articles per day, from today back to the first article. Read once, then
 * every minute and whenever `refresh` is called (the date menu opens).
 */
export function useDayCounts(today: string, onUnauthorized: () => void) {
  const [counts, setCounts] = useState<DayCounts | null>(null);
  const [ask, setAsk] = useState(0);
  const unauthorized = useRef(onUnauthorized);
  unauthorized.current = onUnauthorized;
  useEffect(() => {
    const controller = new AbortController();
    (async () => {
      const days: [string, number][] = [];
      let total = 0;
      let asOf = "";
      for (let chunk = 0; chunk < MAX_CHUNKS; chunk++) {
        const first = shiftDay(today, -chunk * CHUNK);
        const list = Array.from({ length: CHUNK }, (_, i) => shiftDay(first, -i));
        const bounds = [
          dayBounds(first).before,
          ...list.map((day) => dayBounds(day).after),
        ];
        const answer = await fetchFeedDays(bounds, controller.signal);
        if (!chunk) {
          total = answer.total;
          asOf = answer.as_of;
        }
        list.forEach((day, i) => days.push([day, answer.days[i]]));
        if (!answer.older) break;
      }
      // Today and yesterday always show; earlier days start at the first article.
      while (days.length > 2 && !days.at(-1)![1]) days.pop();
      setCounts({ total, days, asOf });
    })().catch((error) => {
      if (controller.signal.aborted) return;
      if (error instanceof APIError && error.status === 401)
        unauthorized.current();
      // Otherwise the menu keeps the last counts, or the loaded articles'.
    });
    return () => controller.abort();
  }, [today, ask]);
  useEffect(() => {
    const timer = setInterval(() => setAsk((n) => n + 1), REFRESH_MS);
    return () => clearInterval(timer);
  }, []);
  const refresh = useCallback(() => setAsk((n) => n + 1), []);
  return { counts, refresh };
}

/**
 * The articles that arrived live after Quivr counted. An article is dated by
 * the first Version this page saw of it, so a new Version of an article it
 * already showed is not counted twice.
 */
export function useLiveSince(items: FeedItem[]) {
  const firstSeen = useRef(new Map<string, string>());
  for (const item of items) {
    const seen = firstSeen.current.get(item.record_id);
    if (item.received_at && (!seen || item.received_at < seen))
      firstSeen.current.set(item.record_id, item.received_at);
  }
  return (asOf: string) =>
    items.filter((item) => (firstSeen.current.get(item.record_id) || "") > asOf);
}

/** The articles of one day ("" for none), newest first, a page at a time. */
export function useDayItems(day: string, onUnauthorized: () => void) {
  const [items, setItems] = useState<FeedItem[]>([]);
  const [next, setNext] = useState<string | undefined>();
  const [status, setStatus] = useState<"idle" | "loading" | "ready" | "error">("idle");
  const [error, setError] = useState<{ message: string; stale: boolean } | null>(null);
  const [attempt, setAttempt] = useState(0);
  const loading = useRef(false);
  const current = useRef<{ day: string; controller: AbortController } | null>(null);
  const unauthorized = useRef(onUnauthorized);
  unauthorized.current = onUnauthorized;

  const load = useCallback((cursor: string | undefined) => {
    const run = current.current;
    if (!run || loading.current) return;
    loading.current = true;
    setStatus("loading");
    fetchFeedPage(dayBounds(run.day), cursor, run.controller.signal)
      .then((page) => {
        if (current.current !== run) return;
        setItems((shown) => {
          const ids = new Set(shown.map((i) => i.record_id));
          return [...shown, ...page.items.filter((i) => !ids.has(i.record_id))].sort(newestFirst);
        });
        setNext(page.next_cursor);
        setStatus("ready");
      })
      .catch((e) => {
        if (current.current !== run) return;
        if (e instanceof APIError && e.status === 401) return unauthorized.current();
        setError({
          message: e instanceof Error ? e.message : "Ce jour ne s’affiche pas.",
          // The day changed under its page cursor: only a fresh start reads it.
          stale: e instanceof APIError && e.status === 409,
        });
        setStatus("error");
      })
      .finally(() => {
        if (current.current === run) loading.current = false;
      });
  }, []);

  useEffect(() => {
    setItems([]);
    setNext(undefined);
    setError(null);
    loading.current = false;
    if (!day) {
      current.current = null;
      setStatus("idle");
      return;
    }
    const run = { day, controller: new AbortController() };
    current.current = run;
    load(undefined);
    return () => run.controller.abort();
  }, [day, attempt, load]);

  const more = useCallback(() => {
    if (next) load(next);
  }, [next, load]);
  // A failed later page tries again; a failed first page, or a cursor the day
  // outgrew, starts the day over.
  const retry = useCallback(() => {
    if (next && !error?.stale) load(next);
    else setAttempt((n) => n + 1);
  }, [next, error, load]);
  return { items, next, status, error: error?.message || "", more, retry };
}

/** Whether a day as "2026-10-03" is the one of a time. */
export const onDay = (value: string | undefined, day: string) =>
  !!value && dayOf(value) === day;
