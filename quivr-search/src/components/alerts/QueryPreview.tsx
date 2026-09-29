import { useMemo } from "react";
import { CheckCircle, WarningCircle } from "@phosphor-icons/react";
import {
  NotationError,
  parse,
  type KeywordExpression,
} from "../../lib/notation";
import { Interpretation } from "./Interpretation";

export type Parsed =
  | { state: "empty" }
  | { state: "valid"; expression: KeywordExpression }
  | { state: "invalid"; message: string };

export function useParsed(query: string): Parsed {
  return useMemo(() => {
    if (!query.trim()) return { state: "empty" };
    try {
      return { state: "valid", expression: parse(query) };
    } catch (error) {
      if (error instanceof NotationError)
        return { state: "invalid", message: error.message };
      throw error;
    }
  }, [query]);
}

/**
 * Live reading of the query under its field: how the alert understands it,
 * or what to fix. Screen readers get it through aria-describedby, not as an
 * announcement at every keystroke.
 */
export function QueryPreview({ id, parsed }: { id: string; parsed: Parsed }) {
  if (parsed.state === "empty")
    return (
      <p id={id} className="query-preview muted">
        Des mots, une expression entre guillemets, AND, OR, NOT et des
        parenthèses.
      </p>
    );
  if (parsed.state === "invalid")
    return (
      <p id={id} className="query-preview" data-state="invalid">
        <WarningCircle size={16} aria-hidden="true" />
        <span>{parsed.message}</span>
      </p>
    );
  return (
    <p id={id} className="query-preview" data-state="valid">
      <CheckCircle size={16} aria-hidden="true" />
      <span>
        <span className="muted">Articles avec </span>
        <Interpretation node={parsed.expression.match} />
      </span>
    </p>
  );
}
