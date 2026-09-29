import { Fragment, type ReactNode } from "react";
import type { KeywordNode } from "../../lib/notation";

const FIELDS: Record<string, string> = {
  source: "source",
  producer: "producteur",
  origin: "origine",
  connector: "connecteur",
  connector_kind: "type de connecteur",
  author: "auteur",
  category: "catégorie",
};

const join = (items: ReactNode[], word: string) =>
  items.map((item, i) => (
    <Fragment key={i}>
      {i > 0 && <span className="interpretation-op"> {word} </span>}
      {item}
    </Fragment>
  ));

function render(node: KeywordNode, nested: boolean): ReactNode {
  if ("term" in node) return <span className="keyword">{node.term}</span>;
  if ("field" in node)
    return (
      <span className="keyword keyword-field">
        {FIELDS[node.field] || node.field} = {node.equals}
      </span>
    );
  if ("not" in node)
    return (
      <>
        <span className="interpretation-op">sans </span>
        {render(node.not, true)}
      </>
    );
  const [items, word] = "all" in node ? [node.all, "et"] : [node.any, "ou"];
  const inner = join(
    items.map((child) => render(child, true)),
    word,
  );
  return nested ? (
    <span className="interpretation-group">({inner})</span>
  ) : (
    inner
  );
}

/** How the alert reads a keyword query, as a sentence of words and operators. */
export function Interpretation({ node }: { node: KeywordNode }) {
  return <span className="interpretation-tree">{render(node, false)}</span>;
}
