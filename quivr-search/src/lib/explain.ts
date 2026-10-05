// What a keyword alert will catch, as one French sentence, for any tree the
// notation reads: "Articles qui contiennent « élection » et la phrase
// « second tour », venus de « Météo locale », sauf ceux qui parlent de
// « football »." Words and phrases come out as their own pieces so the page
// can set them apart.
import type { KeywordNode } from "./notation";
import { SOURCE_FIELD, sourceName } from "./alertForm";

/** Plain text, or a word, a phrase or a field value the alert looks for. */
export type Piece = string | { keyword: string };

const FIELDS: Record<string, string> = {
  producer: "le producteur",
  origin: "l’origine",
  connector: "le connecteur",
  connector_kind: "le type de connecteur",
  author: "l’auteur",
  category: "la catégorie",
};

const isSource = (n: KeywordNode): n is { field: string; equals: string } =>
  "field" in n && n.field === SOURCE_FIELD;
const isWord = (n: KeywordNode) => "term" in n && !/\s/u.test(n.term);

/** a, b et c */
function list(parts: Piece[][], word: string): Piece[] {
  return parts.flatMap((part, i) => [
    ...(i === 0 ? [] : [i === parts.length - 1 ? ` ${word} ` : ", "]),
    ...part,
  ]);
}

function term(node: { term: string }): Piece[] {
  return /\s/u.test(node.term) ? ["la phrase ", { keyword: node.term }] : [{ keyword: node.term }];
}

function field(node: { field: string; equals: string }): Piece[] {
  if (node.field === SOURCE_FIELD) return ["la source ", { keyword: sourceName(node.equals) }];
  return [`${FIELDS[node.field] || node.field} `, { keyword: node.equals }];
}

/** A part of the tree, inside a sentence that says the article contains it. */
function part(node: KeywordNode): Piece[] {
  if ("term" in node) return term(node);
  if ("field" in node) return field(node);
  if ("not" in node) {
    const inner = node.not;
    return "any" in inner && inner.any.every((n) => "term" in n)
      ? ["sans ", ...list(inner.any.map(part), "ni")]
      : ["sans ", ...part(inner)];
  }
  if ("all" in node) {
    // « a » et « b » mais sans « c »
    const wanted = node.all.filter((n) => !("not" in n));
    const without = node.all.filter((n) => "not" in n);
    const head = wanted.length > 1 ? ["à la fois ", ...list(wanted.map(part), "et")] : wanted.flatMap(part);
    return without.length ? [...head, ...(head.length ? [" mais "] : []), ...list(without.map(part), "et")] : head;
  }
  if (node.any.every(isWord)) return ["au moins un des mots ", ...list(node.any.map(part), "ou")];
  return node.any.flatMap((child, i) => [i === 0 ? "soit " : ", soit ", ...part(child)]);
}

/** The sentence for a keyword tree. */
export function explain(node: KeywordNode): Piece[] {
  const items = "all" in node ? node.all : [node];
  const sources: string[] = [];
  const excluded: KeywordNode[] = [];
  const wanted: KeywordNode[] = [];
  for (const item of items) {
    if (isSource(item)) sources.push(item.equals);
    else if ("any" in item && item.any.every(isSource))
      sources.push(...item.any.map((f) => (f as { equals: string }).equals));
    // "Sauf ceux qui parlent de « a » ou de « b »" says NOT (a OR b) too.
    else if ("not" in item)
      excluded.push(...("any" in item.not && item.not.any.every((n) => "term" in n) ? item.not.any : [item.not]));
    else wanted.push(item);
  }
  // Alone, a choice of words reads "« a », « b » ou « c »".
  const only = wanted.length === 1 ? wanted[0] : null;
  const contains =
    only && "any" in only && only.any.every((n) => "term" in n)
      ? list(only.any.map(part), "ou")
      : list(wanted.map(part), "et");
  const out: Piece[] = wanted.length ? ["Articles qui contiennent ", ...contains] : ["Tous les articles"];
  if (sources.length)
    out.push(", venus de ", ...list(sources.map((s) => [{ keyword: sourceName(s) }]), "ou"));
  if (excluded.length)
    out.push(
      ", sauf ceux qui parlent de ",
      ...list(
        excluded.map((n) => ("term" in n && !/\s/u.test(n.term) ? [{ keyword: n.term }] : part(n))),
        "ou de",
      ),
    );
  out.push(".");
  return out;
}

/** The sentence as plain text, keywords in French quotes. */
export const sentence = (node: KeywordNode) =>
  explain(node)
    .map((p) => (typeof p === "string" ? p : `« ${p.keyword} »`))
    .join("");
