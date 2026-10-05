// The documents stored in the demo corpus (THE-1034): the total since the
// beginning and the count of each day, from the facade's
// /demo/admin/history, which reads the engine's exact counts in the reader's
// time zone and caches them. Read-only.
import { useCallback, useEffect, useState } from "react";
import { APIError, request } from "./search";
import type { StatsStatus } from "./adminStats";

export interface History {
  time_zone: string;
  /** Every Record of the corpus, withdrawn ones and undated ones included. */
  total: number;
  /** YYYY-MM-DD, local to time_zone. */
  first_day: string;
  today: string;
  /** Days before first_day exist but are not counted (a year at most). */
  truncated: boolean;
  /** One per day from first_day to today, empty days at zero. */
  days: { day: string; count: number }[];
}

const zone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

export const fetchHistory = (signal?: AbortSignal) =>
  request<History>(
    `/demo/admin/history?tz=${encodeURIComponent(zone())}`,
    undefined,
    signal,
  );

const POLL_MS = 60000;

/** The history, reread every minute while the page is visible. */
export function useHistory(onUnauthorized: () => void) {
  const [data, setData] = useState<History | null>(null);
  const [status, setStatus] = useState<StatsStatus>("loading");
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    const read = () =>
      fetchHistory(controller.signal)
        .then((next) => {
          setData(next);
          setStatus("ready");
        })
        .catch((e) => {
          if (controller.signal.aborted) return;
          if (e instanceof APIError && e.status === 401)
            return onUnauthorized();
          setError(e instanceof Error ? e.message : "Chiffres indisponibles.");
          setStatus("error");
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
  }, [attempt, onUnauthorized]);
  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  return { data, status, error, reload };
}

// A day is a calendar date: formatted in UTC so it never shifts.
const asDate = (day: string) => new Date(`${day}T00:00:00Z`);
const longDay = new Intl.DateTimeFormat("fr-FR", {
  weekday: "short",
  day: "numeric",
  month: "short",
  year: "numeric",
  timeZone: "UTC",
});
const shortDay = new Intl.DateTimeFormat("fr-FR", {
  day: "numeric",
  month: "short",
  timeZone: "UTC",
});
const fullDay = new Intl.DateTimeFormat("fr-FR", {
  day: "numeric",
  month: "long",
  year: "numeric",
  timeZone: "UTC",
});

/** "lun. 5 oct. 2026" */
export const dayLabel = (day: string) => longDay.format(asDate(day));
/** "5 oct." */
export const dayTick = (day: string) => shortDay.format(asDate(day));
/** "5 octobre 2026" */
export const dayName = (day: string) => fullDay.format(asDate(day));
