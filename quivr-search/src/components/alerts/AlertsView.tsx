import { useCallback, useEffect, useRef, useState } from "react";
import { APIError } from "../../lib/search";
import {
  alertMessage,
  deleteAlert,
  fetchAlert,
  fetchAlerts,
  followMonitoring,
  pauseAlert,
  resumeAlert,
  type Alert,
  type AlertDetail as Detail,
} from "../../lib/alerts";
import { HAND_NAMESPACE, type FeedItem } from "../../lib/feed";
import type { Connector } from "../../lib/connectors";
import { groupSources } from "../connectors/SourceList";
import { alertRule } from "../feed/SideColumn";
import { AlertForm } from "./AlertForm";
import { AlertDetail } from "./AlertDetail";
import { LiveBadge, LoadingState, Notice } from "../ui";

type Status = "loading" | "ready" | "unavailable" | "error";

const articles = (n: number, capped = false) =>
  n === 0
    ? "Aucun article"
    : `${n}${capped ? "+" : ""} article${n === 1 ? "" : "s"}`;

/**
 * The Alertes page: the form on the left writes or edits an alert; the list
 * on the right shows each alert, what it caught, and pauses, edits or
 * deletes it. Opening one lists its articles. Matches arrive live: the change
 * feed's match.* and subscription.* events trigger a reread.
 */
export function AlertsView({
  selected,
  onSelect,
  onOpen,
  connectors,
  feedItems,
  onChanged,
  notify,
  onUnauthorized,
}: {
  selected: string | null;
  onSelect: (id: string | null) => void;
  onOpen: (record: string, version: string) => void;
  connectors: Connector[];
  feedItems: FeedItem[];
  onChanged: () => void;
  notify: (text: string) => void;
  onUnauthorized: () => void;
}) {
  const [status, setStatus] = useState<Status>("loading");
  const [error, setError] = useState("");
  const [alerts, setAlerts] = useState<Alert[]>([]);
  const [described, setDescribed] = useState(false);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [detailError, setDetailError] = useState("");
  const [live, setLive] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [grown, setGrown] = useState<Set<string>>(new Set());
  const [fresh, setFresh] = useState<Set<string>>(new Set());
  const [editing, setEditing] = useState<Alert | null>(null);
  const counts = useRef(new Map<string, number>());
  const seen = useRef<{ id: string; matches: Set<string> } | null>(null);
  const listHeading = useRef<HTMLHeadingElement>(null);

  const failed = useCallback(
    (e: unknown) => {
      if (e instanceof APIError && e.status === 401) onUnauthorized();
      else {
        setError(alertMessage(e));
        setStatus("error");
      }
    },
    [onUnauthorized],
  );

  const reload = useCallback(async (signal?: AbortSignal) => {
    const list = await fetchAlerts(signal);
    if (!list.available) {
      setStatus("unavailable");
      return;
    }
    const up = new Set<string>();
    for (const a of list.items) {
      const before = counts.current.get(a.alert_id);
      if (before !== undefined && a.match_count > before) up.add(a.alert_id);
      counts.current.set(a.alert_id, a.match_count);
    }
    if (up.size) setGrown(up);
    setAlerts(list.items);
    setDescribed(list.described === true);
    setStatus("ready");
  }, []);

  const reloadDetail = useCallback(
    async (id: string, signal?: AbortSignal) => {
      try {
        const next = await fetchAlert(id, signal);
        const ids = new Set(next.matches.map((m) => m.match_id));
        if (seen.current?.id === id) {
          const added = [...ids].filter((m) => !seen.current!.matches.has(m));
          if (added.length) setFresh((f) => new Set([...f, ...added]));
        } else setFresh(new Set());
        seen.current = { id, matches: ids };
        setDetail(next);
        setDetailError("");
      } catch (e) {
        if (signal?.aborted) return;
        if (e instanceof APIError && e.status === 401) onUnauthorized();
        else if (e instanceof APIError && e.status === 404) onSelect(null);
        else setDetailError(alertMessage(e));
      }
    },
    [onSelect, onUnauthorized],
  );

  useEffect(() => {
    const controller = new AbortController();
    setStatus("loading");
    reload(controller.signal).catch((e) => {
      if (!controller.signal.aborted) failed(e);
    });
    return () => controller.abort();
  }, [reload, failed, attempt]);

  useEffect(() => {
    setDetail(null);
    setDetailError("");
    if (!selected) return;
    const controller = new AbortController();
    void reloadDetail(selected, controller.signal);
    return () => controller.abort();
  }, [selected, reloadDetail]);

  useEffect(() => {
    if (status !== "ready") return;
    return followMonitoring({
      onChange: async (signal) => {
        await reload(signal);
        if (selected) await reloadDetail(selected, signal);
      },
      onLive: setLive,
      onUnauthorized,
    });
  }, [status, selected, reload, reloadDetail, onUnauthorized]);

  const upsert = (alert: Alert) => {
    counts.current.set(alert.alert_id, alert.match_count);
    setAlerts((list) =>
      list.some((a) => a.alert_id === alert.alert_id)
        ? list.map((a) => (a.alert_id === alert.alert_id ? alert : a))
        : [alert, ...list],
    );
    onChanged();
  };
  const removed = (alert: Alert) => {
    setAlerts((list) => list.filter((a) => a.alert_id !== alert.alert_id));
    if (editing?.alert_id === alert.alert_id) setEditing(null);
    notify(`Alerte « ${alert.name} » supprimée.`);
    onChanged();
    listHeading.current?.focus();
  };

  // What an alert can watch: every source of the demo, and texts added by hand.
  const sources = [
    ...new Set([
      ...groupSources(connectors).map((c) => c.source_namespace),
      ...feedItems.map((i) => i.namespace).filter(Boolean),
      HAND_NAMESPACE,
    ]),
  ];

  if (status !== "ready")
    return (
      <main className="board board-single">
        <h1 className="visually-hidden">Alertes</h1>
        <section className="panel panel-pad">
          {status === "loading" && (
            <LoadingState label="Chargement des alertes…" />
          )}
          {status === "unavailable" && (
            <Notice
              tone="info"
              title="Les alertes ne sont pas activées sur ce déploiement."
            >
              L’administrateur peut les activer côté serveur. Le fil, la
              recherche, les sources et l’ajout de textes restent disponibles.
            </Notice>
          )}
          {status === "error" && (
            <Notice
              title="Les alertes n’ont pas pu être chargées."
              onRetry={() => setAttempt((n) => n + 1)}
            >
              {error}
            </Notice>
          )}
        </section>
      </main>
    );

  return (
    <main className="board board-split">
      <h1 className="visually-hidden">Alertes</h1>
      <section className="panel form-panel">
        <AlertForm
          described={described}
          editing={editing}
          sources={sources}
          onUnauthorized={onUnauthorized}
          onCancel={() => setEditing(null)}
          onSaved={(alert, created) => {
            upsert(alert);
            setEditing(null);
            setGrown(new Set([alert.alert_id]));
            if (selected === alert.alert_id) void reloadDetail(alert.alert_id);
            notify(
              created
                ? `C’est noté : vous serez prévenu dès qu’un nouvel article correspondra à « ${alert.name} ».`
                : `Alerte « ${alert.name} » mise à jour.`,
            );
          }}
        />
      </section>
      <section className="panel list-panel" aria-labelledby="alerts-list-title">
        {selected ? (
          <div className="panel-pad">
            <AlertDetail
              detail={detail?.alert_id === selected ? detail : null}
              error={detailError}
              fresh={fresh}
              onBack={() => onSelect(null)}
              onRetry={() => void reloadDetail(selected)}
              onOpen={onOpen}
              onUnauthorized={onUnauthorized}
              onChanged={(alert, message) => {
                upsert(alert);
                setDetail((d) => d && { ...d, ...alert });
                notify(message);
                void reloadDetail(alert.alert_id);
              }}
              onDeleted={(alert) => {
                removed(alert);
                onSelect(null);
              }}
            />
          </div>
        ) : (
          <>
            <div className="panel-head list-head">
              <h2 id="alerts-list-title" ref={listHeading} tabIndex={-1}>
                Vos alertes
              </h2>
              <span className="list-count">
                {alerts.length} alerte{alerts.length > 1 ? "s" : ""}
              </span>
              <LiveBadge live={live} />
            </div>
            {alerts.length === 0 ? (
              <p className="list-empty">
                Aucune alerte pour l’instant. Créez la première avec le
                formulaire.
              </p>
            ) : (
              <ul className="alert-items" aria-label="Alertes">
                {alerts.map((alert) => (
                  <AlertRow
                    key={alert.alert_id}
                    alert={alert}
                    highlight={grown.has(alert.alert_id)}
                    editing={editing?.alert_id === alert.alert_id}
                    onOpen={() => onSelect(alert.alert_id)}
                    onEdit={() => setEditing(alert)}
                    onChanged={(next, message) => {
                      upsert(next);
                      notify(message);
                    }}
                    onDeleted={() => removed(alert)}
                    onUnauthorized={onUnauthorized}
                  />
                ))}
              </ul>
            )}
          </>
        )}
      </section>
    </main>
  );
}

function AlertRow({
  alert,
  highlight,
  editing,
  onOpen,
  onEdit,
  onChanged,
  onDeleted,
  onUnauthorized,
}: {
  alert: Alert;
  highlight: boolean;
  editing: boolean;
  onOpen: () => void;
  onEdit: () => void;
  onChanged: (alert: Alert, message: string) => void;
  onDeleted: () => void;
  onUnauthorized: () => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const confirmRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (confirming) confirmRef.current?.focus();
  }, [confirming]);
  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (e) {
      if (e instanceof APIError && e.status === 401) onUnauthorized();
      setError(alertMessage(e, alert.kind));
    } finally {
      setBusy(false);
    }
  };
  return (
    <li
      className="alert-item"
      data-enabled={alert.enabled}
      data-highlight={highlight || undefined}
      data-editing={editing || undefined}
    >
      <div className="alert-item-head">
        <span
          className="diamond"
          data-tone={
            !alert.enabled ? "off" : alert.match_count ? "hot" : "idle"
          }
          aria-hidden="true"
        />
        <h3 className="alert-item-name">
          <button type="button" className="alert-open" onClick={onOpen}>
            {alert.name}
          </button>
        </h3>
        <span className="alert-state" data-enabled={alert.enabled}>
          {alert.enabled ? "Active" : "En pause"}
        </span>
      </div>
      <p className="alert-item-rule">{alertRule(alert, false)}</p>
      <p className="alert-item-stats">
        <span data-testid="alert-count">
          {articles(alert.match_count, alert.capped)}
        </span>{" "}
        attrapé{alert.match_count > 1 ? "s" : ""}
        {alert.kind === "described" && " · décrite en une phrase"}
      </p>
      {confirming ? (
        <div
          className="row-actions"
          role="group"
          aria-label={`Supprimer ${alert.name}`}
        >
          <span className="row-confirm">Supprimer cette alerte ?</span>
          <button
            ref={confirmRef}
            type="button"
            className="button danger small"
            disabled={busy}
            onClick={() =>
              void run(async () => {
                await deleteAlert(alert.alert_id);
                onDeleted();
              })
            }
          >
            Oui, supprimer
          </button>
          <button
            type="button"
            className="button small"
            onClick={() => setConfirming(false)}
          >
            Non
          </button>
        </div>
      ) : (
        <div
          className="row-actions"
          role="group"
          aria-label={`Actions de ${alert.name}`}
        >
          <button
            type="button"
            className="button small"
            disabled={busy}
            onClick={() =>
              void run(async () => {
                const next = await (alert.enabled
                  ? pauseAlert(alert.alert_id)
                  : resumeAlert(alert.alert_id));
                onChanged(
                  next,
                  next.enabled
                    ? `« ${next.name} » surveille de nouveau les arrivées.`
                    : `« ${next.name} » est en pause : rien ne sera marqué jusqu’à la reprise.`,
                );
              })
            }
          >
            {alert.enabled ? "Mettre en pause" : "Reprendre"}
          </button>
          {(alert.kind === "keywords" || alert.kind === "described") && (
            <button
              type="button"
              className="button small"
              disabled={busy}
              onClick={onEdit}
            >
              Modifier
            </button>
          )}
          <button type="button" className="button small" onClick={onOpen}>
            Voir les articles
          </button>
          <button
            type="button"
            className="button small quiet"
            disabled={busy}
            onClick={() => setConfirming(true)}
          >
            Supprimer
          </button>
        </div>
      )}
      {error && (
        <p className="error-text" role="alert">
          {error}
        </p>
      )}
    </li>
  );
}
