import type { DocumentDetail, Mode, Receipt, SearchResponse } from "../types";
export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
    public retryable: boolean,
  ) {
    super(message);
  }
}
export async function request<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(path, {
    method: body === undefined ? "GET" : "POST",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: signal
      ? AbortSignal.any([signal, AbortSignal.timeout(20000)])
      : AbortSignal.timeout(20000),
  }).catch((error) => {
    if (signal?.aborted) throw error;
    throw new APIError(
      503,
      "Connexion interrompue. Réessayez dans un instant.",
      true,
    );
  });
  const data = await response.json();
  if (!response.ok)
    throw new APIError(
      response.status,
      data.code === "unsupported_search"
        ? "Cette recherche dépasse les limites disponibles. Essayez une requête plus courte."
        : data.message || "La demande a échoué.",
      data.retryable === true,
    );
  return data as T;
}
export const session = () =>
  request<{ corpus_id: string; name: string }>("/demo/session");
export const login = (password: string) => request("/demo/login", { password });
export const search = (
  query: string,
  mode: Mode,
  corpus: string,
  signal?: AbortSignal,
) =>
  request<SearchResponse>(
    "/v0/search",
    { query, mode, profile: "balanced", limit: 10, corpus_ids: [corpus] },
    signal,
  );
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
