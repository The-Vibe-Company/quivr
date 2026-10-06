import { useEffect, useState } from "react";
import { ArrowSquareOut, CaretDown } from "@phosphor-icons/react";
import { APIError } from "../../lib/search";
import {
  changeSummary,
  countLabel,
  fetchRecordDetail,
  fetchVersion,
  fieldValues,
  textsOf,
  valueLabel,
  wordCount,
  type RecordDetail,
  type VersionDetail,
} from "../../lib/explore";
import { Notice } from "../ui";
import { dayLabel, timeLabel } from "./WireList";

// The history shows this many Versions, the newest.
const MAX_VERSIONS = 10;

/**
 * The preview panel (THE-1204, THE-1211): the selected document read
 * without leaving the list. Its language, date and whether it was
 * corrected; its headline and lede; a few fields; its Versions on a line,
 * each with what changed from the one before; and what Quivr keeps of its
 * source, on demand. The document shown stays, dimmed, until the next one
 * is read, so the panel never collapses between two.
 */
export function Preview({
  id,
  onOpen,
  onUnauthorized,
}: {
  id: string;
  onOpen: () => void;
  onUnauthorized: () => void;
}) {
  const [detail, setDetail] = useState<RecordDetail | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  // Every Version read so far, by id, to compare each with the one before.
  const [read, setRead] = useState<Map<string, VersionDetail>>(new Map());
  const [source, setSource] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    setError("");
    setSource(false);
    fetchRecordDetail(id, controller.signal)
      .then((data) => {
        setDetail(data);
        setRead(new Map(data.version ? [[data.version.version_id, data.version]] : []));
      })
      .catch((e) => {
        if (controller.signal.aborted) return;
        if (e instanceof APIError && e.status === 401) return onUnauthorized();
        setError(e instanceof Error ? e.message : "Ce document ne s’affiche pas.");
      });
    return () => controller.abort();
  }, [id, attempt, onUnauthorized]);

  // The earlier Versions, each read once per document shown; one that
  // cannot be read says so and can be asked again.
  const [failed, setFailed] = useState<Set<string>>(new Set());
  const [again, setAgain] = useState(0);
  useEffect(() => {
    // The document left behind while the new one loads is not read again.
    if (!detail || detail.record.record_id !== id) return;
    const controller = new AbortController();
    setFailed(new Set());
    for (const { version_id } of detail.versions.slice(0, MAX_VERSIONS))
      if (version_id !== detail.version?.version_id)
        fetchVersion(id, version_id, controller.signal)
          .then((v) => {
            if (!controller.signal.aborted) setRead((known) => new Map(known).set(v.version_id, v));
          })
          .catch((e) => {
            if (controller.signal.aborted) return;
            if (e instanceof APIError && e.status === 401) return onUnauthorized();
            setFailed((known) => new Set(known).add(version_id));
          });
    return () => controller.abort();
  }, [id, detail, again, onUnauthorized]);

  if (error)
    return (
      <Notice title="Ce document ne s’affiche pas." onRetry={() => setAttempt((n) => n + 1)}>
        {error}
      </Notice>
    );
  if (!detail) return <PreviewSkeleton />;

  const { record, corpus, blobs } = detail;
  const loading = record.record_id !== id;
  const version = detail.version;
  if (!version)
    return (
      <p className="facets-note" data-pending={loading || undefined}>
        Ce document n’a plus de version lisible.
      </p>
    );
  const versions = detail.versions.slice(0, MAX_VERSIONS);
  const field = (name: string) => [...corpus.common, ...corpus.own].find((f) => f.name === name);
  const values = (name: string) => {
    const f = field(name);
    return f ? fieldValues(version, f) : [];
  };
  const { title, texts } = textsOf(version);
  const headline = title || texts[0]?.text.trim().split("\n")[0] || "Sans titre";
  const lede = (title ? texts[0]?.text : texts[1]?.text)?.trim();
  const language = String(values("metadata.language")[0] || "");
  const at = String(values("metadata.published_at")[0] || version.accepted_at || "");
  const corrected = detail.versions.length > 1;
  const list = (name: string) => values(name).map((v) => valueLabel(v, undefined, name)).join(", ");
  const rows = [
    ["Lieu", list("metadata.place")],
    ["Pays", list("metadata.country")],
    ["Sujets", list("metadata.subjects")],
    ["Source", list("metadata.source")],
    ["Mots", countLabel(wordCount(version))],
  ].filter(([, v]) => v);
  const xml = blobs.some((b) => /xml/i.test(b.media_type));
  const sourceParts = version.manifest.parts.filter((p) => p.role === "source_html" || p.content.kind === "blob");
  const raw = {
    extensions: version.extensions || {},
    provenance: version.provenance || {},
    ...(sourceParts.length ? { source_parts: sourceParts } : {}),
  };
  // A Version's time, with its day when it is not the document's.
  const when = (iso: string) => (
    <>
      {dayLabel(iso) !== dayLabel(at) && `${dayLabel(iso)} `}
      <span className="preview-time">{timeLabel(iso)}</span>
    </>
  );

  return (
    <article className="preview" aria-labelledby="preview-title" aria-busy={loading || undefined} data-pending={loading || undefined}>
      <p className="preview-meta">
        {language && <span className="wire-lang">{language.slice(0, 3).toUpperCase()}</span>}
        {at && (
          <time dateTime={at}>
            {dayLabel(at)} <span className="preview-time">{timeLabel(at)}</span>
          </time>
        )}
        {corrected && <span className="preview-corrected">corrigée</span>}
        {record.withdrawn && <span className="record-withdrawn">Retiré</span>}
      </p>
      <h2 id="preview-title" dir="auto">
        {headline}
      </h2>
      {lede && (
        <p className="preview-lede" dir="auto">
          {lede}
        </p>
      )}
      {rows.length > 0 && (
        <dl className="preview-fields">
          {rows.map(([label, value]) => (
            <div key={label}>
              <dt>{label}</dt>
              <dd dir="auto">{value}</dd>
            </div>
          ))}
        </dl>
      )}
      <section className="preview-versions" aria-labelledby="preview-versions-title">
        <h3 id="preview-versions-title">
          Versions <span className="preview-count">{detail.versions.length}</span>
        </h3>
        <ol>
          {versions.map((v, i) => {
            const before = versions[i + 1];
            const mine = read.get(v.version_id);
            const theirs = before && read.get(before.version_id);
            const n = detail.versions.length - i;
            return (
              <li key={v.version_id} data-current={i === 0 || undefined}>
                <span className="preview-version-n">v{n}</span>
                {v.accepted_at && <time dateTime={v.accepted_at}>{when(v.accepted_at)}</time>}
                <span className="preview-version-change">
                  {!before && n === 1 ? (
                    "première version"
                  ) : !before ? (
                    "versions plus anciennes non lues"
                  ) : mine && theirs ? (
                    changeSummary(theirs, mine)
                  ) : failed.has(v.version_id) || failed.has(before.version_id) ? (
                    <>
                      changements illisibles pour l’instant{" "}
                      <button type="button" className="link-button" onClick={() => setAgain((n) => n + 1)}>
                        Réessayer
                      </button>
                    </>
                  ) : (
                    "…"
                  )}
                </span>
              </li>
            );
          })}
        </ol>
      </section>
      <div className="preview-actions">
        <button type="button" className="button preview-open" onClick={onOpen}>
          Ouvrir <ArrowSquareOut size={14} aria-hidden="true" />
        </button>
        <button
          type="button"
          className="preview-source-toggle"
          aria-expanded={source}
          aria-controls={source ? "preview-source" : undefined}
          onClick={() => setSource((open) => !open)}
        >
          {xml ? "Source XML" : "Source brute"}
          <CaretDown size={12} aria-hidden="true" />
        </button>
      </div>
      {source && (
        <section id="preview-source" className="preview-source" aria-label={xml ? "Source XML" : "Source brute"}>
          <p className="facets-note">Ce que Quivr garde de la source : ses extensions, sa provenance et ses parties d’origine.</p>
          <pre>{JSON.stringify(raw, null, 2)}</pre>
        </section>
      )}
    </article>
  );
}

/** The panel's shape while the first document is read. */
function PreviewSkeleton() {
  return (
    <div className="preview preview-skeleton">
      <p role="status" className="visually-hidden">
        Chargement de l’aperçu…
      </p>
      <div aria-hidden="true">
        <span className="skeleton-bar" style={{ width: "40%" }} />
        <span className="skeleton-bar skeleton-title" style={{ width: "92%" }} />
        <span className="skeleton-bar skeleton-title" style={{ width: "70%" }} />
        <span className="skeleton-bar skeleton-soft" style={{ width: "96%" }} />
        <span className="skeleton-bar skeleton-soft" style={{ width: "88%" }} />
        <span className="skeleton-bar skeleton-soft" style={{ width: "60%" }} />
      </div>
    </div>
  );
}
