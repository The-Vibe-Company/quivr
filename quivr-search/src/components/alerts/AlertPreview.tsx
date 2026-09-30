import { useEffect, useRef, useState } from "react";
import { APIError } from "../../lib/search";
import {
  alertMessage,
  previewAlert,
  sourceLabel,
  type AlertExpression,
  type AlertPreview as Preview,
} from "../../lib/alerts";

type State =
  | { state: "idle" }
  | { state: "loading" }
  | { state: "done"; preview: Preview }
  | { state: "error"; message: string };

/** How long typing must pause before a keyword preview is asked for. */
const DEBOUNCE_MS = 600;

const caught = (n: number) =>
  n === 1 ? "1 aurait été attrapé" : `${n} auraient été attrapés`;

/**
 * What the alert being written would have caught among the newest articles,
 * judged by the alert engine with the alert's own rules; nothing is saved.
 * A keyword alert is previewed as it is typed. A described alert asks a paid
 * classifier about every article, so it is previewed only on request.
 */
export function AlertPreview({
  expression,
  onDemand,
  onUnauthorized,
}: {
  expression: AlertExpression | null;
  /** Preview only when the button is pressed (described alerts). */
  onDemand: boolean;
  onUnauthorized: () => void;
}) {
  const [status, setStatus] = useState<State>({ state: "idle" });
  const running = useRef<AbortController | null>(null);
  const key = expression ? JSON.stringify(expression) : "";

  const run = (target: AlertExpression) => {
    running.current?.abort();
    const controller = new AbortController();
    running.current = controller;
    setStatus({ state: "loading" });
    previewAlert(target, controller.signal)
      .then((preview) => {
        if (!controller.signal.aborted) setStatus({ state: "done", preview });
      })
      .catch((error) => {
        if (controller.signal.aborted) return;
        if (error instanceof APIError && error.status === 401) onUnauthorized();
        setStatus({ state: "error", message: alertMessage(error, target.kind) });
      });
  };

  // A changed alert makes the last preview stale; a keyword alert asks again.
  useEffect(() => {
    running.current?.abort();
    setStatus({ state: "idle" });
    if (!expression || onDemand) return;
    const timer = setTimeout(() => run(expression), DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [key, onDemand]);
  useEffect(() => () => running.current?.abort(), []);

  if (!expression) return null;
  return (
    <div className="alert-preview" aria-live="polite" aria-busy={status.state === "loading"}>
      {status.state === "idle" && onDemand && (
        <button type="button" className="button small" onClick={() => run(expression)}>
          Tester sur les derniers articles
        </button>
      )}
      {status.state === "loading" && (
        <p className="alert-preview-title" data-empty="true">
          Quivr cherche parmi les derniers articles…
        </p>
      )}
      {status.state === "error" && (
        <p className="alert-preview-title" data-state="error">
          {status.message}
        </p>
      )}
      {status.state === "done" && <Result preview={status.preview} />}
    </div>
  );
}

function Result({ preview }: { preview: Preview }) {
  const { evaluated, matched, complete, items } = preview;
  if (!evaluated)
    return (
      <p className="alert-preview-title" data-empty="true">
        Aucun article récent pour tester l’alerte.
      </p>
    );
  const scope = `Sur les ${evaluated} derniers articles`;
  return (
    <>
      <p className="alert-preview-title" data-empty={!matched || undefined}>
        {matched
          ? `${scope}, ${caught(matched)}.`
          : `${scope}, aucun n’aurait été attrapé.`}
      </p>
      {items.length > 0 && (
        <ul className="alert-preview-list" aria-label="Articles qui auraient été attrapés">
          {items.map((item) => (
            <li key={item.version_id}>
              <span className="alert-preview-article">{item.title || "Article sans titre"}</span>
              <span className="alert-preview-source">{sourceLabel(item.source)}</span>
            </li>
          ))}
        </ul>
      )}
      {!complete && (
        <p className="form-help">
          Le moteur a manqué de temps : seuls ces articles ont été jugés.
        </p>
      )}
    </>
  );
}
