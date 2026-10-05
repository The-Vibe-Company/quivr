// How the Admin tab judges the pipeline and the plugins (THE-797), from the
// engine's rollups (THE-795): which step slows documents down now, and
// whether each plugin is healthy, degraded, down or idle. Pure functions, so
// the thresholds have one owner test (tests/health.spec.ts).
import {
  fillPoints,
  type PluginCallStats,
  type StatsPoint,
  type StatsWindow,
  type StepStats,
} from "./adminStats";
import { STEPS, short, type StepKey } from "./admin";

type List<T> = {
  window: StatsWindow;
  resolution_seconds: number;
  from: string;
  to: string;
  items: T[];
};

const MINUTE = 60000;
export const WINDOW_MINUTES: Record<StatsWindow, number> = {
  "1h": 60,
  "24h": 24 * 60,
  "7d": 7 * 24 * 60,
};
/** What "now" means in each window: its last few buckets. */
export const RECENT_MINUTES: Record<StatsWindow, number> = {
  "1h": 10,
  "24h": 120,
  "7d": 12 * 60,
};
const RECENT_LABEL: Record<StatsWindow, string> = {
  "1h": "ces 10 dernières minutes",
  "24h": "ces 2 dernières heures",
  "7d": "ces 12 dernières heures",
};

/**
 * A queue is named once its oldest document waits this long, and twice the
 * step's p95: a document waiting a few seconds is ordinary work.
 */
export const QUEUE_MS = 60000;
/** A step is slow when its recent p95 is this many times its usual one. */
export const SLOW_RATIO = 2;
// Fewer calls than these, recent or usual, are too few to judge a step by.
const MIN_RECENT = 3;
const MIN_USUAL = 10;

/**
 * The steps in pipeline order, with the step rollup that times each one
 * (from the step that causes it, waiting included). Alert decisions have no
 * step rollup: their row reads the calls to the alert plugins. `floor` keeps
 * a 90 ms step beside 40 ms ones from being called slow.
 */
export const PIPELINE: {
  key: StepKey;
  rollup?: string;
  operation?: string;
  floor: number;
}[] = [
  { key: "received", rollup: "materialized", floor: 500 },
  { key: "cut", rollup: "segmented", floor: 500 },
  { key: "searchable", rollup: "retrieval_ready", floor: 500 },
  { key: "vectors", rollup: "enriched", floor: 1000 },
  { key: "alerts", operation: "evaluate_subscription", floor: 1000 },
];

export const stepLabel = (key: StepKey) =>
  STEPS.find((s) => s.key === key)?.label || key;

/** Documents waiting at one step, from the live flow (admin.mjs). */
export interface Waiting {
  count: number;
  oldest_since?: string;
}

export interface StepRow {
  key: StepKey;
  label: string;
  /** Call time, not waiting included: the alerts row. */
  calls: boolean;
  count: number;
  p50?: number;
  p95?: number;
  /** p95 over the window's last buckets, and over the buckets before them. */
  recent?: number;
  usual?: number;
  slow: boolean;
  waiting: Waiting;
}

/** Count-weighted mean of the buckets' p95, the rollups' best estimate. */
function weighted(points: StatsPoint[], field: "p50_ms" | "p95_ms") {
  let n = 0;
  let sum = 0;
  for (const p of points)
    if (p.count > 0) {
      n += p.count;
      sum += p.count * p[field];
    }
  return { n, value: n ? sum / n : undefined };
}

/** When a bucket of a plugin's spark starts, as its tooltip reads it. */
const bucketTime = new Intl.DateTimeFormat("fr-FR", {
  weekday: "short",
  hour: "2-digit",
  minute: "2-digit",
});

/** Sums several series bucket by bucket; latencies are count-weighted. */
export function mergePoints(
  list: Omit<List<unknown>, "items">,
  series: { points: StatsPoint[] }[],
) {
  const filled = series.map((s) => fillPoints(list, s.points));
  if (!filled.length) return fillPoints(list, []);
  return filled[0].map((first, i) => {
    const at = filled.map((f) => f[i]);
    const count = at.reduce((n, p) => n + p.count, 0);
    const errors = at.reduce((n, p) => n + p.errors, 0);
    return {
      start: first.start,
      count,
      errors,
      p50_ms: weighted(at, "p50_ms").value ?? 0,
      p95_ms: weighted(at, "p95_ms").value ?? 0,
    };
  });
}

/** Splits filled buckets into the recent ones and those before. */
function split(points: StatsPoint[], window: StatsWindow, to: string) {
  const cut = Date.parse(to) - RECENT_MINUTES[window] * MINUTE;
  return {
    recent: points.filter((p) => Date.parse(p.start) >= cut),
    before: points.filter((p) => Date.parse(p.start) < cut),
  };
}

/** One row per pipeline step, judged against the step's own usual time. */
export function stepRows(
  steps: List<StepStats> | null,
  plugins: List<PluginCallStats> | null,
  waiting: Partial<Record<StepKey, Waiting>> = {},
): StepRow[] {
  return PIPELINE.map((step) => {
    const list = step.rollup ? steps : plugins;
    const series = !list
      ? []
      : step.rollup
        ? (steps?.items || []).filter((s) => s.step === step.rollup)
        : (plugins?.items || []).filter((s) => s.operation === step.operation);
    const row: StepRow = {
      key: step.key,
      label: stepLabel(step.key),
      calls: !step.rollup,
      count: series.reduce((n, s) => n + s.summary.count, 0),
      slow: false,
      waiting: waiting[step.key] || { count: 0 },
    };
    if (!list || !row.count) return row;
    const counted = series.filter((s) => s.summary.count > 0);
    // One series per plugin for alerts: the busiest one's median, the worst p95.
    const busiest = counted.reduce((a, b) =>
      b.summary.count > a.summary.count ? b : a,
    );
    row.p50 = busiest.summary.p50_ms;
    row.p95 = Math.max(...counted.map((s) => s.summary.p95_ms ?? 0));
    const { recent, before } = split(
      mergePoints(list, counted),
      list.window,
      list.to,
    );
    const r = weighted(recent, "p95_ms");
    const u = weighted(before, "p95_ms");
    if (r.n >= MIN_RECENT) row.recent = r.value;
    if (u.n >= MIN_USUAL) row.usual = u.value;
    row.slow =
      row.recent !== undefined &&
      row.usual !== undefined &&
      row.recent >= step.floor &&
      row.recent >= SLOW_RATIO * row.usual;
    return row;
  });
}

export interface Verdict {
  tone: "ok" | "warn" | "quiet";
  /** The step named, for the chip on its row. */
  step?: StepKey;
  text: string;
}

const docs = (n: number) =>
  `${n.toLocaleString("fr-FR")} document${n > 1 ? "s" : ""}`;
const since = (at: string | undefined, now: number) => {
  if (!at) return "";
  const ms = Math.max(0, now - Date.parse(at));
  return ms < MINUTE ? "moins d’une minute" : short(ms);
};

/**
 * The one sentence under the Bottlenecks title: the slowed step, else a
 * queue that waits longer than the step usually takes, else that everything
 * flows and which step is the longest.
 */
export function bottleneck(
  rows: StepRow[],
  window: StatsWindow,
  now: number,
): Verdict {
  const slow = rows
    .filter((r) => r.slow)
    .sort(
      (a, b) =>
        (b.recent ?? 0) / Math.max(b.usual ?? 1, 1) -
        (a.recent ?? 0) / Math.max(a.usual ?? 1, 1),
    )[0];
  if (slow) {
    const wait = slow.waiting.count
      ? ` ${docs(slow.waiting.count)} ${slow.waiting.count > 1 ? "attendent" : "attend"} à cette étape.`
      : "";
    return {
      tone: "warn",
      step: slow.key,
      text: `L’étape « ${slow.label} » ralentit tout : ${short(slow.recent!)} au p95 ${RECENT_LABEL[window]}, contre ${short(slow.usual!)} d’habitude.${wait}`,
    };
  }
  const queued = rows
    .filter(
      (r) =>
        r.waiting.count > 0 &&
        r.waiting.oldest_since &&
        now - Date.parse(r.waiting.oldest_since) >
          Math.max(2 * (r.p95 ?? 0), QUEUE_MS),
    )
    .sort(
      (a, b) =>
        Date.parse(a.waiting.oldest_since!) -
        Date.parse(b.waiting.oldest_since!),
    )[0];
  if (queued)
    return {
      tone: "warn",
      step: queued.key,
      text:
        queued.waiting.count > 1
          ? `Des documents s’accumulent à l’étape « ${queued.label} » : ${docs(queued.waiting.count)} en attente, le plus ancien depuis ${since(queued.waiting.oldest_since, now)}.`
          : `Un document attend à l’étape « ${queued.label} » depuis ${since(queued.waiting.oldest_since, now)}, plus longtemps que d’habitude.`,
    };
  const timed = rows.filter((r) => r.p95 !== undefined && !r.calls);
  if (!timed.length)
    return {
      tone: "quiet",
      text: "Pas encore de document traité sur cette période : les durées de chaque étape apparaîtront au premier.",
    };
  const longest = timed.reduce((a, b) => (b.p95! > a.p95! ? b : a));
  return {
    tone: "ok",
    step: longest.key,
    text: `Tout s’écoule normalement. L’étape la plus longue est « ${longest.label} », ${short(longest.p95!)} au p95${longest.usual === undefined ? "" : ", comme d’habitude"}.`,
  };
}

// Plugins

export type PluginState = "ok" | "degraded" | "down" | "idle";

/** How far back "now" reaches for a plugin's state, in the 1 h rollup. */
export const NOW_MINUTES = 5;
/** Recent error share above which a plugin is degraded, and down. */
export const DEGRADED_ERRORS = 0.05;
export const DOWN_ERRORS = 0.5;
/** Fewer failed calls than this are not enough to call a plugin down. */
export const DOWN_CALLS = 3;

export interface ActivePlugin {
  plugin_id: string;
  version: string;
  roles: string[];
}

export interface OperationRow {
  operation: string;
  version: string;
  count: number;
  errors: number;
  p50?: number;
  p95?: number;
  last_error_code?: string;
  last_error_at?: string;
}

export interface PluginRow {
  plugin_id: string;
  version?: string;
  /** Other versions that answered calls during the window. */
  others: string[];
  roles: string[];
  state: PluginState;
  /** Why the state, in a few words: "3 appels sur 4 échouent". */
  reason: string;
  per_minute: number;
  p95?: number;
  error_rate: number;
  count: number;
  errors: number;
  last_error_code?: string;
  last_error_at?: string;
  /** Error share per bucket of the window, oldest first; null when idle. */
  spark: (number | null)[];
  /** What each bucket reads on hover: when, how many calls, how many failed. */
  sparkTips: string[];
  operations: OperationRow[];
}

const latest = <T extends { last_error_at?: string }>(items: T[]) =>
  items
    .filter((i) => i.last_error_at)
    .sort((a, b) => b.last_error_at!.localeCompare(a.last_error_at!))[0];

const calls = (n: number) =>
  `${n.toLocaleString("fr-FR")} appel${n > 1 ? "s" : ""}`;

/**
 * The state of one plugin now, from its minutes with calls among the last
 * NOW_MINUTES of the 1 h rollup: down when most calls of its latest minute
 * fail, degraded when enough of its latest two minutes' calls fail, idle
 * without a call, healthy otherwise. A plugin whose calls succeed again is
 * healthy at once, and says it recovered.
 */
export function pluginState(
  hour: List<PluginCallStats> | null,
  series: PluginCallStats[],
): { state: PluginState; reason: string } {
  if (!hour) return { state: "idle", reason: "" };
  const cut = Date.parse(hour.to) - NOW_MINUTES * MINUTE;
  const recent = mergePoints(hour, series).filter(
    (p) => Date.parse(p.start) >= cut && p.count > 0,
  );
  if (!recent.length)
    return { state: "idle", reason: `Aucun appel depuis ${NOW_MINUTES} min.` };
  const sum = (points: StatsPoint[]) => ({
    count: points.reduce((n, p) => n + p.count, 0),
    errors: points.reduce((n, p) => n + p.errors, 0),
  });
  const all = sum(recent);
  const latest = sum(recent.slice(-2));
  const last = recent[recent.length - 1];
  const failing = `${calls(latest.errors)} sur ${latest.count.toLocaleString("fr-FR")} en échec ces dernières minutes.`;
  // One failed call of a rarely called plugin is not an outage: down needs
  // a few failed calls over the latest two minutes.
  if (
    latest.errors >= DOWN_CALLS &&
    last.errors / last.count >= DOWN_ERRORS &&
    latest.errors / latest.count >= DOWN_ERRORS
  )
    return { state: "down", reason: failing };
  if (latest.errors / latest.count >= DEGRADED_ERRORS)
    return { state: "degraded", reason: failing };
  return {
    state: "ok",
    reason: all.errors
      ? `Rétabli : les appels réussissent de nouveau, après ${calls(all.errors)} en échec ces ${NOW_MINUTES} dernières minutes.`
      : `${calls(all.count)} réussi${all.count > 1 ? "s" : ""} ces ${NOW_MINUTES} dernières minutes.`,
  };
}

/**
 * One row per plugin: the active plan's plugins first, then any other plugin
 * that answered calls during the window (a version since replaced).
 */
export function pluginRows(
  active: ActivePlugin[],
  window: List<PluginCallStats> | null,
  hour: List<PluginCallStats> | null,
): PluginRow[] {
  const ids = [
    ...new Set([
      ...active.map((a) => a.plugin_id),
      ...(window?.items || []).map((i) => i.plugin_id),
    ]),
  ];
  const minutes = window ? WINDOW_MINUTES[window.window] : 60;
  return ids.map((id) => {
    const plan = active.filter((a) => a.plugin_id === id);
    const series = (window?.items || []).filter((i) => i.plugin_id === id);
    const now = (hour?.items || []).filter((i) => i.plugin_id === id);
    const count = series.reduce((n, s) => n + s.summary.count, 0);
    const errors = series.reduce((n, s) => n + s.summary.errors, 0);
    const operations: OperationRow[] = series
      .map((s) => ({
        operation: s.operation,
        version: s.plugin_version,
        count: s.summary.count,
        errors: s.summary.errors,
        p50: s.summary.p50_ms,
        p95: s.summary.p95_ms,
        last_error_code: s.summary.last_error_code,
        last_error_at: s.summary.last_error_at,
      }))
      .sort((a, b) => b.count - a.count);
    const main = operations.find((o) => o.count > 0);
    const versions = [...new Set(plan.map((p) => p.version))];
    const version = versions.join(", ") || undefined;
    const { state, reason } = pluginState(hour, now);
    const last = latest(operations);
    const points = window ? mergePoints(window, series) : [];
    const spark = points.map((p) => (p.count ? p.errors / p.count : null));
    const sparkTips = points.map(
      (p) =>
        `${bucketTime.format(new Date(p.start))} · ${
          p.count
            ? `${p.count} appel${p.count > 1 ? "s" : ""}, ${Math.round((p.errors / p.count) * 100)} % d’erreurs`
            : "aucun appel"
        }`,
    );
    return {
      plugin_id: id,
      version,
      others: [
        ...new Set(
          series
            .map((s) => s.plugin_version)
            .filter((v) => !versions.includes(v)),
        ),
      ],
      roles: [...new Set(plan.flatMap((p) => p.roles))],
      state,
      reason,
      per_minute: count / minutes,
      p95: main?.p95,
      error_rate: count ? errors / count : 0,
      count,
      errors,
      last_error_code: last?.last_error_code,
      last_error_at: last?.last_error_at,
      spark,
      sparkTips,
      operations,
    };
  });
}

const STATE_ORDER: Record<PluginState, number> = {
  down: 0,
  degraded: 1,
  ok: 2,
  idle: 3,
};
/** Problems first, then by name. */
export const byConcern = (a: PluginRow, b: PluginRow) =>
  STATE_ORDER[a.state] - STATE_ORDER[b.state] ||
  a.plugin_id.localeCompare(b.plugin_id);

/** What a plugin does, from the roles it serves, in plain words. */
export function roleLabel(roles: string[]) {
  const words = new Set<string>();
  for (const role of roles) {
    const [kind, detail] = role.split(":", 2);
    if (kind === "ingestion") words.add("Découpage et vecteurs");
    else if (kind === "retrieval") words.add("Recherche");
    else if (kind === "normalizer") words.add("Lecture de fichiers");
    else if (kind === "subscription") words.add("Alertes");
    else if (kind === "connector") words.add(`Collecte ${detail || ""}`.trim());
    else words.add(role);
  }
  return [...words].join(" · ");
}

export const OPERATIONS: Record<string, string> = {
  normalize: "Lecture d’un fichier",
  segment_and_embed: "Découpage et vecteurs",
  embed_query: "Vecteurs d’une recherche",
  search_round: "Recherche",
  connector_fetch: "Relevé d’une source",
  connector_receive: "Réception en direct",
  check_credential: "Vérification d’un accès",
  describe_attachment: "Pièce jointe",
  upload_attachment: "Pièce jointe",
  evaluate_subscription: "Décision d’alerte",
};

/** What an error code means for the operator, when the engine names it. */
export function errorMeaning(code: string) {
  switch (code) {
    case "plugin_unavailable":
      return "Le plugin ne répond pas : il est arrêté, injoignable ou trop lent. Quivr réessaie tout seul.";
    case "invalid_output":
      return "Le plugin a répondu, mais sa réponse ne respecte pas le contrat : Quivr l’a écartée.";
    default:
      return "Code d’erreur renvoyé par le plugin lui-même : sa documentation l’explique.";
  }
}
