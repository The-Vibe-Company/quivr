// The engine's rollups (THE-795) through the facade, for the Admin tab's
// sections: plugin calls, searches, processing steps and top queries over the
// last hour, day or week. Read-only; the facade caches each read a few
// seconds, so every section can poll.
import { useCallback, useEffect, useState } from "react";
import { APIError, request } from "./search";

export type StatsWindow = "1h" | "24h" | "7d";

/** A series over the whole window; latencies are present when count > 0. */
export interface StatsSummary {
  count: number;
  errors: number;
  p50_ms?: number;
  p95_ms?: number;
  mean_ms?: number;
  last_error_code?: string;
  last_error_at?: string;
}

/** One non-empty bucket of `resolution_seconds`; empty buckets are omitted. */
export interface StatsPoint {
  start: string;
  count: number;
  errors: number;
  p50_ms: number;
  p95_ms: number;
}

interface Series {
  summary: StatsSummary;
  points: StatsPoint[];
}

interface StatsList<T> {
  window: StatsWindow;
  resolution_seconds: number;
  from: string;
  to: string;
  items: T[];
}

export interface PluginCallStats extends Series {
  plugin_id: string;
  plugin_version: string;
  operation: string;
}

export interface SearchStats extends Series {
  mode: "lexical" | "semantic" | "hybrid";
  profile: string;
  results: number;
}

export interface StepStats extends Series {
  step: string;
}

export interface TopQueryList {
  window: StatsWindow;
  recording: boolean;
  items: { query: string; count: number }[];
}

export interface StatsKinds {
  plugins: StatsList<PluginCallStats>;
  searches: StatsList<SearchStats>;
  steps: StatsList<StepStats>;
  "top-queries": TopQueryList;
}

export const fetchStats = <K extends keyof StatsKinds>(
  kind: K,
  window: StatsWindow,
  signal?: AbortSignal,
  limit?: number,
) =>
  request<StatsKinds[K]>(
    `/demo/admin/stats/${kind}?window=${window}${limit ? `&limit=${limit}` : ""}`,
    undefined,
    signal,
  );

/**
 * Every bucket of a sparse series over its window, empty ones at zero, oldest
 * first: `from` to `to` in steps of `resolution_seconds`.
 */
export function fillPoints(
  list: { from: string; to: string; resolution_seconds: number },
  points: StatsPoint[],
) {
  const step = list.resolution_seconds * 1000;
  const from = Math.floor(Date.parse(list.from) / step) * step;
  const to = Date.parse(list.to);
  const byStart = new Map(
    points.map((p) => [Math.floor(Date.parse(p.start) / step) * step, p]),
  );
  const out: StatsPoint[] = [];
  for (let t = from; t < to; t += step)
    out.push(
      byStart.get(t) || {
        start: new Date(t).toISOString(),
        count: 0,
        errors: 0,
        p50_ms: 0,
        p95_ms: 0,
      },
    );
  return out;
}

export type StatsStatus = "loading" | "ready" | "unavailable" | "error";

const POLL_MS = 15000;

/**
 * One rollup, reread every 15 s while the page is visible. `unavailable`
 * means the deployment does not give the web app observability:read.
 */
export function useAdminStats<K extends keyof StatsKinds>(
  kind: K,
  window: StatsWindow,
  onUnauthorized: () => void,
  limit?: number,
) {
  const [data, setData] = useState<StatsKinds[K] | null>(null);
  const [status, setStatus] = useState<StatsStatus>("loading");
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    const read = () =>
      fetchStats(kind, window, controller.signal, limit)
        .then((next) => {
          setData(next);
          setStatus("ready");
        })
        .catch((e) => {
          if (controller.signal.aborted) return;
          if (e instanceof APIError && e.status === 401)
            return onUnauthorized();
          setError(e instanceof Error ? e.message : "Chiffres indisponibles.");
          if (e instanceof APIError && e.status === 403) {
            // Not enabled on this deployment: polling will not change that.
            clearInterval(timer);
            setStatus("unavailable");
          } else setStatus("error");
        });
    setStatus("loading");
    void read();
    const timer = setInterval(() => {
      if (!document.hidden) void read();
    }, POLL_MS);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, [kind, window, limit, attempt, onUnauthorized]);
  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  return { data, status, error, reload };
}
