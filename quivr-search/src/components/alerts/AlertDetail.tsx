import { useEffect, useRef, useState } from "react";
import {
  ArrowLeft,
  ArrowClockwise,
  BellSlash,
  Pause,
  PencilSimple,
  Play,
  Trash,
} from "@phosphor-icons/react";
import { APIError } from "../../lib/search";
import { print } from "../../lib/notation";
import {
  alertMessage,
  deleteAlert,
  editAlert,
  pauseAlert,
  resumeAlert,
  type Alert,
  type AlertDetail as Detail,
} from "../../lib/alerts";
import { Interpretation } from "./Interpretation";
import { QueryPreview, useParsed } from "./QueryPreview";
import { CaughtItem } from "./CaughtItem";

export function StateBadge({ enabled }: { enabled: boolean }) {
  return (
    <span className="health-badge" data-tone={enabled ? "ok" : undefined}>
      {enabled ? (
        <span className="status-dot" aria-hidden="true" />
      ) : (
        <Pause size={12} weight="fill" aria-hidden="true" />
      )}
      {enabled ? "Active" : "En pause"}
    </span>
  );
}

export const queryText = (alert: Alert) =>
  alert.kind === "keywords"
    ? (print(alert.expression.match) ?? "Requête avancée")
    : "Alerte d’un autre type";

export function AlertDetail({
  detail,
  error,
  fresh,
  onBack,
  onChanged,
  onDeleted,
  onRetry,
  onOpen,
  onUnauthorized,
}: {
  detail: Detail | null;
  error: string;
  fresh: Set<string>;
  onBack: () => void;
  onChanged: (alert: Alert, message: string) => void;
  onDeleted: (alert: Alert) => void;
  onRetry: () => void;
  onOpen: (record: string, version: string) => void;
  onUnauthorized: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState("");
  const heading = useRef<HTMLHeadingElement>(null);
  const parsed = useParsed(draft);

  useEffect(() => {
    heading.current?.focus();
  }, [detail?.alert_id]);

  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    setActionError("");
    try {
      await action();
    } catch (e) {
      if (e instanceof APIError && e.status === 401) onUnauthorized();
      setActionError(alertMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const back = (
    <button type="button" className="back-button text-button" onClick={onBack}>
      <ArrowLeft size={16} aria-hidden="true" /> Toutes les alertes
    </button>
  );
  if (!detail)
    return (
      <section className="alert-detail">
        {back}
        {error ? (
          <div className="notice" role="alert">
            <h2>L’alerte n’a pas pu être chargée.</h2>
            <p>{error}</p>
            <button className="button" onClick={onRetry}>
              <ArrowClockwise size={16} aria-hidden="true" /> Réessayer
            </button>
          </div>
        ) : (
          <p role="status" className="muted">
            Chargement de l’alerte…
          </p>
        )}
      </section>
    );

  const text = queryText(detail);
  return (
    <section className="alert-detail" aria-labelledby="alert-title">
      {back}
      <div className="alert-detail-head">
        <div>
          <h1 id="alert-title" ref={heading} tabIndex={-1}>
            {detail.name}
          </h1>
          <code className="alert-query">{text}</code>
        </div>
        <StateBadge enabled={detail.enabled} />
      </div>
      {detail.kind === "keywords" && !editing && (
        <p className="query-preview" data-state="valid">
          <span>
            <span className="muted">Articles avec </span>
            <Interpretation node={detail.expression.match} />
          </span>
        </p>
      )}
      {editing ? (
        <form
          className="alert-edit"
          aria-label="Modifier la requête"
          noValidate
          onSubmit={(event) => {
            event.preventDefault();
            if (busy || parsed.state !== "valid") return;
            void run(async () => {
              const next = await editAlert(detail.alert_id, parsed.expression);
              setEditing(false);
              onChanged(next, `${next.name} : requête modifiée.`);
            });
          }}
        >
          <label className="field-label" htmlFor="alert-edit-query">
            Nouvelle requête
          </label>
          <div className="source-field alert-field">
            <input
              id="alert-edit-query"
              className="source-input"
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              autoComplete="off"
              spellCheck={false}
              aria-describedby="alert-edit-preview alert-edit-note"
              autoFocus
            />
          </div>
          <QueryPreview id="alert-edit-preview" parsed={parsed} />
          <p id="alert-edit-note" className="field-help">
            Les articles déjà trouvés restent. La nouvelle requête s’applique
            aux articles qui arrivent ensuite.
          </p>
          <div className="alert-actions">
            <button
              className="button primary small"
              disabled={busy || parsed.state !== "valid"}
            >
              Enregistrer
            </button>
            <button
              type="button"
              className="button small"
              onClick={() => setEditing(false)}
            >
              Annuler
            </button>
          </div>
        </form>
      ) : confirming ? (
        <div
          className="remove-confirm alert-actions"
          role="group"
          aria-label="Confirmer la suppression"
        >
          <p>
            Supprimer « {detail.name} » ? Elle ne préviendra plus de rien et ses
            résultats ne seront plus affichés.
          </p>
          <button
            type="button"
            className="button danger small"
            disabled={busy}
            onClick={() =>
              void run(async () => {
                await deleteAlert(detail.alert_id);
                onDeleted(detail);
              })
            }
          >
            <Trash size={15} aria-hidden="true" /> Supprimer définitivement
          </button>
          <button
            type="button"
            className="button small"
            onClick={() => setConfirming(false)}
          >
            Annuler
          </button>
        </div>
      ) : (
        <div
          className="alert-actions"
          role="group"
          aria-label="Actions de l’alerte"
        >
          <button
            type="button"
            className="button small"
            disabled={busy}
            onClick={() =>
              void run(async () => {
                const next = await (detail.enabled
                  ? pauseAlert(detail.alert_id)
                  : resumeAlert(detail.alert_id));
                onChanged(
                  next,
                  `${next.name} : ${next.enabled ? "alerte reprise" : "en pause"}.`,
                );
              })
            }
          >
            {detail.enabled ? (
              <Pause size={15} aria-hidden="true" />
            ) : (
              <Play size={15} aria-hidden="true" />
            )}
            {detail.enabled ? "Mettre en pause" : "Reprendre"}
          </button>
          {detail.kind === "keywords" && (
            <button
              type="button"
              className="button small"
              disabled={busy}
              onClick={() => {
                setDraft(text);
                setEditing(true);
              }}
            >
              <PencilSimple size={15} aria-hidden="true" /> Modifier
            </button>
          )}
          <button
            type="button"
            className="button small quiet"
            disabled={busy}
            onClick={() => setConfirming(true)}
          >
            <Trash size={15} aria-hidden="true" /> Supprimer
          </button>
        </div>
      )}
      {actionError && (
        <p className="error-text" role="alert">
          {actionError}
        </p>
      )}
      <div className="sources-head">
        <h2>
          Articles trouvés
          <span className="count">
            {" "}
            {detail.match_count}
            {detail.capped ? "+" : ""}
          </span>
        </h2>
        {!detail.enabled && (
          <span className="live-line muted">
            <BellSlash size={14} aria-hidden="true" /> En pause : les nouveaux
            articles ne sont pas examinés
          </span>
        )}
      </div>
      {detail.matches.length === 0 ? (
        <div className="empty sources-empty">
          <h2>Rien pour l’instant.</h2>
          <p>
            Les articles qui arrivent à partir de maintenant et correspondent à
            la requête s’afficheront ici, en direct.
          </p>
        </div>
      ) : (
        <ul className="caught-list" aria-label="Articles trouvés">
          {detail.matches.map((article) => (
            <CaughtItem
              key={article.match_id}
              article={article}
              fresh={fresh.has(article.match_id)}
              onOpen={() => onOpen(article.record_id, article.version_id)}
            />
          ))}
        </ul>
      )}
      {detail.matches.length < detail.match_count && (
        <p className="field-help">
          Les {detail.matches.length} plus récents sont affichés.
        </p>
      )}
    </section>
  );
}
