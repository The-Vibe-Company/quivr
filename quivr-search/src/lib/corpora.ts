// The corpora the demo reads (THE-1171), as the facade lists them: the demo
// corpus, where the demo writes, and the others its deployment names. Each
// carries the fields a filter can use: the common ones of every corpus and
// its own typed mappings.
import { request } from "./search";

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
}

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

/** The corpora's names, for a sentence: « A », « A » et « B »… */
export function corpusNames(ids: string[], corpora: Corpus[]) {
  const names = ids.map((id) => `« ${corpora.find((c) => c.corpus_id === id)?.name || id} »`);
  return names.length > 1 ? `${names.slice(0, -1).join(", ")} et ${names.at(-1)}` : names[0] || "";
}

/** Which corpora a type-specific filter left out, as the engine says. */
export interface Exclusion {
  corpus_id: string;
  fields: string[];
}

/** The sentence telling which corpora a filter excluded, and why. */
export function exclusionNotice(excluded: Exclusion[], corpora: Corpus[]) {
  if (!excluded.length) return "";
  const fields = [...new Set(excluded.flatMap((e) => e.fields))].map((f) => `« ${fieldLabel(f)} »`);
  const one = excluded.length === 1;
  return `${one ? "Le corpus" : "Les corpus"} ${corpusNames(excluded.map((e) => e.corpus_id), corpora)} ${one ? "est exclu" : "sont exclus"} : ${one ? "il n’a" : "ils n’ont"} pas le champ ${fields.join(", ")}.`;
}
