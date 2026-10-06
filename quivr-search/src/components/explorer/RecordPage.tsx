import { useEffect, useMemo, useState } from "react";
import { ArrowLeft } from "@phosphor-icons/react";
import { APIError } from "../../lib/search";
import { fieldLabel } from "../../lib/corpora";
import {
  fetchRecordDetail,
  fetchVersion,
  fieldValues,
  sizeLabel,
  textsOf,
  valueLabel,
  type RecordDetail,
  type VersionDetail,
} from "../../lib/explore";
import { diffWords, summarize } from "../../lib/diff";
import { formatAbsolute } from "../../lib/connectors";
import { displayName } from "../../lib/sourceNames";
import { LoadingState, Notice } from "../ui";

const fullText = (version: VersionDetail) => {
  const { title, texts } = textsOf(version);
  return [title, ...texts.map((t) => t.text)].filter(Boolean).join("\n\n");
};

/**
 * One document of the Explorer: its text, every metadata field, the
 * Versions the demo read with what changed from one to the next, and what
 * Quivr keeps of its source in a panel that opens on demand.
 */
export function RecordPage({
  id,
  onBack,
  onUnauthorized,
}: {
  id: string;
  onBack: () => void;
  onUnauthorized: () => void;
}) {
  const [detail, setDetail] = useState<RecordDetail | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  // The Version shown, and the ones read to compare, by id.
  const [shown, setShown] = useState<string | null>(null);
  const [read, setRead] = useState<Map<string, VersionDetail>>(new Map());

  useEffect(() => {
    const controller = new AbortController();
    setError("");
    fetchRecordDetail(id, controller.signal)
      .then((data) => {
        setDetail(data);
        setShown(data.version?.version_id || null);
        setRead(new Map(data.version ? [[data.version.version_id, data.version]] : []));
      })
      .catch((e) => {
        if (controller.signal.aborted) return;
        if (e instanceof APIError && e.status === 401) return onUnauthorized();
        setError(e instanceof Error ? e.message : "Ce document ne s’affiche pas.");
      });
    return () => controller.abort();
  }, [id, attempt, onUnauthorized]);

  // The Version shown and the one before it, read when not known yet.
  const versions = detail?.versions || [];
  const at = versions.findIndex((v) => v.version_id === shown);
  const before = at >= 0 ? versions[at + 1] : undefined;
  useEffect(() => {
    const controller = new AbortController();
    for (const wanted of [shown, before?.version_id])
      if (wanted && !read.has(wanted))
        fetchVersion(id, wanted, controller.signal)
          .then((v) => setRead((known) => new Map(known).set(v.version_id, v)))
          .catch(() => {
            // The history says the earlier text could not be read.
          });
    return () => controller.abort();
  }, [id, shown, before?.version_id, read]);

  const version = shown ? read.get(shown) : undefined;
  const previous = before ? read.get(before.version_id) : undefined;
  const changes = useMemo(
    () => (version && previous ? diffWords(fullText(previous), fullText(version)) : null),
    [version, previous],
  );

  const back = (
    <button type="button" className="link-button explorer-back" onClick={onBack}>
      <ArrowLeft size={14} aria-hidden="true" /> Tous les documents
    </button>
  );
  if (error)
    return (
      <div className="record-page">
        {back}
        <Notice title="Ce document ne s’affiche pas." onRetry={() => setAttempt((n) => n + 1)}>
          {error}
        </Notice>
      </div>
    );
  if (!detail) return <LoadingState label="Chargement du document…" rows={4} />;

  const { record, corpus, blobs } = detail;
  const current = detail.version;
  const shownText = version ? textsOf(version) : null;
  const fields = [...corpus.common, ...corpus.own];
  const values = version
    ? fields
        .map((field) => ({ field, values: fieldValues(version, field) }))
        .filter((f) => f.values.length)
    : [];
  const sourceParts = version?.manifest.parts.filter(
    (p) => p.role === "source_html" || p.content.kind === "blob",
  );
  const raw = version && {
    extensions: version.extensions || {},
    provenance: version.provenance || {},
    ...(sourceParts?.length ? { source_parts: sourceParts } : {}),
  };

  return (
    <article className="record-page" aria-labelledby="record-title">
      {back}
      <header className="record-head">
        <p className="row-meta">
          <span className="row-corpus">{corpus.name}</span>
          <span className="row-source">{displayName(record.source.namespace)}</span>
          {current?.accepted_at && (
            <time className="row-when" dateTime={current.accepted_at}>
              {formatAbsolute(current.accepted_at)}
            </time>
          )}
          {record.withdrawn && <span className="record-withdrawn">Retiré</span>}
        </p>
        <h2 id="record-title">{shownText?.title || "Sans titre"}</h2>
        {version && current && version.version_id !== current.version_id && (
          <p className="record-older" role="note">
            Vous lisez une version antérieure.{" "}
            <button type="button" className="link-button" onClick={() => setShown(current.version_id)}>
              Revenir à la version actuelle
            </button>
          </p>
        )}
      </header>
      <div className="record-grid">
        <section className="panel panel-pad record-text" aria-label="Texte">
          {!version ? (
            <LoadingState label="Chargement du texte…" rows={3} />
          ) : shownText?.texts.length ? (
            shownText.texts.map((t) => (
              <p key={t.key} className="reader-text">
                {t.text.trim()}
              </p>
            ))
          ) : (
            <p className="muted">Ce document n’a pas de texte en dehors de son titre.</p>
          )}
        </section>
        <div className="record-side">
          <section className="panel panel-pad record-meta" aria-labelledby="meta-title">
            <h3 id="meta-title">Métadonnées</h3>
            <dl>
              {values.map(({ field, values: list }) => (
                <div key={field.name}>
                  <dt>{fieldLabel(field.name)}</dt>
                  <dd>{list.map((v) => valueLabel(v, field.type, field.name)).join(", ")}</dd>
                </div>
              ))}
              <div>
                <dt>Corpus</dt>
                <dd>{corpus.name}</dd>
              </div>
              <div>
                <dt>Clé dans la source</dt>
                <dd className="mono">{record.source.record_key}</dd>
              </div>
              <div>
                <dt>Document</dt>
                <dd className="mono">{record.record_id}</dd>
              </div>
              {version?.provenance?.producer && (
                <div>
                  <dt>Lu par</dt>
                  <dd className="mono">
                    {version.provenance.producer}
                    {version.provenance.producer_version && ` ${version.provenance.producer_version}`}
                  </dd>
                </div>
              )}
            </dl>
          </section>
          <section className="panel panel-pad record-versions" aria-labelledby="versions-title">
            <h3 id="versions-title">Versions</h3>
            <ol>
              {versions.map((v, i) => (
                <li key={v.version_id}>
                  <button
                    type="button"
                    className="version-pick"
                    aria-pressed={v.version_id === shown}
                    onClick={() => setShown(v.version_id)}
                  >
                    <span>{i === 0 && v.version_id === current?.version_id ? "Version actuelle" : `Version ${versions.length - i}`}</span>
                    {v.accepted_at && (
                      <time dateTime={v.accepted_at}>{formatAbsolute(v.accepted_at)}</time>
                    )}
                  </button>
                </li>
              ))}
            </ol>
            <p className="facets-note">
              Les versions que la démo a lues de ce document, la plus récente en premier.
            </p>
          </section>
        </div>
      </div>
      {before && (
        <section className="panel panel-pad record-changes reader-diff" aria-labelledby="changes-title">
          <h3 id="changes-title">
            Changements depuis la version
            {before.accepted_at ? ` du ${formatAbsolute(before.accepted_at)}` : " précédente"}
          </h3>
          {changes ? (
            <>
              <p className="record-changes-summary">{summarize(changes)}</p>
              <p className="reader-diff-text" aria-label="Texte avec les changements">
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
            </>
          ) : (
            <LoadingState label="Comparaison des versions…" rows={2} />
          )}
        </section>
      )}
      {raw && (
        <details className="panel record-raw">
          <summary>Source brute</summary>
          <div className="record-raw-body">
            {blobs.length > 0 && (
              <ul className="record-blobs" aria-label="Fichiers sources">
                {blobs.map((b) => (
                  <li key={b.blob_id}>
                    <span className="mono">{b.media_type}</span> · {sizeLabel(b.size_bytes)} ·{" "}
                    <span className="mono" title="Empreinte SHA-256">
                      {b.sha256.slice(0, 12)}…
                    </span>
                  </li>
                ))}
              </ul>
            )}
            <p className="facets-note">
              Ce que Quivr garde de la source : ses extensions, sa provenance et ses parties
              d’origine. Quivr ne sert pas encore les octets des fichiers sources.
            </p>
            <pre>{JSON.stringify(raw, null, 2)}</pre>
          </div>
        </details>
      )}
    </article>
  );
}
