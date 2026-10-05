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
  | { state: "invalid"; message: string; at: number; end: number };

export function useParsed(query: string): Parsed {
  return useMemo(() => {
    if (!query.trim()) return { state: "empty" };
    try {
      return { state: "valid", expression: parse(query) };
    } catch (error) {
      if (error instanceof NotationError)
        return { state: "invalid", message: error.message, at: error.at, end: error.end };
      throw error;
    }
  }, [query]);
}

/**
 * Live reading of the query under its field: the sentence of what the alert
 * will catch, or what to fix and where, the faulty characters marked in a
 * copy of the query (pressing it puts the caret there). Screen readers get
 * it through aria-describedby, not as an announcement at every keystroke.
 */
export function QueryPreview({
  id,
  parsed,
  query = "",
  empty,
  onLocate,
}: {
  id: string;
  parsed: Parsed;
  /** The query as typed, to show where a mistake is. */
  query?: string;
  /** What to say while there is no query. */
  empty: string;
  /** Selects the faulty characters in the field. */
  onLocate?: (at: number, end: number) => void;
}) {
  if (parsed.state === "empty")
    return (
      <p id={id} className="query-preview muted">
        {empty}
      </p>
    );
  if (parsed.state === "invalid") {
    const { at, end } = parsed;
    return (
      <div id={id} className="query-preview" data-state="invalid">
        <WarningCircle size={16} aria-hidden="true" />
        <div className="query-error">
          <span>{parsed.message}</span>
          {query && onLocate && (
            <button
              type="button"
              className="query-error-where"
              title="Placer le curseur sur l’erreur"
              onClick={() => onLocate(at, end)}
            >
              <span className="visually-hidden">
                Placer le curseur sur l’erreur, caractère {at + 1} :{" "}
              </span>
              <code>
                {query.slice(0, at)}
                <mark data-end={at === end || undefined}>{query.slice(at, end) || " "}</mark>
                {query.slice(end)}
              </code>
            </button>
          )}
        </div>
      </div>
    );
  }
  return (
    <p id={id} className="query-preview" data-state="valid">
      <CheckCircle size={16} aria-hidden="true" />
      <Interpretation node={parsed.expression.match} />
    </p>
  );
}
