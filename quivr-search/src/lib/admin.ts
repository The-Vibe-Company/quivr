// The Admin tab through the demo facade (admin.mjs): the demo corpus's latest
// documents with one cell per step, the numbers of the last hour and day, and
// one document's timeline. Read-only; no core credential reaches the browser.
import { useCallback, useEffect, useRef, useState } from "react";
import { APIError, request } from "./search";
import { HAND_NAMESPACE } from "./feed";
import { displayName } from "./sourceNames";

export type CellState = "done" | "slow" | "run" | "todo" | "none" | "error";

/** One step of one document, as the facade judges it. */
export interface Cell {
  key: StepKey;
  state: CellState;
  /** How long the finished step took. */
  ms?: number;
  /** A running step waits from this time. */
  since?: string;
  /** Above this many ms the step is slow: its p95 over the day; null while
   * the step has too little history to say. */
  limit: number | null;
  /** Why a step is not recorded: no_alert when no alert covers the corpus. */
  reason?: "no_alert";
}

export type StepKey = "received" | "cut" | "searchable" | "vectors" | "alerts";

/** The steps in pipeline order, as column heads and in sentences. */
export const STEPS: { key: StepKey; label: string; long: string }[] = [
  { key: "received", label: "Reçu", long: "reçu et lu" },
  { key: "cut", label: "Découpé", long: "découpé" },
  { key: "searchable", label: "Trouvable", long: "trouvable" },
  { key: "vectors", label: "Vecteurs", long: "vecteurs attachés" },
  { key: "alerts", label: "Alertes", long: "alertes décidées" },
];

export interface Steps {
  accepted_at?: string;
  materialized_at?: string;
  segmented_at?: string;
  retrieval_ready_at?: string;
  enriched_at?: string;
  evaluated_at?: string;
  quarantined_at?: string;
  withdrawn_at?: string;
}

export interface AdminDocument {
  version_id: string;
  record_id: string;
  corpus_id: string;
  source_namespace: string;
  record_key: string;
  title?: string;
  state: string;
  is_current: boolean;
  /** A newer Version of the Record took this one's place, as the facade
   * judges it: not current also describes a Version still building. */
  replaced: boolean;
  steps: Steps;
  /** Whether alert evaluation applies; absent from a core before THE-1315. */
  evaluation?: "applicable" | "not_applicable";
}

export interface AdminRow extends AdminDocument {
  flow: Cell[];
}

export interface HourBucket {
  start: string;
  count: number;
  errors: number;
}

export interface AdminStats {
  /** Documents per minute: made searchable over 1 h (engine rollups), or received over 10 min. */
  per_minute: number;
  per_minute_window: "1h" | "10min";
  searchable_p95_ms: number | null;
  searchable_day_p95_ms: number | null;
  waiting: number;
  /** Documents whose running step already exceeds its p95. */
  stuck: number;
  /** Documents running each step now, and since when the oldest waits. */
  waiting_by_step: Record<StepKey, { count: number; oldest_since?: string }>;
  errors: number;
  hours: HourBucket[];
  /** What the hourly bars count: documents made searchable, or received. */
  hours_of: "searchable" | "received";
  /** Hours before this time are not counted (too many documents to scan). */
  counted_from?: string;
}

export interface TimelineStep {
  step: string;
  at: string;
  since?: string;
  duration_ms?: number;
  plugin_id?: string;
  plugin_version?: string;
}

export interface Timeline {
  document: AdminDocument;
  steps: TimelineStep[];
}

export const fetchAdmin = (signal?: AbortSignal) =>
  request<{ documents: AdminRow[]; stats: AdminStats; live: boolean }>(
    "/demo/admin",
    undefined,
    signal,
  );

export const fetchTimeline = (version: string, signal?: AbortSignal) =>
  request<Timeline>(
    `/demo/admin/documents/${encodeURIComponent(version)}/timeline`,
    undefined,
    signal,
  );

/** A Source Namespace as the tab shows it. */
export const sourceName = (namespace: string) =>
  namespace === HAND_NAMESPACE ? "À la main" : displayName(namespace);

/**
 * The title Part, else the Fil's title for the Record (a pasted text's first
 * line, known once it is read), else its key; a pasted text's key is a
 * random identifier, so it says what it is instead.
 */
export const titleOf = (
  doc: {
    title?: string;
    record_id: string;
    record_key: string;
    source_namespace: string;
  },
  titles: Map<string, string>,
) =>
  doc.title ||
  titles.get(doc.record_id) ||
  (doc.source_namespace === HAND_NAMESPACE
    ? "Texte ajouté à la main"
    : doc.record_key);

const decimal = (value: number) => value.toFixed(1).replace(".", ",");
// French keeps a number and its unit on one line.
const nb = (text: string) => text.replace(/ (ms|s|min|h)\b/g, "\u00a0$1");

/** Precise: "12 ms", "1,84 s", "42,1 s", "3 min 05 s", "2 h 10 min" */
export const duration = (ms: number) => nb(precise(ms));
function precise(ms: number) {
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const s = ms / 1000;
  if (s < 10) return `${s.toFixed(2).replace(".", ",")} s`;
  if (s < 60) return `${decimal(s)} s`;
  const m = Math.floor(s / 60);
  if (m < 60)
    return `${m} min ${String(Math.floor(s % 60)).padStart(2, "0")} s`;
  return `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")} min`;
}

/** Compact, for a cell: "640 ms", "1,8 s", "42 s", "3 min", "2 h" */
export const short = (ms: number) => nb(compact(ms));
function compact(ms: number) {
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const s = ms / 1000;
  if (s < 10) return `${decimal(s)} s`;
  if (s < 60) return `${Math.round(s)} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min`;
  return `${Math.floor(s / 3600)} h`;
}

export const acceptedAt = (row: AdminDocument) => row.steps.accepted_at || "";
const newestFirst = (a: AdminDocument, b: AdminDocument) =>
  acceptedAt(b).localeCompare(acceptedAt(a)) ||
  b.version_id.localeCompare(a.version_id);

const LIVE = 50;
const FRESH_MS = 2000;
// Above this many arrivals per second the flow stops animating rows and says
// how many arrived instead.
const BUSY_RATE = 5;
const RATE_WINDOW_MS = 10000;
// Screen readers hear about arrivals at most this often.
const ANNOUNCE_MS = 10000;

type Status = "loading" | "ready" | "unavailable" | "error";

/**
 * The latest documents, newest first, and the stats, kept current from the
 * facade's stream. `connected` is the browser's stream, `live` the facade's
 * own change stream; `lastEvent` dates the latest update that changed a row.
 */
export function useAdminStream(onUnauthorized: () => void) {
  const [rows, setRows] = useState<AdminRow[]>([]);
  const [stats, setStats] = useState<AdminStats | null>(null);
  const [status, setStatus] = useState<Status>("loading");
  const [error, setError] = useState("");
  const [live, setLive] = useState(false);
  const [connected, setConnected] = useState(true);
  const [lastEvent, setLastEvent] = useState(0);
  const [attempt, setAttempt] = useState(0);
  const [fresh, setFresh] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(0);
  const [announcement, setAnnouncement] = useState("");
  const rowsRef = useRef<AdminRow[]>([]);
  const arrivals = useRef<number[]>([]);
  const unannounced = useRef(0);
  const announcedAt = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    let retry: ReturnType<typeof setTimeout>;
    let calm: ReturnType<typeof setTimeout>;
    let announce: ReturnType<typeof setTimeout>;
    let loads = 0;
    // A key without observability:read will not gain it by retrying.
    let refused = false;
    let failed = false;
    const commit = (next: AdminRow[]) => {
      rowsRef.current = next;
      setRows(next);
    };
    const load = () => {
      const ticket = ++loads;
      return fetchAdmin(controller.signal)
        .then((data) => {
          if (ticket !== loads) return;
          failed = false;
          // Steps only accumulate: a row the stream updated while this
          // snapshot was on its way keeps the newer state.
          const shown = new Map(rowsRef.current.map((r) => [r.version_id, r]));
          const newer = (row: AdminRow) => {
            const known = shown.get(row.version_id);
            return known &&
              Object.keys(known.steps).length > Object.keys(row.steps).length
              ? known
              : row;
          };
          commit(data.documents.map(newer).sort(newestFirst).slice(0, LIVE));
          setStats(data.stats);
          // Liveness comes only from the stream's own ordered status events:
          // a snapshot built before the facade's change stream went live
          // would otherwise overwrite the later "live" and leave the badge
          // on "Toutes les 5 s" (seen in CI on #159).
          setStatus("ready");
        })
        .catch((e) => {
          if (controller.signal.aborted || ticket !== loads) return;
          failed = true;
          if (e instanceof APIError && e.status === 401)
            return onUnauthorized();
          if (e instanceof APIError && e.status === 403) {
            refused = true;
            source.close();
            clearTimeout(retry);
          }
          setError(
            e instanceof Error ? e.message : "Le suivi est indisponible.",
          );
          setStatus(
            e instanceof APIError && e.status === 403 ? "unavailable" : "error",
          );
        });
    };
    const say = () => {
      const n = unannounced.current;
      if (!n) return;
      unannounced.current = 0;
      announcedAt.current = Date.now();
      // Cleared first, so the same sentence twice is announced twice.
      setAnnouncement("");
      requestAnimationFrame(() =>
        setAnnouncement(
          `${n} nouveau${n > 1 ? "x" : ""} document${n > 1 ? "s" : ""} dans le flux.`,
        ),
      );
    };
    const source = new EventSource("/demo/admin/stream");
    void load();
    source.onopen = () => {
      setConnected(true);
      void load();
    };
    source.onerror = () => {
      if (refused) return;
      setConnected(false);
      if (source.readyState === EventSource.CLOSED) {
        void load();
        retry = setTimeout(() => {
          if (!refused) setAttempt((n) => n + 1);
        }, 3000);
      }
    };
    source.addEventListener("status", (event) =>
      setLive(JSON.parse((event as MessageEvent).data).live === true),
    );
    source.addEventListener("stats", (event) => {
      setStats(JSON.parse((event as MessageEvent).data));
      // The stream works again: a snapshot that had failed is reread.
      if (failed) void load();
    });
    source.addEventListener("documents", (event) => {
      const items: AdminRow[] = JSON.parse((event as MessageEvent).data).items;
      const known = new Set(rowsRef.current.map((r) => r.version_id));
      const added = items.filter((r) => !known.has(r.version_id));
      const changed = new Set(items.map((r) => r.version_id));
      commit(
        [...rowsRef.current.filter((r) => !changed.has(r.version_id)), ...items]
          .sort(newestFirst)
          .slice(0, LIVE),
      );
      const now = Date.now();
      setLastEvent(now);
      arrivals.current = [
        ...arrivals.current.filter((t) => now - t < RATE_WINDOW_MS),
        ...added.map(() => now),
      ];
      const count = arrivals.current.length;
      const crowded = count > (BUSY_RATE * RATE_WINDOW_MS) / 1000;
      setBusy(crowded ? count : 0);
      clearTimeout(calm);
      if (crowded) calm = setTimeout(() => setBusy(0), RATE_WINDOW_MS);
      if (!added.length) return;
      unannounced.current += added.length;
      clearTimeout(announce);
      announce = setTimeout(
        say,
        Math.max(0, ANNOUNCE_MS - (now - announcedAt.current)),
      );
      if (crowded) return;
      const newIds = added.map((r) => r.version_id);
      setFresh((current) => new Set([...current, ...newIds]));
      setTimeout(
        () =>
          setFresh((current) => {
            const next = new Set(current);
            newIds.forEach((id) => next.delete(id));
            return next;
          }),
        FRESH_MS,
      );
    });
    return () => {
      source.close();
      controller.abort();
      clearTimeout(retry);
      clearTimeout(calm);
      clearTimeout(announce);
    };
  }, [attempt, onUnauthorized]);

  const reload = useCallback(() => {
    setStatus("loading");
    setAttempt((n) => n + 1);
  }, []);

  return {
    rows,
    stats,
    status,
    error,
    live,
    connected,
    lastEvent,
    fresh,
    busy,
    announcement,
    reload,
  };
}

export type Health = "ok" | "quiet" | "warn" | "bad";

/** The last hour's received → searchable p95 is well above the day's. */
export const slower = (stats: AdminStats) =>
  stats.searchable_p95_ms !== null &&
  stats.searchable_day_p95_ms !== null &&
  stats.searchable_p95_ms > 2000 &&
  stats.searchable_p95_ms > 1.5 * stats.searchable_day_p95_ms;

/** Whether everything is healthy right now, in one sentence. */
export function verdict(stats: AdminStats): { tone: Health; text: string } {
  const p95 = stats.searchable_p95_ms;
  const usual = stats.searchable_day_p95_ms;
  const docs = (n: number) => `${n} document${n > 1 ? "s" : ""}`;
  if (stats.errors)
    return {
      tone: "bad",
      text: `${docs(stats.errors)} en erreur dans la dernière heure : ${stats.errors > 1 ? "ils ne sont pas trouvables" : "il n’est pas trouvable"}.`,
    };
  if (stats.stuck)
    return {
      tone: "warn",
      text: `${docs(stats.stuck)} ${stats.stuck > 1 ? "attendent" : "attend"} plus longtemps que d’habitude à une étape.`,
    };
  if (slower(stats))
    return {
      tone: "warn",
      text: `Plus lent que d’habitude : trouvable en ${short(p95!)} (p95 sur 1\u00a0h), contre ${short(usual!)} sur 24\u00a0h.`,
    };
  if (!stats.per_minute && !stats.waiting && p95 === null)
    return {
      tone: "quiet",
      text: "Calme : aucun document reçu depuis une heure.",
    };
  return {
    tone: "ok",
    text:
      p95 === null
        ? "Tout va bien : les documents avancent normalement."
        : `Tout va bien : les documents deviennent trouvables en ${short(p95)} (p95 sur 1\u00a0h).`,
  };
}
