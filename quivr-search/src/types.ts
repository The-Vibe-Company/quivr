export type Mode = "hybrid" | "lexical" | "semantic";
export interface Availability {
  state: string;
  searchable: boolean;
  is_current: boolean;
}
export interface Receipt {
  receipt_id: string;
  state: "pending" | "resolved";
  outcome?: string;
  record_id?: string;
  version_id?: string;
  availability?: Availability;
  processing: { state: string; phase?: string };
  diagnostics: Array<{ code: string; message: string }>;
}
export interface SearchResult {
  record_id: string;
  version_id: string;
  part_key: string;
  segment_id: string;
  segmentation_id: string;
  rank: number;
  excerpt: {
    text: string;
    start: number;
    end: number;
    coordinate_system: string;
  };
  embedding_artifact_id?: string;
  vector_space_id?: string;
  /** Why the retrieval plugin ranked this hit here, when it says so. */
  explanation?: string;
}
/** What a search answered by a retrieval plugin spent. */
export interface SearchUsage {
  rounds: number;
  elapsed_ms: number;
  paid_calls: number;
  cost_cents: number;
}
export interface SearchResponse {
  items: SearchResult[];
  retrieval_profile: { name: string; version: string; degraded?: SearchDegradation[] };
  usage?: SearchUsage;
  /** Corpora a filter on a field they lack left out of the search. */
  excluded_corpora?: Exclusion[];
}
export interface SearchDegradation {
  reason: "vectors_unavailable";
  corpus_ids: string[];
}
/** A corpus a filter left out, and the fields it lacks, as the engine says. */
export interface Exclusion {
  corpus_id: string;
  fields: string[];
}
export interface SearchProfile {
  name: string;
  description?: string;
  max_latency_ms?: number;
  max_cost_cents?: number;
  provider: { kind: "engine" | "plugin"; plugin_id?: string; plugin_version?: string };
}
export interface DocumentDetail {
  record_id: string;
  version_id: string;
  manifest: {
    parts: Array<{
      key: string;
      role: string;
      content: { kind: string; text: string };
    }>;
  };
  availability: Availability;
}
