import { useEffect, useMemo, useRef, useState } from "react";
import { APIError, tokenize } from "../../lib/search";
import {
  alertMessage,
  previewAlert,
  sourceLabel,
  type AlertExpression,
  type AlertPreview as Preview,
} from "../../lib/alerts";
import { arrivedAt } from "../../lib/alertStats";
import type { FeedItem } from "../../lib/feed";
import { shortTime } from "../../lib/format";
import { Highlight } from "../Highlight";
import { SourceLogo } from "../feed/SourceLogo";

type State =
  | { state: "idle" }
  | { state: "loading" }
  | { state: "done"; preview: Preview }
  | { state: "error"; message: string };

/** How long typing must pause before a keyword preview is asked for. */
const DEBOUNCE_MS = 600;

const decimal = new Intl.NumberFormat("fr-FR", { maximumFractionDigits: 1 });

/**
 * What the alert being written would have caught among the newest articles,
 * judged by the alert engine with the alert's own rules; nothing is saved.
 * A keyword alert is previewed as it is typed. A described alert asks a paid
 * classifier about every article, so it is previewed only on request.
 */
export function AlertPreview({
  expression,
  onDemand,
  terms,
  logoOf,
  feedItems,
  onUnauthorized,
}: {
  expression: AlertExpression | null;
  /** Preview only when the button is pressed (described alerts). */
  onDemand: boolean;
  /** Words to highlight in the titles. */
  terms: string[];
  logoOf: Map<string, string>;
  /** The feed's articles: when the judged ones arrived. */
  feedItems: FeedItem[];
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
      {status.state === "done" && (
        <Result preview={status.preview} terms={terms} logoOf={logoOf} feedItems={feedItems} />
      )}
    </div>
  );
}

function Result({
  preview,
  terms,
  logoOf,
  feedItems,
}: {
  preview: Preview;
  terms: string[];
  logoOf: Map<string, string>;
  feedItems: FeedItem[];
}) {
  const { evaluated, matched, complete, items } = preview;
  const highlighted = useMemo(() => terms.flatMap((t) => tokenize(t)), [terms]);
  // The judged articles are the newest ones: when they were published says
  // over how long, hence how often the alert would fire (a feed's poll
  // brings many at once, so their arrival says less).
  const { byRecord, perDay } = useMemo(() => {
    const byRecord = new Map(feedItems.map((i) => [i.record_id, i]));
    const times = feedItems
      .map((i) => Date.parse(i.published_at || arrivedAt(i)))
      .filter((t) => !Number.isNaN(t))
      .sort((a, b) => b - a)
      .slice(0, evaluated);
    const span = times.length ? Date.now() - times[times.length - 1] : 0;
    return { byRecord, perDay: span > 0 && matched ? matched / Math.max(span / 86400000, 1 / 24) : null };
  }, [feedItems, evaluated, matched]);
  if (!evaluated)
    return (
      <p className="alert-preview-title" data-empty="true">
        Aucun article récent pour tester l’alerte.
      </p>
    );
  return (
    <>
      <div className="alert-preview-head">
        <p className="alert-preview-title" data-empty={!matched || undefined}>
          {matched ? (
            <b>
              {matched} article{matched > 1 ? "s" : ""}
            </b>
          ) : (
            <b>Aucun</b>
          )}{" "}
          sur les {evaluated} derniers
        </p>
        {perDay !== null && <span className="alert-preview-rate">≈ {decimal.format(perDay)} par jour</span>}
      </div>
      {items.length > 0 && (
        <ul className="alert-preview-list" aria-label="Articles qui auraient été attrapés">
          {items.map((item) => {
            const arrived = byRecord.get(item.record_id);
            const when = arrived && arrivedAt(arrived);
            return (
              <li key={item.version_id}>
                <SourceLogo namespace={item.source} connectorId={logoOf.get(item.source)} size="small" />
                <span className="alert-preview-body">
                  <span className="alert-preview-article">
                    <Highlight text={item.title || "Article sans titre"} terms={highlighted} />
                  </span>
                  <span className="alert-preview-source">
                    {sourceLabel(item.source)}
                    {when ? ` · ${shortTime(when)}` : ""}
                  </span>
                </span>
              </li>
            );
          })}
        </ul>
      )}
      {!complete && <p className="form-help">Le moteur a manqué de temps : seuls ces articles ont été jugés.</p>}
    </>
  );
}
