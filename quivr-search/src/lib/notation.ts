// The text notation of keyword alerts, ported from the alerts plugin's
// reference parser (plugins/alerts/alerts/notation.py). Both parsers are held
// to the same cases (plugins/alerts/tests/notation-cases.json), so a query
// typed here is read exactly as the plugin documents it:
//
//   query   := or
//   or      := and ( "OR" and )*
//   and     := unary ( [ "AND" ] unary )*      juxtaposed items are all required
//   unary   := "NOT" unary | primary
//   primary := "(" or ")" | PHRASE | WORD | FIELD
//   PHRASE  := '"' characters '"'              \" and \\ escape a quote and a backslash
//   FIELD   := name ":" ( WORD | PHRASE )      name is [a-z][a-z0-9_]*
//
// Messages are in French for the demo; the grammar is the plugin's.

export type KeywordNode =
  | { term: string }
  | { field: string; equals: string }
  | { all: KeywordNode[] }
  | { any: KeywordNode[] }
  | { not: KeywordNode };

export interface KeywordExpression {
  kind: "keywords";
  match: KeywordNode;
}

export class NotationError extends Error {}

const MAX_DEPTH = 6;
const MAX_ITEMS = 64;
const MAX_TEXT = 256;
const FIELD = /^([a-z][a-z0-9_]{0,63}):(.*)$/s;
const OPERATORS = new Set(["AND", "OR", "NOT"]);
const SPACE = /\s/u;
const hasWord = (text: string) => /[\p{L}\p{N}]/u.test(text);
const length = (text: string) => [...text].length;

type Token =
  | { kind: "(" | ")" | "AND" | "OR" | "NOT" | "end" }
  | { kind: "term"; text: string }
  | { kind: "field"; name: string; text: string };

function phrase(query: string, start: number): [string, number] {
  let out = "";
  let i = start + 1;
  while (i < query.length) {
    const c = query[i];
    if (c === "\\" && (query[i + 1] === '"' || query[i + 1] === "\\")) {
      out += query[i + 1];
      i += 2;
    } else if (c === '"') return [out, i + 1];
    else {
      out += c;
      i += 1;
    }
  }
  throw new NotationError("Il manque le guillemet fermant d’une expression.");
}

function checked(text: string, what: string): string {
  if (length(text) > MAX_TEXT)
    throw new NotationError(
      `${what} « ${text.slice(0, 40)}… » dépasse ${MAX_TEXT} caractères.`,
    );
  if (!hasWord(text))
    throw new NotationError(
      `${what} « ${text} » ne contient ni lettre ni chiffre.`,
    );
  return text;
}

function tokens(query: string): Token[] {
  const out: Token[] = [];
  let i = 0;
  while (i < query.length) {
    const c = query[i];
    if (SPACE.test(c)) i += 1;
    else if (c === "(" || c === ")") {
      out.push({ kind: c });
      i += 1;
    } else if (c === '"') {
      const [text, next] = phrase(query, i);
      i = next;
      out.push({ kind: "term", text: checked(text, "L’expression") });
    } else {
      const start = i;
      while (
        i < query.length &&
        !SPACE.test(query[i]) &&
        !'()"'.includes(query[i])
      )
        i += 1;
      const word = query.slice(start, i);
      if (OPERATORS.has(word)) {
        out.push({ kind: word as "AND" | "OR" | "NOT" });
        continue;
      }
      if (word.startsWith("-"))
        throw new NotationError(
          `« ${word} » : écrivez NOT pour exclure un mot, par exemple NOT ${word.replace(/^-+/, "")}.`,
        );
      const field = FIELD.exec(word);
      if (!field || field[2].startsWith("/")) {
        // A URL such as https://example.com is text, not a field filter.
        out.push({ kind: "term", text: checked(word, "Le mot") });
        continue;
      }
      const name = field[1];
      let value = field[2];
      if (!value && query[i] === '"') [value, i] = phrase(query, i);
      if (!value)
        throw new NotationError(
          `${name}: attend une valeur, par exemple ${name}:valeur ou ${name}:"deux mots".`,
        );
      if (length(value) > MAX_TEXT)
        throw new NotationError(
          `La valeur de ${name}: dépasse ${MAX_TEXT} caractères.`,
        );
      out.push({ kind: "field", name, text: value });
    }
  }
  out.push({ kind: "end" });
  return out;
}

function describe(token: Token): string {
  if (token.kind === "end") return "la fin de la requête";
  if (token.kind === "term" || token.kind === "field")
    return `« ${token.text} »`;
  return token.kind === "(" || token.kind === ")"
    ? `la parenthèse « ${token.kind} »`
    : token.kind;
}

const TOO_DEEP = `Les groupes et les NOT s’imbriquent sur plus de ${MAX_DEPTH} niveaux : simplifiez la requête.`;

function group(operator: "all" | "any", items: KeywordNode[]): KeywordNode {
  if (items.length === 1) return items[0];
  const flat: KeywordNode[] = [];
  for (const item of items)
    if (operator in item)
      flat.push(...(item as Record<typeof operator, KeywordNode[]>)[operator]);
    else flat.push(item);
  if (flat.length > MAX_ITEMS)
    throw new NotationError(
      `Un groupe compte ${flat.length} éléments ; ${MAX_ITEMS} au plus sont acceptés.`,
    );
  return operator === "all" ? { all: flat } : { any: flat };
}

function depth(node: KeywordNode): number {
  if ("not" in node) return 1 + depth(node.not);
  if ("all" in node) return 1 + Math.max(...node.all.map(depth));
  if ("any" in node) return 1 + Math.max(...node.any.map(depth));
  return 0;
}

class Parser {
  private at = 0;
  private nesting = 0;
  constructor(private items: Token[]) {}
  peek() {
    return this.items[this.at];
  }
  take() {
    return this.items[this.at++];
  }
  enter() {
    // Bounds recursion on pathological input; the depth rule is checked on the tree.
    if (++this.nesting > 4 * MAX_DEPTH + 8) throw new NotationError(TOO_DEEP);
  }
  or(): KeywordNode {
    const items = [this.and()];
    while (this.peek().kind === "OR") {
      this.take();
      items.push(this.and());
    }
    return group("any", items);
  }
  and(): KeywordNode {
    const items = [this.unary()];
    while (!["OR", ")", "end"].includes(this.peek().kind)) {
      if (this.peek().kind === "AND") this.take();
      items.push(this.unary());
    }
    return group("all", items);
  }
  unary(): KeywordNode {
    if (this.peek().kind !== "NOT") return this.primary();
    this.take();
    this.enter();
    const node = { not: this.unary() };
    this.nesting -= 1;
    return node;
  }
  primary(): KeywordNode {
    const token = this.take();
    if (token.kind === "(") {
      this.enter();
      const node = this.or();
      this.nesting -= 1;
      if (this.take().kind !== ")")
        throw new NotationError("Il manque une parenthèse fermante.");
      return node;
    }
    if (token.kind === "term") return { term: token.text };
    if (token.kind === "field")
      return { field: token.name, equals: token.text };
    if (token.kind === "end") {
      const previous = this.at >= 2 ? this.items[this.at - 2].kind : "";
      throw new NotationError(
        previous
          ? `La requête s’arrête après ${previous} : ajoutez un mot.`
          : "La requête est vide.",
      );
    }
    throw new NotationError(`${capital(describe(token))} est inattendu ici.`);
  }
}

const capital = (text: string) => text[0].toUpperCase() + text.slice(1);

/** Reads a query in the text notation; throws NotationError, in French, on a mistake. */
export function parse(query: string): KeywordExpression {
  const parser = new Parser(tokens(query));
  if (parser.peek().kind === "end")
    throw new NotationError("La requête est vide.");
  const node = parser.or();
  if (parser.peek().kind !== "end")
    throw new NotationError(
      `${capital(describe(parser.peek()))} est inattendu ici.`,
    );
  if (depth(node) > MAX_DEPTH) throw new NotationError(TOO_DEEP);
  return { kind: "keywords", match: node };
}

const quote = (text: string) =>
  `"${text.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
// A bare word the tokenizer reads back as the same single term.
const bare = (text: string) =>
  text !== "" &&
  !/[\s()"]/u.test(text) &&
  !OPERATORS.has(text) &&
  !text.startsWith("-") &&
  !(FIELD.test(text) && !FIELD.exec(text)![2].startsWith("/"));

/**
 * Writes an expression back in the text notation, for display and editing:
 * parse(print(tree)) gives the tree back. A filter on a JSON Pointer has no
 * notation, so such a tree prints as null.
 */
export function print(node: KeywordNode): string | null {
  if ("term" in node) return bare(node.term) ? node.term : quote(node.term);
  if ("field" in node) {
    if (!/^[a-z][a-z0-9_]{0,63}$/.test(node.field)) return null;
    const value = node.equals;
    return `${node.field}:${value && !/[\s()"]/u.test(value) && !value.startsWith("/") ? value : quote(value)}`;
  }
  const inner = (child: KeywordNode, wrap: boolean) => {
    const text = print(child);
    return text === null ? null : wrap ? `(${text})` : text;
  };
  if ("not" in node) {
    const text = inner(node.not, "all" in node.not || "any" in node.not);
    return text === null ? null : `NOT ${text}`;
  }
  const [items, joiner] =
    "all" in node ? [node.all, " AND "] : [node.any, " OR "];
  const parts = items.map((child) =>
    inner(child, "any" in child || ("all" in child && "any" in node)),
  );
  return parts.includes(null) ? null : parts.join(joiner);
}
