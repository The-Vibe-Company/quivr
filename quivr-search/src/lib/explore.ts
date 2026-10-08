// The Explorer through the demo facade (explore.mjs): documents of the
// corpora picked, newest first or as a search ranks them, narrowed by
// metadata; the values each field offers and the timeline; and one document
// with all it holds. The view's state lives in the address (THE-1204).
import { request } from "./search.ts";
import { diffWords } from "./diff.ts";
import type { FeedItem } from "./feed";
import type { Corpus, Exclusion, Field } from "./corpora";
import type { Availability, SearchResponse } from "../types";

export type Scalar = string | number | boolean;

export interface ExploreItem extends FeedItem {
  /** Each field's values in the document's current Version, by filter name. */
  metadata: Record<string, Scalar[]>;
  /** Its place among the Versions the demo read of it: 1 for the first. */
  version?: number;
}

export interface ExplorePage {
  items: ExploreItem[];
  next_cursor?: string;
  excluded_corpora?: Exclusion[];
  retrieval_profile?: SearchResponse["retrieval_profile"];
  /** A search that reached its bound: more documents may match. */
  bounded?: boolean;
}

export type Interval = "day" | "month" | "year";

export interface Facet {
  field: string;
  type: Field["type"];
  /**
   * A date field's step: its values are periods ("2026", "2026-10",
   * "2026-10-05") in time order. Other fields' come most frequent first.
   */
  interval?: Interval;
  values: { value: Scalar; count: number }[];
}

export interface Facets {
  fields: Facet[];
  /** The corpora the active filters leave out, as the engine reports them. */
  excluded_corpora?: Exclusion[];
  /** The dated documents every filter keeps. */
  total?: number;
}

/** A predicate on a field, as the engine takes it. */
export interface MetadataFilter {
  field: string;
  any_of?: Scalar[];
  gte?: string;
  lte?: string;
}

export interface Part {
  key: string;
  parent_key?: string;
  role: string;
  content: { kind: "text"; text: string } | { kind: "blob"; blob_id: string; media_type: string };
}

export interface VersionDetail {
  record_id: string;
  version_id: string;
  accepted_at?: string;
  manifest: { parts: Part[] };
  extensions?: Record<string, { schema_version?: string; data?: unknown }>;
  provenance?: {
    source_blob_ids?: string[];
    producer?: string;
    producer_version?: string;
  };
  availability: Availability;
}

export interface Blob {
  blob_id: string;
  size_bytes: number;
  sha256: string;
  media_type: string;
}

export interface RecordDetail {
  record: {
    record_id: string;
    source: { corpus_id: string; namespace: string; record_key: string };
    withdrawn: boolean;
    current_version_id?: string;
  };
  version: VersionDetail | null;
  /** The Versions the demo read of this document, newest first. */
  versions: { version_id: string; accepted_at?: string }[];
  corpus: Corpus;
  blobs: Blob[];
}

/**
 * What is picked in each facet, by field: values as the address keeps them,
 * or one period for a date; typed for the engine by predicatesOf.
 */
export type Selection = Record<string, string[]>;

/** A year, a month or a day, in UTC: "2026", "2026-10", "2026-10-05". */
export const PERIOD = /^\d{4}(-(0[1-9]|1[0-2])(-(0[1-9]|[12]\d|3[01]))?)?$/;

/** A period as the instants that bound it, in UTC. */
export function periodBounds(period: string) {
  const [year, month = 1, day = 1] = period.split("-").map(Number);
  // Date.UTC would read the years 0–99 as 1900–1999.
  const utc = (y: number, m: number, d: number) => new Date(0).setUTCFullYear(y, m, d);
  const next =
    period.length === 4
      ? utc(year + 1, 0, 1)
      : period.length === 7
        ? utc(year, month, 1)
        : utc(year, month - 1, day + 1);
  return {
    gte: new Date(utc(year, month - 1, day)).toISOString(),
    lte: new Date(next - 1).toISOString(),
  };
}

/** The field the timeline counts and its range filters. */
export const TIMELINE_FIELD = "metadata.published_at";

/** A span of whole periods, both included: two days, two months or two years. */
export interface Range {
  from: string;
  to: string;
}

/** A range as the instants that bound it, in UTC. */
export const rangeBounds = ({ from, to }: Range) => ({ gte: periodBounds(from).gte, lte: periodBounds(to).lte });

/** A value as the engine takes it for a field of this type. */
const typed = (value: Scalar, type?: Field["type"]): Scalar =>
  type === "number" && typeof value === "string" && value.trim() && !Number.isNaN(Number(value))
    ? Number(value)
    : type === "boolean" && typeof value === "string"
      ? value === "true"
      : value;

/**
 * The engine's predicates for what is picked: a value list per field, one
 * period for another date field, and the timeline's range.
 */
export function predicatesOf(selection: Selection, types: Map<string, Field["type"]>, range?: Range) {
  const out = Object.entries(selection)
    // A field of unknown type cannot be filtered on yet, and a date value
    // that is not a period cannot bound one: both are left out.
    .filter(
      ([field, values]) =>
        values.length &&
        field !== TIMELINE_FIELD &&
        types.has(field) &&
        (types.get(field) !== "datetime" || PERIOD.test(String(values[0]))),
    )
    .map(([field, values]): MetadataFilter =>
      types.get(field) === "datetime"
        ? { field, ...periodBounds(String(values[0])) }
        : { field, any_of: values.map((v) => typed(v, types.get(field))) },
    );
  if (range) out.push({ field: TIMELINE_FIELD, ...rangeBounds(range) });
  return out.sort((a, b) => a.field.localeCompare(b.field));
}

export type Sort = "recent" | "relevance";

/** What the Explorer shows, as its address keeps it. */
export interface ExplorerState {
  q: string;
  selection: Selection;
  range?: Range;
  /** The span the timeline shows, when narrowed to it. */
  window?: Range;
  sort: Sort;
  /** The document in the preview. */
  selected: string | null;
}

// A period that names a real day: "2026-02-31" does not.
const real = (period: string) => periodBounds(period).gte.slice(0, period.length) === period;

const rangeOf = (raw: string | null): Range | undefined => {
  const [from, to, ...rest] = (raw || "").split("..");
  return !rest.length &&
    from &&
    to &&
    PERIOD.test(from) &&
    PERIOD.test(to) &&
    from.length === to.length &&
    real(from) &&
    real(to) &&
    from <= to
    ? { from, to }
    : undefined;
};

/** The Explorer's state from its address: q, f=field:value…, from..to, window, sort, selected. */
export function readState(params: URLSearchParams): ExplorerState {
  const selection: Selection = {};
  for (const pick of params.getAll("f")) {
    const at = pick.indexOf(":");
    if (at <= 0) continue;
    const field = pick.slice(0, at);
    const value = pick.slice(at + 1);
    if (!value || field === TIMELINE_FIELD) continue;
    const values = (selection[field] ||= []);
    if (!values.includes(value)) values.push(value);
  }
  return {
    q: params.get("q") || "",
    selection,
    range: rangeOf(params.get("range")),
    window: rangeOf(params.get("window")),
    sort: params.get("sort") === "relevance" ? "relevance" : "recent",
    selected: params.get("selected"),
  };
}

/** The address of a state, defaults left out. */
export function writeState(state: ExplorerState) {
  const p = new URLSearchParams();
  if (state.q) p.set("q", state.q);
  for (const [field, values] of Object.entries(state.selection))
    for (const value of values) p.append("f", `${field}:${value}`);
  if (state.range) p.set("range", `${state.range.from}..${state.range.to}`);
  if (state.window) p.set("window", `${state.window.from}..${state.window.to}`);
  if (state.sort !== "recent") p.set("sort", state.sort);
  if (state.selected) p.set("selected", state.selected);
  return p;
}

const query = (corpora: string[], predicates: MetadataFilter[], extra: Record<string, string> = {}) => {
  const params = new URLSearchParams({ corpora: corpora.join(","), ...extra });
  if (predicates.length) params.set("metadata", JSON.stringify(predicates));
  return params;
};

export const fetchExplore = (
  corpora: string[],
  predicates: MetadataFilter[],
  { cursor, q }: { cursor?: string; q?: string },
  signal?: AbortSignal,
) =>
  request<ExplorePage>(
    `/demo/explore?${query(corpora, predicates, { ...(cursor ? { cursor } : {}), ...(q ? { q } : {}) })}`,
    undefined,
    signal,
  );

// The engine may take 25 s per count, and the facade chains a few.
export const fetchFacets = (
  corpora: string[],
  predicates: MetadataFilter[],
  window: Range | undefined,
  signal?: AbortSignal,
) => {
  const bounds = window && rangeBounds(window);
  return request<Facets>(
    `/demo/explore/facets?${query(corpora, predicates, bounds ? { window: `${bounds.gte},${bounds.lte}` } : {})}`,
    undefined,
    signal,
    "GET",
    90000,
  );
};

export const fetchRecordDetail = (id: string, signal?: AbortSignal) =>
  request<RecordDetail>(`/demo/explore/records/${encodeURIComponent(id)}`, undefined, signal);

/** "2026", "octobre 2026" or "5 octobre 2026" for a period. */
export const periodLabel = (period: string) =>
  period.length === 4
    ? period
    : new Date(`${period.length === 7 ? `${period}-01` : period}T00:00:00Z`).toLocaleDateString("fr-FR", {
        ...(period.length === 7 ? {} : { day: "numeric" }),
        month: "long",
        year: "numeric",
        timeZone: "UTC",
      });

const SHORT: Record<number, Intl.DateTimeFormatOptions> = {
  4: {},
  7: { month: "short" },
  10: { day: "numeric", month: "short" },
};

/** A period, short: "2026", "oct. 2026", "5 oct. 2026"; the year left out on request. */
export function shortPeriod(period: string, withYear = true) {
  if (period.length === 4) return period;
  const at = new Date(`${period.length === 7 ? `${period}-01` : period}T00:00:00Z`);
  return at.toLocaleDateString("fr-FR", { ...SHORT[period.length], ...(withYear ? { year: "numeric" } : {}), timeZone: "UTC" });
}

/** "3 – 5 oct. 2026", "sept. – oct. 2026", or one period alone. */
export function rangeLabel({ from, to }: Range) {
  if (from === to) return shortPeriod(from);
  // Two days of one month name it once: "3 – 5 oct. 2026".
  if (from.length === 10 && from.slice(0, 7) === to.slice(0, 7)) return `${Number(from.slice(8))} – ${shortPeriod(to)}`;
  return `${shortPeriod(from, from.slice(0, 4) !== to.slice(0, 4))} – ${shortPeriod(to)}`;
}

/** "12 400" for a count. */
export const countLabel = (n: number) => n.toLocaleString("fr-FR");

const languages = new Intl.DisplayNames(["fr"], { type: "language", fallback: "code" });

/** A value as a facet or the metadata table shows it: a language by its name. */
export function valueLabel(value: Scalar, type?: Field["type"], field?: string) {
  if (typeof value === "boolean") return value ? "Oui" : "Non";
  if (field === "metadata.language" && typeof value === "string")
    try {
      return languages.of(value) || value;
    } catch {
      return value;
    }
  if (type === "datetime" && typeof value === "string") {
    if (PERIOD.test(value)) return periodLabel(value);
    const at = Date.parse(value);
    if (!Number.isNaN(at))
      return new Date(at).toLocaleString("fr-FR", { dateStyle: "long", timeStyle: "short" });
  }
  return String(value);
}

/** "12,4 Ko" for a size in bytes. */
export function sizeLabel(bytes: number) {
  if (bytes < 1024) return `${bytes} o`;
  const units = ["Ko", "Mo", "Go"];
  let n = bytes / 1024;
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i += 1;
  }
  return `${n.toLocaleString("fr-FR", { maximumFractionDigits: 1 })} ${units[i]}`;
}

export const fetchVersion = (record: string, version: string, signal?: AbortSignal) =>
  request<VersionDetail>(
    `/v0/records/${encodeURIComponent(record)}/versions/${encodeURIComponent(version)}`,
    undefined,
    signal,
  );

/** The value at a JSON Pointer (RFC 6901) in a document, or undefined. */
export function pointerValue(doc: unknown, pointer: string): unknown {
  if (!pointer.startsWith("/")) return undefined;
  let at = doc;
  for (const raw of pointer.slice(1).split("/")) {
    const key = raw.replace(/~1/g, "/").replace(/~0/g, "~");
    if (at === null || typeof at !== "object" || !Object.hasOwn(at, key)) return undefined;
    at = (at as Record<string, unknown>)[key];
  }
  return at;
}

/** A field's values in a Version, as scalars: an array field gives each element. */
export function fieldValues(version: VersionDetail, field: Field): Scalar[] {
  const value = pointerValue(version, field.source_pointer);
  const list = Array.isArray(value) ? value : value === undefined || value === null ? [] : [value];
  return list.filter((v): v is Scalar => ["string", "number", "boolean"].includes(typeof v));
}

/** A Version's title and readable texts, the HTML it was cut from left out. */
export function textsOf(version: VersionDetail) {
  const parts = version.manifest.parts.filter(
    (p): p is Part & { content: { kind: "text"; text: string } } =>
      p.content.kind === "text" && p.role !== "source_html" && !!p.content.text.trim(),
  );
  const title = parts.find((p) => p.role === "title");
  return {
    title: title?.content.text.trim() || "",
    texts: parts.filter((p) => p !== title).map((p) => ({ key: p.key, role: p.role, text: p.content.text })),
  };
}

/** How many words a Version's title and texts hold. */
export function wordCount(version: VersionDetail) {
  const { title, texts } = textsOf(version);
  return [title, ...texts.map((t) => t.text)].join(" ").split(/\s+/).filter(Boolean).length;
}

/**
 * What changed from one Version to the next, in a line: "titre corrigé ·
 * +2 §", "texte retouché (3 mots)", or "sans changement de texte".
 */
export function changeSummary(before: VersionDetail, after: VersionDetail) {
  const a = textsOf(before);
  const b = textsOf(after);
  const out: string[] = [];
  if (a.title.trim() !== b.title.trim()) out.push("titre corrigé");
  const paragraphs = b.texts.length - a.texts.length;
  if (paragraphs) out.push(`${paragraphs > 0 ? "+" : "−"}${Math.abs(paragraphs)} §`);
  else {
    // Words replaced count once: the larger of those added and removed.
    const changes = diffWords(a.texts.map((t) => t.text).join("\n\n"), b.texts.map((t) => t.text).join("\n\n"));
    const words = (kind: string) =>
      changes.filter((c) => c.kind === kind).reduce((n, c) => n + c.text.split(/\s+/).filter(Boolean).length, 0);
    const changed = Math.max(words("added"), words("removed"));
    if (changed) out.push(`texte retouché (${changed} mot${changed > 1 ? "s" : ""})`);
  }
  return out.join(" · ") || "sans changement de texte";
}

const PERIOD_LENGTH: Record<Interval, number> = { year: 4, month: 7, day: 10 };

// Beyond this many periods, the bars are those counted, without the gaps.
const MAX_BARS = 400;

/**
 * The periods between two, both included, at one step: "2026-09",
 * "2026-10"…; undefined when there would be more than MAX_BARS.
 */
export function periodsBetween(first: string, last: string, interval: Interval) {
  const out: string[] = [];
  const at = new Date(`${first.padEnd(10, "-01").slice(0, 10)}T00:00:00Z`);
  const length = PERIOD_LENGTH[interval];
  for (;;) {
    const period = at.toISOString().slice(0, length);
    out.push(period);
    if (period >= last) return out;
    if (out.length >= MAX_BARS) return undefined;
    if (interval === "year") at.setUTCFullYear(at.getUTCFullYear() + 1);
    else if (interval === "month") at.setUTCMonth(at.getUTCMonth() + 1);
    else at.setUTCDate(at.getUTCDate() + 1);
  }
}

/** A range's first and last periods at another step: its days, months or years. */
export const rangeAt = (range: Range, interval: Interval): Range => ({
  from: periodBounds(range.from).gte.slice(0, PERIOD_LENGTH[interval]),
  to: periodBounds(range.to).lte.slice(0, PERIOD_LENGTH[interval]),
});

/** Whether a period and a range share an instant. */
export function overlaps(period: string, range: Range) {
  const p = periodBounds(period);
  const r = rangeBounds(range);
  return p.gte <= r.lte && r.gte <= p.lte;
}
