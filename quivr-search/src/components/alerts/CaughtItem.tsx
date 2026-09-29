import { useMemo } from "react";
import { Highlight } from "../Highlight";
import { tokenize } from "../../lib/search";
import { sourceLabel, whereFound, type CaughtArticle } from "../../lib/alerts";

const FIELDS: Record<string, string> = { source: "source", author: "auteur" };

/** One article an alert caught: where it comes from, and why it matched. */
export function CaughtItem({
  article,
  fresh,
  onOpen,
}: {
  article: CaughtArticle;
  fresh: boolean;
  onOpen: () => void;
}) {
  const terms = useMemo(
    () => article.terms.flatMap((t) => tokenize(t.term)),
    [article.terms],
  );
  return (
    <li className="caught" data-fresh={fresh || undefined}>
      <div className="caught-source">
        <span>{sourceLabel(article.source)}</span>
        {fresh && <span className="new-badge">Nouveau</span>}
      </div>
      {article.available ? (
        <button type="button" className="caught-title" onClick={onOpen}>
          <Highlight text={article.title || "Sans titre"} terms={terms} />
        </button>
      ) : (
        <span className="caught-title" data-unavailable="true">
          {article.title || "Article retiré"}
        </span>
      )}
      {article.excerpt && (
        <p className="caught-excerpt">
          <Highlight text={article.excerpt} terms={terms} />
        </p>
      )}
      <div className="caught-why">
        <span className="muted">Mots trouvés :</span>
        <ul aria-label="Mots trouvés">
          {article.terms.map((t) => (
            <li key={t.term}>
              <span className="keyword">{t.term}</span>
              {t.parts.length > 0 && (
                <span className="muted"> dans {whereFound(t.parts)}</span>
              )}
            </li>
          ))}
          {article.fields.map((f) => (
            <li key={f.field}>
              <span className="keyword keyword-field">
                {FIELDS[f.field] || f.field} = {String(f.value)}
              </span>
            </li>
          ))}
          {article.terms.length === 0 && article.fields.length === 0 && (
            <li className="muted">aucun des mots exclus</li>
          )}
        </ul>
      </div>
    </li>
  );
}
