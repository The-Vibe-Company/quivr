import { useId } from "react";
import {
  score,
  sourceLabel,
  whereFound,
  type CaughtArticle,
} from "../../lib/alerts";
import { shortTime } from "../../lib/format";
import { SourceLogo } from "../feed/SourceLogo";

const FIELDS: Record<string, string> = { source: "source", author: "auteur" };

/** What a described alert's score means, in words. */
const verdict = (value: number) =>
  value >= 0.85
    ? { label: "Très pertinent", text: "Quivr a lu tout l’article : il est en plein dans votre sujet." }
    : value >= 0.7
      ? { label: "Pertinent", text: "Quivr a lu tout l’article : il parle bien de votre sujet." }
      : { label: "Plutôt pertinent", text: "Quivr a lu tout l’article : il touche à votre sujet." };

/**
 * One article an alert caught, on one line like the feed: unread dot, the
 * source's logo, the title, then the source, when it arrived and why it
 * matched (words, or a score).
 */
export function CaughtItem({
  article,
  fresh,
  unread,
  open,
  arrived,
  connectorId,
  now,
  onOpen,
}: {
  article: CaughtArticle;
  fresh: boolean;
  unread: boolean;
  /** Shown in the reader right now. */
  open: boolean;
  /** When the feed saw it arrive, when it is still in the feed. */
  arrived?: string;
  connectorId?: string;
  now: number;
  onOpen: () => void;
}) {
  const tip = useId();
  const title = article.title || (article.available ? "Sans titre" : "Article retiré");
  const judged = article.score !== null ? verdict(article.score) : null;
  return (
    <li
      className="caught"
      data-record={article.record_id}
      data-fresh={fresh || undefined}
      data-unread={unread || undefined}
      data-open={open || undefined}
      data-scored={article.score !== null || undefined}
    >
      {unread && (
        <span className="caught-unread">
          <span className="visually-hidden">Non lu</span>
        </span>
      )}
      <SourceLogo namespace={article.source} connectorId={connectorId} size="small" />
      <div className="caught-body">
        {article.available ? (
          <button type="button" className="caught-title" title={title} aria-current={open || undefined} onClick={onOpen}>
            {title}
          </button>
        ) : (
          <span className="caught-title" data-unavailable="true">
            {title}
          </span>
        )}
        <div className="caught-meta">
          <span className="caught-source">{sourceLabel(article.source)}</span>
          {arrived && <span>{shortTime(arrived, now)}</span>}
          {fresh && <span className="new-badge">Nouveau</span>}
          {article.score === null && (
            <ul className="caught-why" aria-label="Mots trouvés">
              {article.terms.map((t) => (
                <li key={t.term}>
                  <span className="keyword">{t.term}</span>
                  {t.parts.length > 0 && <> dans {whereFound(t.parts)}</>}
                </li>
              ))}
              {article.fields.map((f) => (
                <li key={f.field}>
                  <span className="keyword keyword-field">
                    {FIELDS[f.field] || f.field} = {String(f.value)}
                  </span>
                </li>
              ))}
              {article.terms.length === 0 && article.fields.length === 0 && <li>aucun des mots exclus</li>}
            </ul>
          )}
        </div>
      </div>
      {article.score !== null && judged && (
        <span className="score-wrap">
          <span className="caught-score" data-testid="caught-score" tabIndex={0} aria-describedby={tip}>
            <span className="visually-hidden">Score </span>
            {score(article.score)}
          </span>
          <span className="score-tip" role="tooltip" id={tip}>
            <b>{judged.label}</b>
            {judged.text}
            {article.threshold !== null && (
              <span className="score-tip-note">L’alerte retient tout ce qui dépasse {score(article.threshold)}.</span>
            )}
          </span>
        </span>
      )}
    </li>
  );
}
