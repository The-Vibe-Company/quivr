import { useCallback, useEffect, useRef, useState } from "react";
import { APIError } from "../../lib/search";
import { fetchFacets, type Accuracy, type Facets, type MetadataFilter, type Range } from "../../lib/explore";

// Fields one view counts at once: the engine admits eight counts per instance.
const AT_ONCE = 3;
// How often the age of stored counts is read again.
const AGE_TICK_MS = 30000;

/** The time now, read again every 30 s while `active`, so ages shown agree. */
export function useNow(active: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), AGE_TICK_MS);
    return () => clearInterval(timer);
  }, [active]);
  return now;
}

/** One field's counts, and the corpora, filters and span (`key`) they were read for. */
export interface FieldCount extends Partial<Facets> {
  key: string;
  /** Exact counts are on their way to replace these estimates. */
  refining?: boolean;
  /** Why the last read failed; the counts last read stay shown. */
  error?: string;
  /** Why exact counts could not replace these estimates. */
  refineError?: string;
}

/** The reads for one set of corpora, filters and span. */
interface Session {
  key: string;
  corpora: string[];
  predicates: MetadataFilter[];
  window?: Range;
  controller: AbortController;
  /** Fields read, or waiting to be. */
  asked: Set<string>;
  queue: string[];
  active: number;
  /** Exact counts replace estimates one field after another. */
  exact: Promise<void>;
  /** Fields whose counts, or whose exact counts, failed in this session. */
  failed: Set<string>;
  refineFailed: Set<string>;
}

type Put = (field: string, change: (count?: FieldCount) => FieldCount | undefined) => void;

/**
 * Each field's counts (THE-1387), read one field at a time so a slow field
 * holds back no other: first fast, from the engine's stored counts or a
 * sample, then, for an estimate, exactly, one field after another. Counts
 * read for other filters stay shown until replaced; other corpora clear
 * them. Fields shown later join the reads under way; `retry` asks again
 * only for what failed.
 */
export function useFacetCounts({
  corpora,
  predicates,
  window,
  fields,
  enabled,
  onUnauthorized,
}: {
  corpora: string[];
  predicates: MetadataFilter[];
  window?: Range;
  fields: string[];
  enabled: boolean;
  onUnauthorized: () => void;
}) {
  const [counts, setCounts] = useState<Map<string, FieldCount>>(() => new Map());
  const latest = useRef(counts);
  latest.current = counts;
  const key = JSON.stringify([corpora, predicates, window]);
  const session = useRef<Session | null>(null);
  const shownFor = useRef(corpora.join(","));
  // What a new session reads, as of the last render.
  const reading = useRef({ corpora, predicates, window, fields, onUnauthorized });
  reading.current = { corpora, predicates, window, fields, onUnauthorized };

  // Starts the reads a session still owes, within its bound, and returns
  // how to queue an estimate's exact count.
  const pump = useCallback((s: Session): { put: Put; refine: (name: string) => void } => {
    const { corpora, predicates, window } = s;
    const { onUnauthorized } = reading.current;
    const { signal } = s.controller;
    const put: Put = (field, change) =>
      setCounts((all) => {
        const next = change(all.get(field));
        return next ? new Map(all).set(field, next) : all;
      });
    const ask = (field: string, accuracy: Accuracy) =>
      fetchFacets(corpora, predicates, window, { field, accuracy }, signal);
    // Whether a failure ends the reads: this view left, or the session did.
    const over = (e: unknown) => {
      if (signal.aborted) return true;
      if (e instanceof APIError && e.status === 401) {
        onUnauthorized();
        return true;
      }
      return false;
    };
    const message = (e: unknown) => (e instanceof Error ? e.message : "Les nombres ne s’affichent pas.");
    const refine = (name: string) => {
      s.exact = s.exact.then(async () => {
        if (signal.aborted) return;
        try {
          const data = await ask(name, "exact");
          put(name, () => ({ ...data, key: s.key }));
        } catch (e) {
          if (over(e)) return;
          // The estimate stays, said to be one.
          s.refineFailed.add(name);
          put(name, (count) =>
            count?.key === s.key ? { ...count, refining: false, refineError: message(e) } : undefined,
          );
        }
      });
    };
    while (s.active < AT_ONCE && s.queue.length) {
      const name = s.queue.shift()!;
      s.active++;
      ask(name, "fast")
        .then((data) => {
          put(name, () => ({ ...data, key: s.key, refining: data.approximate }));
          if (data.approximate) refine(name);
        })
        .catch((e) => {
          if (over(e)) return;
          s.failed.add(name);
          put(name, (count) => ({ ...(count || { key: "" }), error: message(e) }));
        })
        .finally(() => {
          s.active--;
          if (!signal.aborted) pump(s);
        });
    }
    return { put, refine };
  }, []);

  // Asks for the fields a session has not asked for yet; estimates already
  // shown for its filters wait for their exact counts again.
  const ask = useCallback(
    (s: Session, names: string[]) => {
      const { put, refine } = pump(s);
      for (const name of names) {
        if (s.asked.has(name)) continue;
        s.asked.add(name);
        const count = latest.current.get(name);
        if (count?.key === s.key && !count.error) {
          if (count.refining) refine(name);
          continue;
        }
        // A failure under other filters is not this read's: it loads again.
        if (count?.error) put(name, (c) => (c ? { ...c, error: undefined } : c));
        s.queue.push(name);
      }
      pump(s);
    },
    [pump],
  );

  // Other corpora, filters or span: the reads under way stop, new ones start.
  useEffect(() => {
    if (!enabled) return;
    if (shownFor.current !== corpora.join(",")) {
      shownFor.current = corpora.join(",");
      latest.current = new Map();
      setCounts(new Map());
    }
    const s: Session = {
      key,
      corpora: reading.current.corpora,
      predicates: reading.current.predicates,
      window: reading.current.window,
      controller: new AbortController(),
      asked: new Set(),
      queue: [],
      active: 0,
      exact: Promise.resolve(),
      failed: new Set(),
      refineFailed: new Set(),
    };
    session.current = s;
    ask(s, reading.current.fields);
    return () => s.controller.abort();
    // The key holds the corpora, the predicates and the span shown.
  }, [key, enabled, ask]);

  // More fields shown: they join the reads under way.
  const fieldsKey = fields.join("\n");
  useEffect(() => {
    if (enabled && session.current?.key === key) ask(session.current, reading.current.fields);
  }, [fieldsKey, enabled, key, ask]);

  /** Asks again for what failed: a field's counts, or an estimate's exact counts. */
  const retry = useCallback(() => {
    const s = session.current;
    if (!s || s.controller.signal.aborted) return;
    const { put, refine } = pump(s);
    for (const name of s.failed) {
      // Counts read for other filters show dimmed while they load again.
      put(name, (c) => (c ? { ...c, error: undefined } : c));
      s.queue.push(name);
    }
    for (const name of s.refineFailed) {
      put(name, (c) => (c?.key === s.key ? { ...c, refineError: undefined, refining: true } : c));
      refine(name);
    }
    s.failed.clear();
    s.refineFailed.clear();
    pump(s);
  }, [pump]);

  return { counts, key, retry };
}
