// The corpora the demo reads (THE-1171), as the facade lists them: the demo
// corpus, where the demo writes, and the others its deployment names. Each
// carries the fields a filter can use: the common ones of every corpus and
// its own typed mappings.
import { request } from "./search";
import type { Exclusion } from "../types";

export type { Exclusion };

export type FieldType = "string" | "number" | "boolean" | "datetime" | "string_array";

export interface Field {
  /** As a filter names it: `metadata.language` for a common field, the mapping's name otherwise. */
  name: string;
  type: FieldType;
  source_pointer: string;
}

export interface Corpus {
  corpus_id: string;
  name: string;
  /** The demo corpus, where sources, alerts and added texts live. */
  demo: boolean;
  common: Field[];
  own: Field[];
  /** How many documents it holds, when the engine could count them. */
  documents?: number;
}

/** The common fields of every corpus (quivr.metadata), by type, as the facade declares them. */
export const COMMON_TYPES: [string, FieldType][] = [
  ["metadata.language", "string"],
  ["metadata.published_at", "datetime"],
  ["metadata.source_type", "string"],
  ["metadata.source", "string"],
  ["metadata.author", "string_array"],
  ["metadata.subjects", "string_array"],
  ["metadata.tags", "string_array"],
  ["metadata.country", "string_array"],
  ["metadata.place", "string_array"],
];

export const fetchCorpora = (signal?: AbortSignal) =>
  request<{ items: Corpus[] }>("/demo/corpora", undefined, signal);

const COMMON_LABELS: Record<string, string> = {
  "metadata.language": "Langue",
  "metadata.published_at": "Date de publication",
  "metadata.source_type": "Type de source",
  "metadata.source": "Source d’origine",
  "metadata.author": "Auteur",
  "metadata.subjects": "Sujets",
  "metadata.tags": "Mots-clés",
  "metadata.country": "Pays",
  "metadata.place": "Lieu",
};

/** A field's name for people: the common fields in French, a corpus's own as declared. */
export const fieldLabel = (name: string) =>
  COMMON_LABELS[name] ||
  name.charAt(0).toUpperCase() + name.slice(1).replace(/_/g, " ");

/**
 * The feed's `scope`: the corpora picked joined by commas, or "" when only
 * the demo corpus is (the facade's default).
 */
export const scopeOf = (picked: string[], demo: string) =>
  picked.length === 1 && picked[0] === demo ? "" : picked.join(",");

/** Items for a sentence: A, A et B, A, B et C. */
const list = (items: string[]) =>
  items.length > 1 ? `${items.slice(0, -1).join(", ")} et ${items.at(-1)}` : items[0] || "";

/** The corpora's names, for a sentence: « A », « A » et « B »… */
export function corpusNames(ids: string[], corpora: Corpus[]) {
  return list(ids.map((id) => `« ${corpora.find((c) => c.corpus_id === id)?.name || id} »`));
}

/**
 * The sentences telling which corpora a filter excluded and why: corpora
 * lacking the same fields share one.
 */
export function exclusionNotice(excluded: Exclusion[], corpora: Corpus[]) {
  const byFields = new Map<string, Exclusion[]>();
  for (const e of excluded) {
    const key = [...e.fields].sort().join("\n");
    byFields.set(key, [...(byFields.get(key) || []), e]);
  }
  return [...byFields.values()]
    .map((group) => {
      const one = group.length === 1;
      const fields = group[0].fields.map((f) => `« ${fieldLabel(f)} »`);
      const lacks = `${one ? "il n’a" : "ils n’ont"} pas ${fields.length > 1 ? "les champs" : "le champ"} ${list(fields)}`;
      return `${one ? "Le corpus" : "Les corpus"} ${corpusNames(group.map((e) => e.corpus_id), corpora)} ${one ? "est exclu" : "sont exclus"} : ${lacks}.`;
    })
    .join(" ");
}
