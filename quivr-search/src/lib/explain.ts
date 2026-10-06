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
  return [`${Object.hasOwn(FIELDS, node.field) ? FIELDS[node.field] : node.field} `, { keyword: node.equals }];
}

const isGroup = (n: KeywordNode) => "all" in n || "any" in n;
// A group inside a group is bracketed, so it cannot be read as going on.
const inner = (node: KeywordNode): Piece[] => (isGroup(node) ? ["(", ...part(node), ")"] : part(node));

/** A part of the tree, inside a sentence that says the article contains it. */
function part(node: KeywordNode): Piece[] {
  if ("term" in node) return term(node);
  if ("field" in node) return field(node);
  if ("not" in node) {
    const negated = node.not;
    if ("not" in negated) return part(negated.not);
    return "any" in negated && negated.any.every((n) => "term" in n)
      ? ["sans ", ...list(negated.any.map(part), "ni")]
      : ["sans ", ...inner(negated)];
  }
  if ("all" in node) {
    // « a » et « b » mais sans « c »
    const wanted = node.all.filter((n) => !("not" in n));
    const without = node.all.filter((n) => "not" in n);
    const head = wanted.length > 1 ? ["à la fois ", ...list(wanted.map(inner), "et")] : wanted.flatMap(inner);
    return without.length ? [...head, ...(head.length ? [" mais "] : []), ...list(without.map(part), "et")] : head;
  }
  if (node.any.every(isWord)) return ["au moins un des mots ", ...list(node.any.map(part), "ou")];
  return node.any.flatMap((child, i) => [i === 0 ? "soit " : ", soit ", ...inner(child)]);
}

/** The sentence for a keyword tree. */
export function explain(node: KeywordNode): Piece[] {
  const items = "all" in node ? node.all : [node];
  const sources: string[] = [];
  const excluded: KeywordNode[] = [];
  const wanted: KeywordNode[] = [];
  for (let item of items) {
    // Two NOTs cancel out.
    while ("not" in item && "not" in item.not) item = item.not.not;
    // One source filter, or one choice of sources, reads "venus de …"; a
    // second one asks for both at once and stays a part of the sentence.
    if (isSource(item) && !sources.length) sources.push(item.equals);
    else if ("any" in item && item.any.every(isSource) && !sources.length)
      sources.push(...item.any.map((f) => (f as { equals: string }).equals));
    // "Sauf ceux qui parlent de « a » ou de « b »" says NOT (a OR b) too.
    else if ("not" in item)
      excluded.push(...("any" in item.not && item.not.any.every((n) => "term" in n) ? item.not.any : [item.not]));
    else wanted.push(item);
  }
  // Alone, a choice of words reads "« a », « b » ou « c »". Otherwise groups
  // come last, and any but the last one is bracketed: "« d » et soit …".
  const only = wanted.length === 1 ? wanted[0] : null;
  const ordered = [...wanted.filter((n) => !isGroup(n)), ...wanted.filter(isGroup)];
  const contains =
    only && "any" in only && only.any.every((n) => "term" in n)
      ? list(only.any.map(part), "ou")
      : list(
          ordered.map((n, i) => (i < ordered.length - 1 ? inner(n) : part(n))),
          "et",
        );
  const out: Piece[] = wanted.length ? ["Articles qui contiennent ", ...contains] : ["Tous les articles"];
  if (sources.length)
    out.push(", venus de ", ...list(sources.map((s) => [{ keyword: sourceName(s) }]), "ou"));
  if (excluded.length)
    out.push(
      ", sauf ceux qui parlent de ",
      ...list(
        excluded.map((n) => ("term" in n && !/\s/u.test(n.term) ? [{ keyword: n.term }] : inner(n))),
        "ou de",
      ),
    );
  out.push(".");
  return out;
}
