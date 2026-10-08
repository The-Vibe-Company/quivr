import type {
  DocumentDetail,
  Mode,
  Receipt,
  SearchProfile,
  SearchResponse,
} from "../types";
export class APIError extends Error {
  status: number;
  retryable: boolean;
  code: string;
  field: string;
  constructor(status: number, message: string, retryable: boolean, code = "", field = "") {
    super(message);
    this.status = status;
    this.retryable = retryable;
    this.code = code;
    this.field = field;
  }
}
// Reads started as the page's script runs (main.tsx), before React's first
// render; the first request for each path takes its answer.
const early = new Map<string, Promise<Response | null>>();
export function readEarly(paths: string[]) {
  for (const path of paths)
    early.set(
      path,
      fetch(path, { signal: AbortSignal.timeout(20000) }).catch(() => null),
    );
}
export async function request<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
  method: "GET" | "POST" | "PUT" = body === undefined ? "GET" : "POST",
  timeout = 20000,
): Promise<T> {
  const started = method === "GET" ? early.get(path) : undefined;
  early.delete(path);
  // An early read that failed (signed out, say) is asked again; a caller that
  // gave up meanwhile gets its abort, not the answer.
  const ready = await started;
  signal?.throwIfAborted();
  const response = ready?.ok
    ? ready
    : await fetch(path, {
        method,
        headers: { "Content-Type": "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: signal
          ? AbortSignal.any([signal, AbortSignal.timeout(timeout)])
          : AbortSignal.timeout(timeout),
      }).catch((error) => {
        if (signal?.aborted) throw error;
        throw new APIError(
          503,
          "Connexion interrompue. Réessayez dans un instant.",
          true,
        );
      });
  const data = await response.json();
  // The demo's databases were reset: its session names a corpus that is gone.
  if (data.code === "demo_corpus_changed") location.reload();
  if (!response.ok)
    throw new APIError(
      response.status,
      data.code === "query_too_long"
        ? "Cette requête est trop longue. Raccourcissez-la."
        : data.code === "unsupported_search"
          ? "Cette recherche dépasse les limites disponibles. Essayez une requête plus courte."
          : data.message || "La demande a échoué.",
      data.retryable === true,
      typeof data.code === "string" ? data.code : "",
      typeof data.field === "string" ? data.field : "",
    );
  return data as T;
}
export const session = () =>
  request<{ corpus_id: string; name: string }>("/demo/session");
export const login = (password: string) => request("/demo/login", { password });
export const search = (
  query: string,
  mode: Mode,
  corpora: string | string[],
  signal?: AbortSignal,
  limit = 10,
  sources: string[] = [],
  profile: "default" | "deep" = "default",
) =>
  request<SearchResponse>(
    "/v0/search",
    {
      query,
      mode,
      profile,
      limit,
      corpus_ids: typeof corpora === "string" ? [corpora] : corpora,
      // The engine ranks within these sources, before the limit.
      ...(sources.length ? { filter: { source_namespaces: sources } } : {}),
    },
    signal,
  );
export const searchProfiles = (signal?: AbortSignal) =>
  request<{ items: SearchProfile[] }>("/v0/search/profiles", undefined, signal);
export const fetchDocument = (
  record: string,
  version: string,
  signal?: AbortSignal,
) =>
  request<DocumentDetail>(
    `/v0/records/${encodeURIComponent(record)}/versions/${encodeURIComponent(version)}`,
    undefined,
    signal,
  );
export const addText = (text: string, key: string, corpus: string) =>
  request<Receipt>("/v0/records", {
    idempotency_key: key,
    source: { corpus_id: corpus, namespace: "web-demo", record_key: key },
    content: { kind: "text", text },
  });
export const fetchReceipt = (id: string, signal?: AbortSignal) =>
  request<Receipt>(
    `/v0/ingestion-receipts/${encodeURIComponent(id)}`,
    undefined,
    signal,
  );
// Highlighting only: never rewrite the query or canonical source sent to the core.
export const tokenize = (query: string) =>
  query
    .normalize("NFD")
    .replace(/\p{M}/gu, "")
    .toLowerCase()
    .split(/[^\p{L}\p{N}]+/u)
    .filter((word) => word.length > 1)
    .slice(0, 32);
