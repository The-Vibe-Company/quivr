// The guided form of a keyword alert, in the style of a search engine's
// advanced search: all these words, this exact phrase, any of these words,
// none of these words, and the sources to watch. Each field is text as typed;
// it writes one part of the plugin's keyword tree. A tree of that shape reads
// back into the fields; any other tree stays an advanced query (notation.ts).
// There is no "words near each other" field: the alerts plugin has no
// proximity operator.
import type { KeywordExpression, KeywordNode } from "./notation";
import { SOURCE_FIELD, cleanWords } from "./alertForm";

export interface BuilderForm {
  /** Tous ces mots. */
  all: string;
  /** Cette phrase exacte. */
  phrase: string;
  /** Au moins un de ces mots. */
  any: string;
  /** Aucun de ces mots. */
  none: string;
  /** Source Namespaces; empty means every source. */
  sources: string[];
}

export const EMPTY_FORM: BuilderForm = { all: "", phrase: "", any: "", none: "", sources: [] };

const hasWord = (text: string) => /[\p{L}\p{N}]/u.test(text);

/**
 * The terms of a field: words separated by spaces or commas, and "several
 * words" in quotes kept together as a phrase (a quote left open runs to the
 * end). Without repeats, nor bits with no letter or digit.
 */
export function fieldTerms(text: string): string[] {
  const out: string[] = [];
  const pattern = /"([^"]*)"?|[^\s,"]+/gu;
  for (const [match, quoted] of text.matchAll(pattern)) out.push(quoted ?? match);
  return cleanWords(out).filter(hasWord);
}

/** The phrase field: everything typed is one term, quotes around it or not. */
const phraseTerm = (text: string) => {
  const [term] = cleanWords([text.replace(/"/g, " ")]);
  return term && hasWord(term) ? term : null;
};

/** A field's text for terms, a phrase in quotes. */
export const fieldText = (terms: string[]) =>
  terms.map((term) => (/[\s,]/u.test(term) ? `"${term}"` : term)).join(" ");

const group = (op: "all" | "any", items: KeywordNode[]): KeywordNode =>
  items.length === 1 ? items[0] : op === "all" ? { all: items } : { any: items };

/** The keyword expression of the form, or null while no field asks for a word. */
export function build(form: BuilderForm): KeywordExpression | null {
  const all = fieldTerms(form.all).map((term) => ({ term }));
  const phrase = phraseTerm(form.phrase);
  const any = fieldTerms(form.any).map((term) => ({ term }));
  if (!all.length && !phrase && !any.length) return null;
  const parts: KeywordNode[] = [...all];
  if (phrase) parts.push({ term: phrase });
  if (any.length) parts.push(group("any", any));
  const sources = [...new Set(form.sources)];
  if (sources.length)
    parts.push(group("any", sources.map((equals) => ({ field: SOURCE_FIELD, equals }))));
  for (const term of fieldTerms(form.none)) parts.push({ not: { term } });
  return { kind: "keywords", match: group("all", parts) };
}

const isTerm = (n: KeywordNode): n is { term: string } => "term" in n;
const isSource = (n: KeywordNode): n is { field: string; equals: string } =>
  "field" in n && n.field === SOURCE_FIELD;
// The items of an "all" group, in any order.
const items = (node: KeywordNode) =>
  ("all" in node ? node.all : [node]).map((n) => JSON.stringify(n)).sort();

/**
 * The form of a keyword tree, or null when the form cannot show it (groups
 * inside groups, two OR groups, a NOT of a group, other fields…). The order
 * of the parts of an AND does not matter; anything else must come back the
 * same from the form.
 */
export function unbuild(node: KeywordNode): BuilderForm | null {
  const all: string[] = [];
  let phrase = "";
  let any: string[] | null = null;
  let sources: string[] | null = null;
  const none: string[] = [];
  for (const item of "all" in node ? node.all : [node]) {
    if (isTerm(item)) {
      if (!phrase && /\s/u.test(item.term)) phrase = item.term;
      else all.push(item.term);
    } else if (isSource(item) || ("any" in item && item.any.every(isSource))) {
      if (sources) return null;
      sources = "any" in item ? item.any.map((f) => (f as { equals: string }).equals) : [item.equals];
    } else if ("any" in item && item.any.every(isTerm)) {
      if (any) return null;
      any = item.any.map((t) => (t as { term: string }).term);
    } else if ("not" in item && isTerm(item.not)) none.push(item.not.term);
    else return null;
  }
  const form: BuilderForm = {
    all: fieldText(all),
    phrase,
    any: fieldText(any || []),
    none: fieldText(none),
    sources: sources || [],
  };
  const back = build(form);
  return back && JSON.stringify(items(back.match)) === JSON.stringify(items(node)) ? form : null;
}

/** The words and phrases an article must or may contain, to highlight them. */
export function positiveTerms(node: KeywordNode): string[] {
  if ("term" in node) return [node.term];
  if ("all" in node) return node.all.flatMap(positiveTerms);
  if ("any" in node) return node.any.flatMap(positiveTerms);
  return [];
}
