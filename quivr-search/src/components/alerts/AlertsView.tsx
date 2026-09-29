import { useCallback, useEffect, useRef, useState } from "react";
import { ArrowClockwise, Bell, CaretRight } from "@phosphor-icons/react";
import { APIError } from "../../lib/search";
import {
  alertMessage,
  fetchAlert,
  fetchAlerts,
  followMonitoring,
  type Alert,
  type AlertDetail as Detail,
} from "../../lib/alerts";
import { AlertComposer } from "./AlertComposer";
import { AlertDetail, StateBadge, queryText } from "./AlertDetail";
import "../../alerts.css";

type Status = "loading" | "ready" | "unavailable" | "error";

const articles = (n: number, capped = false) =>
  n === 0
    ? "Aucun article"
    : `${n}${capped ? "+" : ""} article${n === 1 ? "" : "s"}`;

/**
 * The Alertes tab: write a keyword alert, see every alert with what it caught,
 * and open one to read its articles. Matches arrive live: the change feed's
 * match.* and subscription.* events trigger a reread.
 */
export function AlertsView({
  selected,
  onSelect,
  onOpen,
  onUnauthorized,
}: {
  selected: string | null;
  onSelect: (id: string | null) => void;
  onOpen: (record: string, version: string) => void;
  onUnauthorized: () => void;
}) {
  const [status, setStatus] = useState<Status>("loading");
  const [error, setError] = useState("");
  const [alerts, setAlerts] = useState<Alert[]>([]);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [detailError, setDetailError] = useState("");
  const [live, setLive] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [grown, setGrown] = useState<Set<string>>(new Set());
  const [fresh, setFresh] = useState<Set<string>>(new Set());
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
    if (up.size) {
      setGrown(up);
      const names = list.items
        .filter((a) => up.has(a.alert_id))
        .map((a) => a.name);
      setAnnouncement(`Nouvel article pour ${names.join(", ")}.`);
    }
    setAlerts(list.items);
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

  // Live: drain the corpus change feed from now on; a match.*, subscription.*
  // or saved_query.* event rereads the list and the open alert. Without
  // change-feed access, reread on a slower timer instead.
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
  };

  return (
    <main className="connectors-page alerts-page">
      {!selected && (
        <div className="connectors-head">
          <div>
            <h1>Alertes</h1>
            <p className="muted">
              Soyez prévenu dès qu’un article correspond à vos mots-clés, qu’il
              vienne d’un flux RSS ou d’un texte ajouté à la main.
            </p>
          </div>
        </div>
      )}
      <p className="visually-hidden" role="status" aria-live="polite">
        {announcement}
      </p>
      {status === "loading" && (
        <p role="status" className="muted">
          Chargement des alertes…
        </p>
      )}
      {status === "unavailable" && (
        <div className="notice" role="status">
          <h2>Les alertes ne sont pas activées sur ce déploiement.</h2>
          <p>
            L’administrateur peut les activer côté serveur. La recherche, les
            sources et l’ajout de textes restent disponibles.
          </p>
        </div>
      )}
      {status === "error" && (
        <div className="notice" role="alert">
          <h2>Les alertes n’ont pas pu être chargées.</h2>
          <p>{error}</p>
          <button className="button" onClick={() => setAttempt((n) => n + 1)}>
            <ArrowClockwise size={16} aria-hidden="true" /> Réessayer
          </button>
        </div>
      )}
      {status === "ready" && selected && (
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
            setAnnouncement(message);
            void reloadDetail(alert.alert_id);
          }}
          onDeleted={(alert) => {
            setAlerts((list) =>
              list.filter((a) => a.alert_id !== alert.alert_id),
            );
            setAnnouncement(`${alert.name} supprimée.`);
            onSelect(null);
          }}
        />
      )}
      {status === "ready" && !selected && (
        <>
          <AlertComposer
            onUnauthorized={onUnauthorized}
            onCreated={(alert) => {
              upsert(alert);
              setAnnouncement(`Alerte « ${alert.name} » créée.`);
              setGrown(new Set([alert.alert_id]));
            }}
          />
          <div className="sources-head">
            <h2 ref={listHeading} tabIndex={-1}>
              Vos alertes
              {alerts.length > 0 && (
                <span className="count"> {alerts.length}</span>
              )}
            </h2>
            <span className="live-line muted">
              {live ? (
                <>
                  <span className="status-dot" aria-hidden="true" /> Suivies en
                  direct
                </>
              ) : (
                "Actualisées régulièrement"
              )}
            </span>
          </div>
          {alerts.length === 0 ? (
            <div className="empty sources-empty">
              <Bell size={30} aria-hidden="true" />
              <h2>Aucune alerte pour l’instant.</h2>
              <p>
                Écrivez des mots-clés ci-dessus : chaque nouvel article qui
                correspond s’affichera ici.
              </p>
            </div>
          ) : (
            <ul className="alert-list" aria-label="Alertes">
              {alerts.map((alert) => (
                <li
                  key={alert.alert_id}
                  className="alert-row"
                  data-highlight={grown.has(alert.alert_id) || undefined}
                  data-enabled={alert.enabled}
                >
                  <button
                    type="button"
                    className="alert-open"
                    onClick={() => onSelect(alert.alert_id)}
                  >
                    <span className="alert-row-main">
                      <span className="alert-row-name">{alert.name}</span>
                      <code className="alert-query">{queryText(alert)}</code>
                    </span>
                    <span className="alert-row-side">
                      <StateBadge enabled={alert.enabled} />
                      <span className="alert-count" data-testid="alert-count">
                        {articles(alert.match_count, alert.capped)}
                      </span>
                    </span>
                    <CaretRight
                      size={18}
                      className="alert-caret"
                      aria-hidden="true"
                    />
                  </button>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </main>
  );
}
