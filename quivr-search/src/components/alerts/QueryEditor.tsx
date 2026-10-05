import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { QueryPreview, type Parsed } from "./QueryPreview";

// The syntax the alerts plugin reads (lib/notation.ts), as buttons that write
// it at the caret: [what is written, what it means]. A pair wraps the selection.
const HINTS: [string, string][] = [
  ['"…"', "phrase exacte"],
  ["AND", "tous"],
  ["OR", "l’un ou l’autre"],
  ["NOT", "sans"],
  ["( )", "grouper"],
  ["source:", "une source"],
];

// Neutral examples that each show a part of the syntax.
const EXAMPLES = [
  "orage AND (grêle OR vent) NOT football",
  '"marché aux fleurs" OR brocante',
  "grève (port OR aéroport) NOT sondage",
];

/** The query once a hint is written over the selection, and where the caret goes. */
function insert(query: string, start: number, end: number, hint: string): [string, number, number] {
  const before = query.slice(0, start);
  const selected = query.slice(start, end);
  const after = query.slice(end);
  const pair = hint === '"…"' ? ['"', '"'] : hint === "( )" ? ["(", ")"] : null;
  const space = (text: string) => (text && !/\s$/.test(text) ? `${text} ` : text);
  if (pair) {
    const head = space(before) + pair[0];
    // An empty pair leaves the caret inside; a wrapped selection stays selected.
    return [head + selected + pair[1] + after, head.length, head.length + selected.length];
  }
  const head = space(before) + hint + (hint.endsWith(":") ? "" : " ");
  // A selection is what the operator applies to: NOT football, source:nom.
  // Several words stay together: NOT (marché aux fleurs), source:"Météo locale".
  if (selected) {
    const several = /\s/u.test(selected.trim()) && !/^[("].*[)"]$/su.test(selected.trim());
    const operand = !several ? selected : hint === "source:" ? `"${selected.trim()}"` : hint === "NOT" ? `(${selected.trim()})` : selected;
    return [head + operand + after, head.length, head.length + operand.length];
  }
  const tail = after.replace(/^\s+/, "");
  return [head + tail, head.length, head.length];
}

/**
 * The advanced query, written by hand: the field, buttons that write the
 * syntax, examples to start from, and the live reading of the query (its
 * sentence, or the mistake and where it is). A mistake is announced to screen
 * readers once typing pauses.
 */
export function QueryEditor({
  value,
  onChange,
  parsed,
  footer,
}: {
  value: string;
  onChange: (value: string) => void;
  parsed: Parsed;
  /** Under the field: the way back to the guided form, or why there is none. */
  footer?: ReactNode;
}) {
  const field = useRef<HTMLInputElement>(null);
  // The selection to restore once React has written the new value.
  const selection = useRef<[number, number] | null>(null);
  const [spoken, setSpoken] = useState("");

  useLayoutEffect(() => {
    if (!selection.current || !field.current) return;
    field.current.focus();
    field.current.setSelectionRange(...selection.current);
    selection.current = null;
  }, [value]);

  const message = parsed.state === "invalid" ? parsed.message : "";
  useEffect(() => {
    if (!message) return setSpoken("");
    const timer = setTimeout(() => setSpoken(message), 1000);
    return () => clearTimeout(timer);
  }, [message]);

  const write = (next: string, start: number, end: number) => {
    if (next === value) {
      // Nothing to re-render: place the caret now.
      field.current?.focus();
      field.current?.setSelectionRange(start, end);
      return;
    }
    selection.current = [start, end];
    onChange(next);
  };

  return (
    <div className="form-field query-editor">
      <label htmlFor="alert-query">Requête avancée</label>
      <input
        ref={field}
        id="alert-query"
        className="form-input"
        value={value}
        autoComplete="off"
        autoCapitalize="none"
        autoCorrect="off"
        spellCheck={false}
        placeholder="orage AND (grêle OR vent) NOT football"
        aria-invalid={parsed.state === "invalid" || undefined}
        aria-describedby="alert-query-preview alert-query-help"
        onChange={(event) => onChange(event.target.value)}
      />
      <QueryPreview
        id="alert-query-preview"
        parsed={parsed}
        query={value}
        empty="Des mots, une phrase entre guillemets, AND, OR, NOT et des parenthèses."
        onLocate={(at, end) => {
          field.current?.focus();
          field.current?.setSelectionRange(at, end);
        }}
      />
      <p className="visually-hidden" aria-live="polite">
        {spoken}
      </p>
      <div className="query-hints" role="group" aria-label="Insérer dans la requête" id="alert-query-help">
        {HINTS.map(([hint, meaning]) => (
          <button
            key={hint}
            type="button"
            className="query-hint"
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => {
              const input = field.current;
              const start = input?.selectionStart ?? value.length;
              const end = input?.selectionEnd ?? value.length;
              write(...insert(value, start, end, hint));
            }}
          >
            <code>{hint}</code>
            <span>{meaning}</span>
          </button>
        ))}
      </div>
      <div className="query-examples">
        <span id="alert-query-examples">Exemples</span>
        <ul aria-labelledby="alert-query-examples">
          {EXAMPLES.map((example) => (
            <li key={example}>
              <button
                type="button"
                className="query-example"
                aria-label={`Utiliser l’exemple ${example}`}
                onClick={() => write(example, example.length, example.length)}
              >
                <code>{example}</code>
              </button>
            </li>
          ))}
        </ul>
      </div>
      {footer}
    </div>
  );
}
