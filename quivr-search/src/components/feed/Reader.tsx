import { useEffect, useMemo, useRef, useState } from "react";
import {
  ChevronDownIcon,
  ChevronUpIcon,
  ChevronsRightIcon,
  ExternalLinkIcon,
  SearchIcon,
} from "../RailIcons";
import { SourceLogo } from "./SourceLogo";
import { fetchDocument, search, tokenize } from "../../lib/search";
import { fetchAlert, type Alert, type CaughtArticle } from "../../lib/alerts";
import { diffWords, summarize, type Change } from "../../lib/diff";
import { HAND_NAMESPACE, type FeedItem } from "../../lib/feed";
import { hhmm, longTime } from "../../lib/format";
import type { DocumentDetail } from "../../types";
import type { Doc } from "../../App";
import { Highlight } from "../Highlight";
import { LoadingState, Notice } from "../ui";
import { displayName } from "../../lib/sourceNames";

const NEIGHBOURS = 3;
const SEED_CHARS = 400;

const sourceLabel = (namespace?: string) =>
  !namespace
    ? "Source"
    : namespace === HAND_NAMESPACE
      ? "Ajouté à la main"
      : displayName(namespace);

/** "liberation.fr" for an article's address. */
function host(link: string) {
  try {
    return new URL(link).hostname.replace(/^www\./, "");
  } catch {
    return "";
  }
}

/** The title and text parts of a Version, as the reader shows them. */
function readable(detail: DocumentDetail | null, fallback?: string) {
  const parts = (detail?.manifest.parts || []).filter(
    (part) => part.content.kind === "text" && part.role !== "source_html",
  );
  const titlePart = parts.find((part) => part.role === "title");
  const texts = parts.filter((part) => part !== titlePart);
  const title =
    titlePart?.content.text.trim() ||
    fallback ||
    texts[0]?.content.text.trim().split("\n")[0] ||
    "Sans titre";
  return { title, texts };
}

function Diff({ changes, label }: { changes: Change[]; label: string }) {
  return (
    <p className="reader-diff-text" aria-label={label}>
      {changes.map((c, i) =>
        c.kind === "removed" ? (
          <del key={i}>{c.text}</del>
        ) : c.kind === "added" ? (
          <ins key={i}>{c.text}</ins>
        ) : (
          <span key={i}>{c.text}</span>
        ),
      )}
    </p>
  );
}

/** Why an alert caught this article, in one sentence. */
function why(alert: Alert, match?: CaughtArticle) {
  if (!match) return "";
  if (match.score !== null)
    return `Quivr a jugé qu’il correspond à votre description (confiance ${Math.round(match.score * 100)} %).`;
  const words = [...new Set(match.terms.map((t) => t.term))];
  return words.length
    ? `Il contient ${words.map((w) => `« ${w} »`).join(", ")}.`
    : alert.kind === "keywords"
      ? "Il correspond aux règles de l’alerte."
      : "";
}

/**
 * The built-in reader, a panel that opens over the right of the page while
 * the feed stays in view: the article's text as collected, why an alert
 * caught it, a correction notice, articles on the same subject (a semantic
 * search seeded by this one) and the original on its site. A click outside
 * it, other than on an article, closes it.
 */
export function Reader({
  doc,
  item,
  corpus,
  terms,
  caught,
  feedById,
  logoOf,
  onClose,
  onStep,
  canStep,
  onOpen,
  onSimilar,
}: {
  doc: Doc;
  item?: FeedItem;
  corpus: string;
  terms: string[];
  caught: Alert[];
  feedById: Map<string, FeedItem>;
  /** The connector whose site gives each source its logo. */
  logoOf: Map<string, string>;
  onClose: () => void;
  /** Opens the article above or below in the feed. */
  onStep: (delta: 1 | -1) => void;
  canStep: { back: boolean; forward: boolean };
  onOpen: (record: string, version: string) => void;
  onSimilar: (text: string) => void;
}) {
  const [detail, setDetail] = useState<DocumentDetail | null>(null);
  const [previous, setPrevious] = useState<DocumentDetail | null>(null);
  const [view, setView] = useState<"current" | "changes" | "previous">(
    "current",
  );
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [matches, setMatches] = useState<Map<string, CaughtArticle>>(
    new Map(),
  );
  const [neighbours, setNeighbours] = useState<
    { record_id: string; version_id: string; namespace?: string; title: string; meta: string }[]
  >([]);
  const heading = useRef<HTMLHeadingElement>(null);

  // A click outside the panel closes it, unless it opens another article.
  useEffect(() => {
    const outside = (event: MouseEvent) => {
      const target = event.target as Element;
      if (!target.closest?.(".peek, .row, .caught, .menu, .rail, dialog, .toast-region")) onClose();
    };
    document.addEventListener("mousedown", outside);
    return () => document.removeEventListener("mousedown", outside);
  }, [onClose]);

  useEffect(() => {
    const controller = new AbortController();
    setError("");
    fetchDocument(doc.record, doc.version, controller.signal)
      .then(setDetail)
      .catch((e) => {
        if (!controller.signal.aborted) setError(e.message);
      });
    return () => controller.abort();
  }, [doc.record, doc.version, attempt]);

  // A corrected article: the Version it replaced, to show what changed.
  const previousId = item?.previous_version_id;
  useEffect(() => {
    setPrevious(null);
    setView("current");
    if (!previousId) return;
    const controller = new AbortController();
    fetchDocument(doc.record, previousId, controller.signal)
      .then(setPrevious)
      // Without the earlier text, the notice only says when it changed.
      .catch(() => {});
    return () => controller.abort();
  }, [doc.record, previousId]);

  // What each alert found in this article (its words, or its score).
  const caughtIds = caught.map((a) => a.alert_id).join(",");
  useEffect(() => {
    if (!caughtIds) return;
    const controller = new AbortController();
    Promise.all(
      caughtIds.split(",").map((id) =>
        fetchAlert(id, controller.signal).then(
          (d) =>
            [id, d.matches.find((m) => m.record_id === doc.record)] as const,
          () => [id, undefined] as const,
        ),
      ),
    ).then((pairs) => {
      if (controller.signal.aborted) return;
      setMatches(
        new Map(
          pairs.filter((p): p is readonly [string, CaughtArticle] => !!p[1]),
        ),
      );
    });
    return () => controller.abort();
  }, [caughtIds, doc.record]);

  const { title, texts } = readable(detail, item?.title);
  const before = previous && readable(previous);
  const changes = useMemo(
    () =>
      detail && before
        ? {
            title: diffWords(before.title, title),
            body: diffWords(
              before.texts.map((p) => p.content.text).join("\n\n"),
              texts.map((p) => p.content.text).join("\n\n"),
            ),
          }
        : null,
    // The texts are derived from these two Versions.
    [detail, previous, item?.title],
  );
  const shown = view === "previous" && before ? before : { title, texts };

  // "Sur le même sujet": a semantic search seeded by the article itself.
  const seed = detail
    ? `${title}\n${texts.map((p) => p.content.text).join("\n")}`.slice(
        0,
        SEED_CHARS,
      )
    : "";
  useEffect(() => {
    if (!seed) return;
    const controller = new AbortController();
    search(seed, "semantic", corpus, controller.signal, 12)
      .then((data) => {
        const seen = new Set([doc.record]);
        const found = [];
        for (const r of data.items) {
          if (seen.has(r.record_id)) continue;
          seen.add(r.record_id);
          const known = feedById.get(r.record_id);
          const at = known?.received_at || known?.published_at;
          found.push({
            record_id: r.record_id,
            version_id: known?.version_id || r.version_id,
            namespace: known?.namespace,
            title: known?.title || r.excerpt.text.slice(0, 110),
            meta: [sourceLabel(known?.namespace), at && longTime(at)]
              .filter(Boolean)
              .join(" · "),
          });
          if (found.length >= NEIGHBOURS) break;
        }
        setNeighbours(found);
      })
      // Semantic search may still be preparing: the section stays hidden.
      .catch(() => setNeighbours([]));
    return () => controller.abort();
    // feedById changes with every arrival; the neighbours need not follow.
  }, [seed, corpus, doc.record]);

  useEffect(() => {
    heading.current?.focus({ preventScroll: true });
  }, [doc.record]);

  const highlight = useMemo(
    () =>
      terms.length
        ? terms
        : [...matches.values()].flatMap((m) =>
            m.terms.flatMap((t) => tokenize(t.term)),
          ),
    [terms, matches],
  );
  const at = item?.received_at || item?.published_at;

  return (
    <aside className="panel reader" aria-labelledby="reader-title">
      <div className="reader-top">
        <button
          type="button"
          className="reader-tool"
          title="Fermer (Échap)"
          onClick={onClose}
        >
          <ChevronsRightIcon />
          <span className="visually-hidden">Fermer</span>
        </button>
        <span className="reader-steps">
          <button
            type="button"
            className="reader-tool"
            title="Article précédent (↑)"
            disabled={!canStep.back}
            onClick={() => onStep(-1)}
          >
            <ChevronUpIcon />
            <span className="visually-hidden">Article précédent</span>
          </button>
          <button
            type="button"
            className="reader-tool"
            title="Article suivant (↓)"
            disabled={!canStep.forward}
            onClick={() => onStep(1)}
          >
            <ChevronDownIcon size={16} />
            <span className="visually-hidden">Article suivant</span>
          </button>
        </span>
        <span className="reader-meta">
          {item?.namespace && (
            <SourceLogo
              namespace={item.namespace}
              connectorId={logoOf.get(item.namespace)}
              size="small"
            />
          )}
          <span className="reader-source">{sourceLabel(item?.namespace)}</span>
          {at && (
            <span className="reader-when">
              {item?.received_at ? "Arrivé à" : "Publié à"} {hhmm(at)} · {longTime(at)}
            </span>
          )}
        </span>
      </div>
      <div className="reader-scroll">
        <h2 id="reader-title" ref={heading} tabIndex={-1}>
          {shown.title}
        </h2>
        {caught.length > 0 && (
          <ul className="reader-caught" aria-label="Pourquoi cet article">
            {caught.map((a) => (
              <li key={a.alert_id}>
                <strong>Attrapé par votre alerte « {a.name} »</strong>
                {why(a, matches.get(a.alert_id)) &&
                  ` — ${why(a, matches.get(a.alert_id))}`}
              </li>
            ))}
          </ul>
        )}
        {item?.updated_at && (
          <div className="reader-updated">
            <p>
              {view === "previous"
                ? `Version précédente, remplacée ${longTime(item.updated_at)}.`
                : `Article corrigé ${longTime(item.updated_at)} : Quivr a reçu une nouvelle version et affiche celle-ci.`}
              {changes &&
                ` ${summarize([...changes.title, ...changes.body])}`}
            </p>
            {changes && (
              <div className="reader-updated-actions">
                <button
                  type="button"
                  className="button small"
                  aria-expanded={view === "changes"}
                  onClick={() =>
                    setView(view === "changes" ? "current" : "changes")
                  }
                >
                  {view === "changes"
                    ? "Masquer les changements"
                    : "Voir les changements"}
                </button>
                <button
                  type="button"
                  className="button small"
                  onClick={() =>
                    setView(view === "previous" ? "current" : "previous")
                  }
                >
                  {view === "previous"
                    ? "Revenir à la version actuelle"
                    : "Lire la version précédente"}
                </button>
              </div>
            )}
            {changes && view === "changes" && (
              <div className="reader-diff">
                <Diff changes={changes.title} label="Changements du titre" />
                <Diff changes={changes.body} label="Changements du texte" />
              </div>
            )}
          </div>
        )}
        {!detail && !error && <LoadingState label="Chargement de l’article…" rows={3} />}
        {error && (
          <Notice
            title="L’article ne s’affiche pas."
            onRetry={() => setAttempt((n) => n + 1)}
          >
            {error}
          </Notice>
        )}
        {detail &&
          shown.texts.map((part) => (
            <div
              key={part.key}
              className="reader-text"
              data-testid="canonical-text"
            >
              <Highlight text={part.content.text} terms={highlight} />
            </div>
          ))}
        {neighbours.length > 0 && (
          <section className="reader-near" aria-labelledby="reader-near-title">
            <h3 id="reader-near-title">Sur le même sujet</h3>
            <ul>
              {neighbours.map((n) => (
                <li key={n.record_id}>
                  <button
                    type="button"
                    onClick={() => onOpen(n.record_id, n.version_id)}
                  >
                    {n.namespace ? (
                      <SourceLogo
                        namespace={n.namespace}
                        connectorId={logoOf.get(n.namespace)}
                        size="small"
                      />
                    ) : (
                      <span className="reader-near-blank" aria-hidden="true" />
                    )}
                    <span className="reader-near-text">
                      <span className="reader-near-title">{n.title}</span>
                      {n.meta && (
                        <span className="reader-near-meta">{n.meta}</span>
                      )}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
      <div className="reader-foot">
        {item?.link && (
          <a
            className="reader-action"
            data-main
            href={item.link}
            target="_blank"
            rel="noopener noreferrer"
          >
            <ExternalLinkIcon />
            Ouvrir l’original
            <span className="reader-host">{host(item.link)}</span>
          </a>
        )}
        <button
          type="button"
          className="reader-action"
          onClick={() => onSimilar(title)}
        >
          <SearchIcon size={15} />
          Creuser le sujet
        </button>
      </div>
    </aside>
  );
}
