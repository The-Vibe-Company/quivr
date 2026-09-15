import { FileText, ArrowUpRight } from "@phosphor-icons/react";
import { Highlight } from "./Highlight";
import type { SearchResult } from "../types";
export function ResultItem({
  result,
  terms,
  onOpen,
}: {
  result: SearchResult;
  terms: string[];
  onOpen: () => void;
}) {
  const url = new URL(window.location.href);
  url.searchParams.set("record", result.record_id);
  url.searchParams.set("doc", result.version_id);
  return (
    <article className="result">
      <div className="result-path">
        <FileText size={16} aria-hidden="true" />
        <span>Espace démo</span>
        <span className="result-path-dot">/</span>
        <span>Texte ajouté</span>
        <span className="result-rank">
          {String(result.rank).padStart(2, "0")}
        </span>
      </div>
      <h2 className="result-title">
        <a
          className="result-link"
          href={url.pathname + url.search}
          onClick={(event) => {
            if (!event.metaKey && !event.ctrlKey && !event.shiftKey) {
              event.preventDefault();
              onOpen();
            }
          }}
        >
          Lire le passage <ArrowUpRight size={17} aria-hidden="true" />
        </a>
      </h2>
      <p className="result-snippet">
        <Highlight text={result.excerpt.text} terms={terms} />
      </p>
      <div className="result-meta">
        <span>Texte source</span>
        <span>
          {result.embedding_artifact_id
            ? "Recherche sémantique disponible"
            : "Recherche par mots-clés disponible"}
        </span>
      </div>
    </article>
  );
}
