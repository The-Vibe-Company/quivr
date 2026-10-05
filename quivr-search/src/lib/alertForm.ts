// The simple form of a keyword alert: words to watch (any or all of them),
// words to ignore, and the sources to watch. It is one shape of the plugin's
// keyword tree, so a form becomes a tree and a tree of that shape becomes a
// form again. Any other tree is edited as an advanced query (notation.ts).
import type { KeywordExpression, KeywordNode } from "./notation";
import { HAND_NAMESPACE } from "./feed";
import { displayName } from "./sourceNames";

export interface WordsForm {
  words: string[];
  mode: "any" | "all";
  exclude: string[];
  /** Source Namespaces; empty means every source. */
  sources: string[];
}

/** The plugin's keyword field for a Record's Source Namespace. */
export const SOURCE_FIELD = "source";

/** How a Source Namespace reads in the app. */
export const sourceName = (namespace: string) =>
  namespace === HAND_NAMESPACE ? "Ajouté à la main" : displayName(namespace);

const fold = (text: string) =>
  text.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();

/** Trimmed, without blanks or repeats (accents and case do not count). */
export function cleanWords(words: string[]) {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of words) {
    const word = raw.trim().replace(/\s+/g, " ");
    if (!word || seen.has(fold(word))) continue;
    seen.add(fold(word));
    out.push(word);
  }
  return out;
}

const group = (op: "all" | "any", items: KeywordNode[]): KeywordNode =>
  items.length === 1 ? items[0] : op === "all" ? { all: items } : { any: items };

/** The keyword expression of a form, or null while it has no word. */
export function compose(form: WordsForm): KeywordExpression | null {
  const words = cleanWords(form.words);
  if (!words.length) return null;
  const terms = words.map((term) => ({ term }));
  const parts: KeywordNode[] = [];
  if (form.mode === "all") parts.push(...terms);
  else parts.push(group("any", terms));
  const sources = [...new Set(form.sources)];
  if (sources.length)
    parts.push(
      group(
        "any",
        sources.map((equals) => ({ field: SOURCE_FIELD, equals })),
      ),
    );
  for (const term of cleanWords(form.exclude)) parts.push({ not: { term } });
  return { kind: "keywords", match: group("all", parts) };
}

const isTerm = (n: KeywordNode): n is { term: string } => "term" in n;
const isSource = (n: KeywordNode): n is { field: string; equals: string } =>
  "field" in n && n.field === SOURCE_FIELD;

/** The form of a keyword tree, or null when the tree needs the advanced query. */
export function decompose(node: KeywordNode): WordsForm | null {
  const items = "all" in node ? node.all : [node];
  const bare: string[] = [];
  let either: string[] | null = null;
  let sources: string[] | null = null;
  const exclude: string[] = [];
  for (const item of items) {
    if (isTerm(item)) bare.push(item.term);
    else if (isSource(item)) {
      // Two bare source filters would ask for both at once: not this form.
      if (sources) return null;
      sources = [item.equals];
    } else if ("any" in item && item.any.every(isTerm)) {
      if (either) return null;
      either = item.any.map((t) => (t as { term: string }).term);
    } else if ("any" in item && item.any.every(isSource)) {
      if (sources) return null;
      sources = item.any.map((f) => (f as { equals: string }).equals);
    } else if ("not" in item && isTerm(item.not)) exclude.push(item.not.term);
    else return null;
  }
  if (either && bare.length) return null;
  const words = either || bare;
  if (!words.length) return null;
  const form: WordsForm = {
    words,
    mode: either || bare.length === 1 ? "any" : "all",
    exclude,
    sources: sources || [],
  };
  // Only a tree the form writes back identically is shown as a form.
  const back = compose(form);
  return back && JSON.stringify(back.match) === JSON.stringify(node)
    ? form
    : null;
}

