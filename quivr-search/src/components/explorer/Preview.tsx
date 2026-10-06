import { useEffect, useState } from "react";
import { ArrowSquareOut } from "@phosphor-icons/react";
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
import { LoadingState, Notice } from "../ui";
import { dayLabel, timeLabel } from "./WireList";

// The history shows this many Versions, the newest.
const MAX_VERSIONS = 10;

/**
 * The preview panel (THE-1204): the selected document read without leaving
 * the list. Its language, date and whether it was corrected; its headline
 * and lede; a few fields; its Versions, each with what changed from the one
 * before; and what Quivr keeps of its source, on demand.
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

  useEffect(() => {
    const controller = new AbortController();
    setDetail(null);
    setError("");
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

  const versions = (detail?.versions || []).slice(0, MAX_VERSIONS);
  const wanted = versions.map((v) => v.version_id).filter((v) => !read.has(v)).join(",");
  useEffect(() => {
    if (!wanted) return;
    const controller = new AbortController();
    for (const version of wanted.split(","))
      fetchVersion(id, version, controller.signal)
        .then((v) => setRead((known) => new Map(known).set(v.version_id, v)))
        .catch((e) => {
          if (controller.signal.aborted) return;
          if (e instanceof APIError && e.status === 401) onUnauthorized();
        });
    return () => controller.abort();
  }, [id, wanted, onUnauthorized]);

  if (error)
    return (
      <Notice title="Ce document ne s’affiche pas." onRetry={() => setAttempt((n) => n + 1)}>
        {error}
      </Notice>
    );
  if (!detail) return <LoadingState label="Chargement de l’aperçu…" rows={3} />;

  const { record, corpus, blobs } = detail;
  const version = detail.version;
  if (!version)
    return <p className="facets-note">Ce document n’a plus de version lisible.</p>;
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
  const rows = [
    ["Lieu", values("metadata.place").map((v) => valueLabel(v)).join(", ")],
    ["Sujets", values("metadata.subjects").map((v) => valueLabel(v)).join(", ")],
    ["Mots", countLabel(wordCount(version))],
  ].filter(([, v]) => v);
  const xml = blobs.some((b) => /xml/i.test(b.media_type));
  const sourceParts = version.manifest.parts.filter((p) => p.role === "source_html" || p.content.kind === "blob");
  const raw = {
    extensions: version.extensions || {},
    provenance: version.provenance || {},
    ...(sourceParts.length ? { source_parts: sourceParts } : {}),
  };

  return (
    <article className="preview" aria-labelledby="preview-title">
      <p className="preview-meta">
        {language && <span className="wire-lang">{language.slice(0, 3).toUpperCase()}</span>}
        {at && (
          <time dateTime={at}>
            {dayLabel(at)} · {timeLabel(at)}
          </time>
        )}
        {corrected && <span className="preview-corrected">corrigée</span>}
        {record.withdrawn && <span className="record-withdrawn">Retiré</span>}
      </p>
      <h2 id="preview-title">{headline}</h2>
      {lede && <p className="preview-lede">{lede}</p>}
      {rows.length > 0 && (
        <dl className="preview-fields">
          {rows.map(([label, value]) => (
            <div key={label}>
              <dt>{label}</dt>
              <dd>{value}</dd>
            </div>
          ))}
        </dl>
      )}
      <section className="preview-versions" aria-labelledby="preview-versions-title">
        <h3 id="preview-versions-title">Versions</h3>
        <ol>
          {versions.map((v, i) => {
            const before = versions[i + 1];
            const mine = read.get(v.version_id);
            const theirs = before && read.get(before.version_id);
            const n = detail.versions.length - i;
            return (
              <li key={v.version_id} data-current={i === 0 || undefined}>
                <span className="preview-version-n">v{n}</span>
                {v.accepted_at && <time dateTime={v.accepted_at}>{dayLabel(v.accepted_at)} · {timeLabel(v.accepted_at)}</time>}
                <span className="preview-version-change">
                  {!before && n === 1
                    ? "première version"
                    : !before
                      ? "versions plus anciennes non lues"
                      : mine && theirs
                        ? changeSummary(theirs, mine)
                        : "…"}
                </span>
              </li>
            );
          })}
        </ol>
      </section>
      <div className="preview-actions">
        <button type="button" className="button primary" onClick={onOpen}>
          Ouvrir <ArrowSquareOut size={15} aria-hidden="true" />
        </button>
      </div>
      <details className="preview-source">
        <summary>{xml ? "Source XML" : "Source brute"}</summary>
        <p className="facets-note">
          Ce que Quivr garde de la source : ses extensions, sa provenance et ses parties d’origine.
        </p>
        <pre>{JSON.stringify(raw, null, 2)}</pre>
      </details>
    </article>
  );
}
