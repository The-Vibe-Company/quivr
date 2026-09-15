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
}
export interface SearchResponse {
  items: SearchResult[];
  retrieval_profile: { name: string; version: string };
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
