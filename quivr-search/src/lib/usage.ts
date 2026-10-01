// The usage section's arithmetic (THE-798): the engine's sparse buckets
// regrouped into columns aligned on the reader's clock, the series each chart
// stacks, and the French formats of its numbers.
import type {
  CountPoint,
  MatchStatsList,
  ReceivedStatsList,
  SearchStats,
  StatsList,
} from "./adminStats";
import { sourceName } from "./admin";

export type UsageWindow = "24h" | "7d";

const HOUR = 3600000;

/** One column of a chart: [start, end) in epoch milliseconds. */
export interface Bin {
  start: number;
  end: number;
}

/** What a read covers: from, to, and the length of its buckets, in ms. */
export interface Span {
  from: number;
  to: number;
  resolution: number;
}

export const spanOf = (list: {
  from: string;
  to: string;
  resolution_seconds: number;
}): Span => ({
  from: Date.parse(list.from),
  to: Date.parse(list.to),
  resolution: list.resolution_seconds * 1000,
});

const SPANS: Record<UsageWindow, number> = {
  "24h": 24 * HOUR,
  "7d": 7 * 24 * HOUR,
};

/** The hourly span of the top queries, which carry no from and to. */
export const hourlySpan = (window: UsageWindow, now: number): Span => ({
  from: Math.floor((now - SPANS[window]) / HOUR) * HOUR,
  to: now,
  resolution: HOUR,
});

/**
 * The columns of a read: an hour each over 24 h, 6 hours over 7 days, each
 * a whole number of the engine's buckets and aligned on their multiples, so
 * every bucket of the read falls in exactly one column and the columns add
 * up to the read's totals. The first and last columns may be partial.
 */
export function binsOf(span: Span, window: UsageWindow): Bin[] {
  const width = window === "24h" ? HOUR : 6 * HOUR;
  const size =
    Math.max(1, Math.round(width / span.resolution)) * span.resolution;
  const first = Math.floor(span.from / size) * size;
  const count = Math.max(1, Math.ceil((span.to - first) / size));
  return Array.from({ length: count }, (_, i) => ({
    start: first + i * size,
    end: first + (i + 1) * size,
  }));
}

/** The count of every column, from buckets that start inside it. */
export function countsIn(
  bins: Bin[],
  points: { start: string; count: number }[],
) {
  const out = bins.map(() => 0);
  for (const p of points) {
    const t = Date.parse(p.start);
    const i = bins.findIndex((b) => t >= b.start && t < b.end);
    if (i >= 0) out[i] += p.count;
  }
  return out;
}

/**
 * A row's trend in coarse columns, so a rare query still shows a shape:
 * eight quarters of 3 hours over 24 h, seven days over 7 days.
 */
export function coarse(values: number[], window: UsageWindow) {
  const size = window === "24h" ? 3 : 4;
  const out: number[] = [];
  for (let i = 0; i < values.length; i += size)
    out.push(values.slice(i, i + size).reduce((a, b) => a + b, 0));
  return out;
}

/** A stacked series: its entity, its label and a value per column. */
export interface Series {
  id: string;
  label: string;
  /** Categorical slot, 1 to 5, or 0 for "Autres". */
  slot: number;
  values: number[];
  total: number;
}

/** How many sources a chart stacks before it folds the rest into Autres. */
export const STACKED_SOURCES = 5;

/**
 * Documents received as stacked series: the largest sources, each keeping
 * the colour slot it was first given, then the next ones folded into
 * "Autres". `slots` remembers namespace → slot across reads, so a source
 * keeps its colour when the window or the ranking changes. The read lists
 * at most its limit of sources: the ones past it count in the "Autres"
 * total of the legend, not in the columns.
 */
export function sourceSeries(
  list: ReceivedStatsList,
  bins: Bin[],
  slots: Map<string, number>,
): { series: Series[]; others: { sources: number; total: number } } {
  const shown = list.items.slice(0, STACKED_SOURCES);
  const used = new Set(
    shown.map((i) => slots.get(i.source_namespace)).filter(Boolean),
  );
  for (const item of shown) {
    if (slots.has(item.source_namespace)) continue;
    let slot = 1;
    while (used.has(slot)) slot++;
    slots.set(item.source_namespace, slot);
    used.add(slot);
  }
  const series: Series[] = shown.map((item) => ({
    id: item.source_namespace,
    label: sourceName(item.source_namespace),
    slot: slots.get(item.source_namespace) || 1,
    values: countsIn(bins, item.points),
    total: item.count,
  }));
  const rest = list.items.slice(STACKED_SOURCES);
  const others = {
    sources: Math.max(0, list.sources - shown.length),
    total: Math.max(0, list.total - shown.reduce((sum, i) => sum + i.count, 0)),
  };
  if (others.total > 0)
    series.push({
      id: "",
      label: "Autres sources",
      slot: 0,
      values: countsIn(
        bins,
        rest.flatMap((i) => i.points),
      ),
      total: others.total,
    });
  return { series, others };
}

export const MODES = [
  { mode: "hybrid", label: "Idées proches", hint: "hybride", slot: 1 },
  { mode: "lexical", label: "Mots exacts", hint: "lexicale", slot: 2 },
  { mode: "semantic", label: "Sens", hint: "sémantique", slot: 3 },
] as const;

export interface ModeRow {
  mode: string;
  label: string;
  hint: string;
  slot: number;
  count: number;
  errors: number;
  /** Searches slower than their profile's latency objective. */
  overObjective: number;
  p50?: number;
  p95?: number;
  /** p95 of every column, undefined where nobody searched. */
  p95s: (number | undefined)[];
}

/**
 * Searches per mode, every profile of a mode together. A mode's p50 and
 * p95 over the window are the busiest profile's (profiles are rarely mixed
 * within a mode); a column's p95 is the slowest bucket inside it, so a spike
 * is never averaged away.
 */
export function modeRows(list: StatsList<SearchStats>, bins: Bin[]) {
  const rows: ModeRow[] = [];
  for (const m of MODES) {
    const items = list.items.filter((i) => i.mode === m.mode);
    if (!items.length) continue;
    const busiest = items.reduce((a, b) =>
      b.summary.count > a.summary.count ? b : a,
    );
    const p95s: (number | undefined)[] = bins.map(() => undefined);
    for (const item of items)
      for (const p of item.points) {
        const t = Date.parse(p.start);
        const i = bins.findIndex((b) => t >= b.start && t < b.end);
        if (i >= 0) p95s[i] = Math.max(p95s[i] ?? 0, p.p95_ms);
      }
    rows.push({
      ...m,
      count: items.reduce((sum, i) => sum + i.summary.count, 0),
      errors: items.reduce((sum, i) => sum + i.summary.errors, 0),
      overObjective: items.reduce((sum, i) => sum + i.over_objective, 0),
      p50: busiest.summary.p50_ms,
      p95: busiest.summary.p95_ms,
      p95s,
    });
  }
  return rows;
}

/** The searches of a mode per column, for the stacked chart. */
export const modeSeries = (
  list: StatsList<SearchStats>,
  bins: Bin[],
  rows: ModeRow[],
): Series[] =>
  rows.map((r) => {
    const points = list.items
      .filter((i) => i.mode === r.mode)
      .flatMap((i) => i.points);
    return {
      id: r.mode,
      label: r.label,
      slot: r.slot,
      values: countsIn(bins, points),
      total: r.count,
    };
  });

/** Matches of every evaluator per column. */
export const matchValues = (list: MatchStatsList, bins: Bin[]) =>
  countsIn(
    bins,
    list.items.flatMap((i): CountPoint[] => i.points),
  );

const number = new Intl.NumberFormat("fr-FR");
/** 1 284, with a narrow no-break space. */
export const count = (n: number) => number.format(n);
export const plural = (n: number, one: string, many = one + "s") =>
  `${count(n)} ${n > 1 ? many : one}`;

const time = new Intl.DateTimeFormat("fr-FR", {
  hour: "2-digit",
  minute: "2-digit",
});
const day = new Intl.DateTimeFormat("fr-FR", {
  weekday: "short",
  day: "numeric",
});
const dayTime = new Intl.DateTimeFormat("fr-FR", {
  weekday: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});

/** "14:00–15:00" or "mar. 29, 06:00–12:00". */
export function binRange(bin: Bin, window: UsageWindow) {
  return window === "24h"
    ? `${time.format(bin.start)}–${time.format(bin.end)}`
    : `${dayTime.format(bin.start)}–${time.format(bin.end)}`;
}

/**
 * Axis ticks: the columns starting at a local hour that is a multiple of 6
 * over 24 h, and the columns holding a local midnight over 7 days.
 */
export function ticksOf(bins: Bin[], window: UsageWindow) {
  return bins.flatMap((b, index) => {
    if (window === "24h") {
      const d = new Date(b.start);
      return d.getMinutes() === 0 && d.getHours() % 6 === 0
        ? [{ index, label: time.format(d) }]
        : [];
    }
    const midnight = new Date(b.start);
    if (midnight.getHours() || midnight.getMinutes())
      midnight.setHours(24, 0, 0, 0);
    return midnight.getTime() < b.end
      ? [{ index, label: day.format(midnight) }]
      : [];
  });
}

export const WINDOW_LABELS: Record<UsageWindow, string> = {
  "24h": "24 h",
  "7d": "7 jours",
};
