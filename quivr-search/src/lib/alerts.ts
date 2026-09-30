// Alerts through the demo facade (/demo/alerts, quivr-search/alerts.mjs).
// The facade forces the demo corpus, the alerts evaluator and the webhook
// destination; the browser only sends a name and an expression: keywords, or
// a plain-language description when the deployment has a classifier.
import { APIError, request } from "./search";
import { pollChanges } from "./connectors";
import type { KeywordExpression } from "./notation";

export interface DescribedExpression {
  kind: "described";
  description: string;
}

export type AlertExpression = KeywordExpression | DescribedExpression;

interface AlertBase {
  alert_id: string;
  name: string;
  enabled: boolean;
  match_count: number;
  capped: boolean;
}

export type Alert = AlertBase &
  (
    | { kind: "keywords"; expression: KeywordExpression }
    | { kind: "described"; expression: DescribedExpression }
  );

/** Bounds of a description, as in the alerts plugin's schema. */
export const DESCRIPTION = { min: 3, max: 1000 };

export interface CaughtArticle {
  match_id: string;
  record_id: string;
  version_id: string;
  available: boolean;
  title: string;
  excerpt: string;
  source: string;
  explanation: string;
  /** Matched terms as written in the query, and the Part roles they were found in. */
  terms: { term: string; parts: string[] }[];
  fields: { field: string; value: unknown }[];
  /** Described alerts: the classifier's score and the threshold it passed. */
  score: number | null;
  threshold: number | null;
}

export type AlertDetail = Alert & {
  matches: CaughtArticle[];
};

export interface AlertList {
  available: boolean;
  /** Whether this deployment offers described alerts (it has a classifier). */
  described: boolean;
  items: Alert[];
  /** Record id → ids of the alerts that caught it. */
  matched: Record<string, string[]>;
}

const path = (id: string) => `/demo/alerts/${encodeURIComponent(id)}`;
const key = () => crypto.randomUUID();

export const fetchAlerts = (signal?: AbortSignal) =>
  request<AlertList>("/demo/alerts", undefined, signal);

export const fetchAlert = (id: string, signal?: AbortSignal) =>
  request<AlertDetail>(path(id), undefined, signal);

/** The idempotency key is kept by the caller so a retried submit is not a second alert. */
export const createAlert = (
  name: string,
  expression: AlertExpression,
  idempotency_key: string,
) => request<Alert>("/demo/alerts", { name, expression, idempotency_key });

export const pauseAlert = (id: string) =>
  request<Alert>(`${path(id)}/pause`, { idempotency_key: key() });

export const resumeAlert = (id: string) =>
  request<Alert>(`${path(id)}/resume`, { idempotency_key: key() });

export const editAlert = (id: string, expression: AlertExpression) =>
  request<Alert>(`${path(id)}/edit`, { expression, idempotency_key: key() });

/** What an alert being written would have caught among the newest articles. */
export interface AlertPreview {
  /** Articles the alert engine judged, newest first. */
  evaluated: number;
  matched: number;
  /** False when the engine ran out of time before judging them all. */
  complete: boolean;
  /** The first caught articles, newest first (at most three); they are not Matches. */
  items: Omit<CaughtArticle, "match_id">[];
}

/** Nothing is saved: the engine judges recent articles with the alert's own rules. */
export const previewAlert = (expression: AlertExpression, signal?: AbortSignal) =>
  request<AlertPreview>("/demo/alerts/preview", { expression }, signal);

export const deleteAlert = (id: string) =>
  request<{ deleted: true }>(`${path(id)}/delete`, { idempotency_key: key() });

// French words for the stable public codes of the monitoring commands.
export function alertMessage(
  error: unknown,
  kind: Alert["kind"] = "keywords",
): string {
  if (!(error instanceof APIError))
    return error instanceof Error ? error.message : "La demande a échoué.";
  switch (error.code) {
    case "invalid_expression":
      return kind === "described"
        ? "Le moteur d’alertes refuse cette description : les alertes décrites ne sont peut-être pas activées."
        : "Le moteur d’alertes refuse cette requête. Vérifiez les mots et les parenthèses.";
    case "unsupported_evaluator":
      return "Le moteur d’alertes n’est pas installé sur ce déploiement.";
    case "subscription_deleted":
    case "saved_query_deleted":
      return "Cette alerte a été supprimée entre-temps.";
    case "evaluator_unavailable":
    case "evaluator_error":
      return kind === "described"
        ? "Le classifieur ne répond pas pour le moment. Réessayez dans un instant."
        : "Le moteur d’alertes ne répond pas pour le moment. Réessayez dans un instant.";
    case "idempotency_conflict":
      return "Une demande précédente différente est en cours. Rechargez la page puis réessayez.";
    default:
      return error.message;
  }
}

const PARTS: Record<string, string> = {
  title: "le titre",
  body: "le texte",
  summary: "le résumé",
  caption: "une légende",
};

/** « orage » dans le titre et le texte */
export function whereFound(parts: string[]): string {
  const names = [...new Set(parts.map((p) => PARTS[p] || `« ${p} »`))];
  if (!names.length) return "";
  return names.length === 1
    ? names[0]
    : `${names.slice(0, -1).join(", ")} et ${names.at(-1)}`;
}

/** 0.97 → « 0,97 » */
export const score = (value: number) =>
  value.toLocaleString("fr-FR", {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  });

export const sourceLabel = (namespace: string) =>
  namespace === "web-demo"
    ? "Ajouté à la main"
    : namespace || "Source inconnue";

const LIVE_INTERVAL = 3000;

/**
 * Follows the corpus change feed from now on and calls onChange after a
 * match.*, subscription.* or saved_query.* event. Without change-feed access,
 * it calls onChange on a slower timer instead. Returns the function that stops it.
 */
export function followMonitoring({
  onChange,
  onLive,
  onUnauthorized,
}: {
  onChange: (signal: AbortSignal) => Promise<void>;
  onLive?: (live: boolean) => void;
  onUnauthorized: () => void;
}): () => void {
  const controller = new AbortController();
  let cursor: string | null = null;
  let feed = true;
  let timer: ReturnType<typeof setTimeout>;
  const tick = async () => {
    if (!document.hidden) {
      try {
        let changed = !feed;
        if (feed)
          for (let pages = 0; pages < 10; pages++) {
            const page = await pollChanges(cursor, controller.signal);
            changed ||= page.items.some((e) =>
              /^(match|subscription|saved_query)\./.test(e.type),
            );
            cursor = page.next_cursor;
            onLive?.(true);
            if (!page.has_more) break;
          }
        if (changed) await onChange(controller.signal);
      } catch (e) {
        if (controller.signal.aborted) return;
        if (e instanceof APIError && (e.status === 409 || e.status === 410))
          cursor = null;
        else if (e instanceof APIError && e.status === 403) {
          feed = false;
          onLive?.(false);
        } else if (e instanceof APIError && e.status === 401) {
          onUnauthorized();
          return;
        }
      }
    }
    timer = setTimeout(tick, feed ? LIVE_INTERVAL : LIVE_INTERVAL * 4);
  };
  void tick();
  return () => {
    controller.abort();
    clearTimeout(timer);
  };
}
