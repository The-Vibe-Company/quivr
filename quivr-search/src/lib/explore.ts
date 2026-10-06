// The Explorer through the demo facade (explore.mjs): documents of the
// corpora picked, newest first, narrowed by metadata; the values each field
// offers; and one document with all it holds.
import { request } from "./search";
import type { FeedItem } from "./feed";
import type { Corpus, Exclusion, Field } from "./corpora";
import type { Availability } from "../types";

export type Scalar = string | number | boolean;

export interface ExploreItem extends FeedItem {
  /** Each field's values in the document's current Version, by filter name. */
  metadata: Record<string, Scalar[]>;
}

export interface ExplorePage {
  items: ExploreItem[];
  next_cursor?: string;
  excluded_corpora?: Exclusion[];
}

export interface Facet {
  field: string;
  type: Field["type"];
  /** A date field offers months, as "2026-10". */
  values: { value: Scalar; count?: number }[];
}

export interface Facets {
  fields: Facet[];
  /** Whether the values carry counts; until the engine counts them, they do not. */
  counted: boolean;
  /** How many of the newest documents the values were read from. */
  sample: number;
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

/** What is picked in each facet, by field: values, or one month for a date. */
export type Selection = Record<string, Scalar[]>;

/** A month as the instants that bound it, in UTC. */
function monthBounds(month: string) {
  const [year, m] = month.split("-").map(Number);
  return {
    gte: new Date(Date.UTC(year, m - 1, 1)).toISOString(),
    lte: new Date(Date.UTC(year, m, 1) - 1).toISOString(),
  };
}

/** The engine's predicates for what is picked; a date field picks one month. */
export function predicatesOf(selection: Selection, types: Map<string, Field["type"]>) {
  return Object.entries(selection)
    .filter(([, values]) => values.length)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([field, values]): MetadataFilter =>
      types.get(field) === "datetime"
        ? { field, ...monthBounds(String(values[0])) }
        : { field, any_of: values },
    );
}

const query = (corpora: string[], predicates: MetadataFilter[], extra: Record<string, string> = {}) => {
  const params = new URLSearchParams({ corpora: corpora.join(","), ...extra });
  if (predicates.length) params.set("metadata", JSON.stringify(predicates));
  return params;
};

export const fetchExplore = (
  corpora: string[],
  predicates: MetadataFilter[],
  cursor: string | undefined,
  signal?: AbortSignal,
) =>
  request<ExplorePage>(
    `/demo/explore?${query(corpora, predicates, cursor ? { cursor } : {})}`,
    undefined,
    signal,
  );

export const fetchFacets = (corpora: string[], predicates: MetadataFilter[], signal?: AbortSignal) =>
  request<Facets>(`/demo/explore/facets?${query(corpora, predicates)}`, undefined, signal);

export const fetchRecordDetail = (id: string, signal?: AbortSignal) =>
  request<RecordDetail>(`/demo/explore/records/${encodeURIComponent(id)}`, undefined, signal);

/** "octobre 2026" for "2026-10". */
export const monthLabel = (month: string) => {
  const [year, m] = month.split("-").map(Number);
  return new Date(Date.UTC(year, m - 1, 1)).toLocaleDateString("fr-FR", {
    month: "long",
    year: "numeric",
    timeZone: "UTC",
  });
};

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
    if (/^\d{4}-\d{2}$/.test(value)) return monthLabel(value);
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
