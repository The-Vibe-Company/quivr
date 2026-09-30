// Connector Instances through the demo facade. The facade holds the core API
// key and fences every call to the demo corpus; secrets typed in the browser
// travel once, in the submit request body, and are never read back.
import { APIError, request } from "./search";

export type JSONSchema = {
  type?: string | string[];
  title?: string;
  description?: string;
  properties?: Record<string, JSONSchema>;
  required?: string[];
  items?: JSONSchema;
  enum?: unknown[];
  oneOf?: JSONSchema[];
  anyOf?: JSONSchema[];
  examples?: unknown[];
  pattern?: string;
  minLength?: number;
  maxLength?: number;
  minimum?: number;
  maximum?: number;
  writeOnly?: boolean;
  default?: unknown;
  [key: string]: unknown;
};

export type HealthState =
  "active" | "silent" | "access_error" | "credential_expiring" | "disabled";

export interface ConnectorKind {
  kind: string;
  title: string;
  description?: string;
  config_schema: JSONSchema;
  credential_schema?: JSONSchema;
  credential: "none" | "optional" | "required";
  default_interval_seconds: number;
}

export interface KindCatalog {
  credential_deposits: "available" | "unavailable";
  min_interval_seconds: number;
  items: ConnectorKind[];
}

export interface Connector {
  connector_id: string;
  corpus_id: string;
  source_namespace: string;
  kind: string;
  config: Record<string, unknown>;
  schedule: { interval_seconds: number };
  health_policy: {
    silent_after_seconds: number;
    credential_warning_seconds: number;
  };
  enabled: boolean;
  created_at: string;
  disabled_at?: string;
  credential?: { version: number; deposited_at: string; expires_at?: string };
  health: {
    state: HealthState;
    evaluated_at: string;
    last_success_at?: string;
    last_item_at?: string;
    last_error?: { code: string; at: string };
    usage?: {
      day: string;
      items_read: number;
      previous_day_items_read: number;
    };
  };
}

export interface ChangeEvent {
  event_id: string;
  type: string;
  resource: { kind: string; id: string };
}

const connectorPath = (id: string) =>
  `/v0/connectors/${encodeURIComponent(id)}`;

export const fetchKinds = (signal?: AbortSignal) =>
  request<KindCatalog>("/v0/connector-kinds", undefined, signal);

export async function fetchConnectors(signal?: AbortSignal) {
  const items: Connector[] = [];
  let cursor: string | undefined;
  // The facade forces the demo corpus; a handful of pages is plenty here.
  for (let page = 0; page < 20; page++) {
    const query = cursor ? `?page_cursor=${encodeURIComponent(cursor)}` : "";
    const data = await request<{
      items: Connector[];
      next_page_cursor?: string;
    }>(`/v0/connectors${query}`, undefined, signal);
    items.push(...data.items);
    cursor = data.next_page_cursor;
    if (!cursor) break;
  }
  return items;
}

export const fetchConnector = (id: string, signal?: AbortSignal) =>
  request<Connector>(connectorPath(id), undefined, signal);

export interface CreateConnector {
  idempotency_key: string;
  corpus_id: string;
  source_namespace: string;
  kind: string;
  config: unknown;
  schedule?: { interval_seconds: number };
  credential?: { secret: unknown; expires_at?: string };
}

export const createConnector = (body: CreateConnector) =>
  request<Connector>("/v0/connectors", body);

export const changeSchedule = (id: string, interval_seconds: number) =>
  request<Connector>(
    `${connectorPath(id)}/schedule`,
    { interval_seconds },
    undefined,
    "PUT",
  );

export const rotateCredential = (
  id: string,
  body: { idempotency_key: string; secret: unknown; expires_at?: string },
) =>
  request<Connector>(`${connectorPath(id)}/credential`, body, undefined, "PUT");

export const disableConnector = (id: string, key: string) =>
  request<Connector>(`${connectorPath(id)}/disable`, { idempotency_key: key });

/** Asks the core to check the source now; run_at is when the run is due. */
export const requestRun = (id: string, key: string) =>
  request<{ connector_id: string; run_at: string }>(`${connectorPath(id)}/runs`, {
    idempotency_key: key,
  });

// Sources (THE-732): feed discovery, suggestions and removal are facade
// routes; the facade fetches the address under the private-address refusal.
export interface FeedChoice {
  url: string;
  title: string;
}

export const discoverFeeds = (url: string, signal?: AbortSignal) =>
  request<{ feeds: FeedChoice[] }>("/demo/feeds/discover", { url }, signal);

export const fetchSuggestions = (signal?: AbortSignal) =>
  request<{ items: FeedChoice[] }>(
    "/demo/feeds/suggestions",
    undefined,
    signal,
  );

export const removeSource = (id: string) =>
  request<{ removed: string[] }>("/demo/sources/remove", {
    connector_id: id,
  });

export const pollChanges = (cursor: string | null, signal?: AbortSignal) =>
  request<{ items: ChangeEvent[]; next_cursor: string; has_more?: boolean }>(
    `/v0/changes${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`,
    undefined,
    signal,
  );

// French messages for the stable public codes of connector commands.
export function connectorMessage(error: unknown, minInterval = 30): string {
  if (!(error instanceof APIError))
    return error instanceof Error ? error.message : "La demande a échoué.";
  switch (error.code) {
    case "invalid_config":
      return "Cette valeur n’est pas acceptée par le connecteur.";
    case "invalid_credential":
      return "Cet identifiant n’a pas le format attendu.";
    case "invalid_interval":
      return `L’intervalle doit être compris entre ${formatInterval(minInterval)} et 24 h.`;
    case "invalid_schema":
    case "invalid_input":
      return "Cette valeur n’est pas valide.";
    case "source_namespace_in_use":
      return "Un connecteur actif utilise déjà cet espace de noms.";
    case "unsupported_connector_kind":
      return "Ce type de connecteur n’est pas disponible sur ce déploiement.";
    case "credentials_unavailable":
      return "Le dépôt d’identifiants est désactivé sur ce déploiement.";
    case "connector_disabled":
      return "Ce connecteur est désactivé.";
    case "idempotency_conflict":
      return "Une demande précédente différente est en cours de traitement. Vérifiez la liste puis réessayez.";
    case "forbidden":
      return "Les connecteurs ne sont pas activés sur ce déploiement.";
    default:
      return error.message;
  }
}

export function formatInterval(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600} h`;
  if (seconds % 60 === 0) return `${seconds / 60} min`;
  return `${seconds} s`;
}

const relative = new Intl.RelativeTimeFormat("fr", { numeric: "auto" });
const absolute = new Intl.DateTimeFormat("fr", {
  dateStyle: "medium",
  timeStyle: "short",
});

export function formatAbsolute(value: string): string {
  return absolute.format(new Date(value));
}

export function formatRelative(value: string, now = Date.now()): string {
  const seconds = Math.round((new Date(value).getTime() - now) / 1000);
  const steps: [Intl.RelativeTimeFormatUnit, number][] = [
    ["day", 86400],
    ["hour", 3600],
    ["minute", 60],
  ];
  for (const [unit, size] of steps)
    if (Math.abs(seconds) >= size)
      return relative.format(Math.round(seconds / size), unit);
  return Math.abs(seconds) < 10
    ? "à l’instant"
    : relative.format(seconds, "second");
}
