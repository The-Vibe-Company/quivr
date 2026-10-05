import { Fragment } from "react";
import type { KeywordNode } from "../../lib/notation";
import { explain } from "../../lib/explain";

/** What a keyword query will catch, as one sentence; its words set apart. */
export function Interpretation({ node }: { node: KeywordNode }) {
  return (
    <span className="interpretation">
      {explain(node).map((piece, i) =>
        typeof piece === "string" ? (
          <Fragment key={i}>{piece}</Fragment>
        ) : (
          <span key={i} className="keyword">
            {piece.keyword}
          </span>
        ),
      )}
    </span>
  );
}
