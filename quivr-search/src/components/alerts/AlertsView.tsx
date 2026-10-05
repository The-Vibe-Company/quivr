import {
  Fragment,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import type { Doc } from "../../App";
import { APIError, tokenize } from "../../lib/search";
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
import { decompose, sourceName } from "../../lib/alertForm";
import {
  alertStats,
  arrivedAt,
  busiestPeriod,
  streakStart,
  type AlertStats,
} from "../../lib/alertStats";
import { HAND_NAMESPACE, type FeedItem } from "../../lib/feed";
import type { Connector } from "../../lib/connectors";
import { longTime, plural } from "../../lib/format";
import { daily, dayLabel } from "../../lib/moments";
import { print } from "../../lib/notation";
import { groupSources } from "../connectors/SourceList";
import { Reader } from "../feed/Reader";
import { SourceLogo, logoIds } from "../feed/SourceLogo";
import {
  ActivityIcon,
  AlertsIcon,
  ChecksIcon,
  ChevronsRightIcon,
  DescribedIcon,
  KeywordsIcon,
  PencilIcon,
  PlusIcon,
  TrendDownIcon,
  TrendUpIcon,
} from "../RailIcons";
import { MoreMenu } from "../MoreMenu";
import { AlertForm } from "./AlertForm";
import { CaughtItem } from "./CaughtItem";
import { EmptyState, LiveBadge, LoadingState, Notice } from "../ui";

type Status = "loading" | "ready" | "unavailable" | "error";

const dayMonth = new Intl.DateTimeFormat("fr-FR", { day: "numeric", month: "short" });
const decimal = new Intl.NumberFormat("fr-FR", { maximumFractionDigits: 1 });

const KINDS: Record<string, string> = { keywords: "Mots précis", described: "Sujet décrit" };

/**
 * The sources an alert watches: some, null for every source, or undefined
 * when an advanced query says it.
 */
function watched(alert: Alert): string[] | null | undefined {
  if (alert.kind === "keywords") {
    const form = decompose(alert.expression.match);
    return form ? (form.sources.length ? form.sources : null) : undefined;
  }
  if (alert.kind === "described" && alert.expression.sources?.length) return alert.expression.sources;
  return null;
}

/** The logos of the sources an alert watches, the first four then "+N". */
function SourceLogos({
  alert,
  every,
  logoOf,
}: {
  alert: Alert;
  /** Every source, for an alert that watches them all. */
  every: string[];
  logoOf: Map<string, string>;
}) {
  const only = watched(alert);
  if (only === undefined) return <span className="muted">Selon la requête</span>;
  const list = only || every;
  const label = only ? only.map(sourceName).join(", ") : "Toutes les sources";
  const shown = list.length > 4 ? list.slice(0, 3) : list;
  return (
    <span className="source-logos" role="img" aria-label={label} title={label}>
      {shown.map((ns) => (
        <SourceLogo key={ns} namespace={ns} connectorId={logoOf.get(ns)} size="small" />
      ))}
      {list.length > shown.length && <span className="source-more">+{list.length - shown.length}</span>}
    </span>
  );
}

/**
 * The Alertes page: every alert in a table (what it caught, the last seven
 * days, when it was created, its state), and under it the selected alert:
 * its analytics on the left (growth, frequency, trend, sources, hours) and
 * the articles it caught on the right. Writing or editing an alert happens in
 * a panel over the right of the page. Matches arrive live: the change feed's
 * match.* and subscription.* events trigger a reread.
 */
export function AlertsView({
  corpus,
  selected,
  onSelect,
  doc,
  onOpen,
  onCloseDoc,
  onSimilar,
  connectors,
  feedItems,
  isUnread,
  onMarkAllRead,
  onChanged,
  notify,
  onUnauthorized,
}: {
  corpus: string;
  selected: string | null;
  onSelect: (id: string | null) => void;
  /** The article open in the reader over the page. */
  doc: Doc | null;
  onOpen: (record: string, version: string) => void;
  onCloseDoc: () => void;
  /** Searches the feed for articles on the same subject. */
  onSimilar: (text: string) => void;
  connectors: Connector[];
  feedItems: FeedItem[];
  isUnread: (item: FeedItem) => boolean;
  onMarkAllRead: (items: FeedItem[]) => void;
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
  // The form panel: closed, writing a new alert, or editing `editing`.
  const [composing, setComposing] = useState(false);
  const [matched, setMatched] = useState<Record<string, string[]>>({});
  const [now, setNow] = useState(() => Date.now());
  const counts = useRef(new Map<string, number>());
  const seen = useRef<{ id: string; matches: Set<string> } | null>(null);
  const listHeading = useRef<HTMLHeadingElement>(null);

  // The alert shown under the table: the one picked, else the first.
  const current = alerts.find((a) => a.alert_id === selected) || alerts[0] || null;
  const currentID = current?.alert_id ?? null;

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
    setMatched(list.matched || {});
    setNow(Date.now());
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
    setDetailError("");
    if (!currentID) return;
    const controller = new AbortController();
    void reloadDetail(currentID, controller.signal);
    return () => controller.abort();
  }, [currentID, reloadDetail]);

  useEffect(() => {
    if (status !== "ready") return;
    return followMonitoring({
      onChange: async (signal) => {
        await reload(signal);
        if (currentID) await reloadDetail(currentID, signal);
      },
      onLive: setLive,
      onUnauthorized,
    });
  }, [status, currentID, reload, reloadDetail, onUnauthorized]);

  const upsert = (alert: Alert) => {
    counts.current.set(alert.alert_id, alert.match_count);
    setAlerts((list) =>
      list.some((a) => a.alert_id === alert.alert_id)
        ? list.map((a) => (a.alert_id === alert.alert_id ? alert : a))
        : [alert, ...list],
    );
    onChanged();
  };
  const compose = (alert: Alert | null) => {
    onCloseDoc();
    setEditing(alert);
    setComposing(true);
  };
  const closeForm = useCallback(() => {
    setComposing(false);
    setEditing(null);
  }, []);
  const removed = (alert: Alert) => {
    setAlerts((list) => list.filter((a) => a.alert_id !== alert.alert_id));
    if (editing?.alert_id === alert.alert_id) closeForm();
    if (selected === alert.alert_id) onSelect(null);
    notify(`Alerte « ${alert.name} » supprimée.`);
    onChanged();
    listHeading.current?.focus();
  };

  // What each alert caught among the feed's articles, newest first.
  const caught = useMemo(() => {
    const out = new Map<string, FeedItem[]>();
    for (const item of [...feedItems].sort((a, b) => arrivedAt(b).localeCompare(arrivedAt(a))))
      for (const id of matched[item.record_id] || []) {
        const list = out.get(id);
        if (list) list.push(item);
        else out.set(id, [item]);
      }
    return out;
  }, [feedItems, matched]);
  const byRecord = useMemo(() => new Map(feedItems.map((i) => [i.record_id, i])), [feedItems]);
  const logoOf = useMemo(() => logoIds(connectors), [connectors]);
  const stats = useMemo(
    () =>
      current &&
      alertStats({
        createdAt: current.created_at,
        caught: caught.get(current.alert_id) || [],
        feed: feedItems,
        now,
        total: current.match_count,
      }),
    [current, caught, feedItems, now],
  );

  // Relative times and windows move on while the page stays open, and with
  // each arrival in the feed.
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(timer);
  }, []);
  useEffect(() => setNow(Date.now()), [feedItems]);

  // Closing the reader from the keyboard gives the focus back to the article
  // that opened it; closing it with the mouse leaves focus where the click put it.
  const keyboard = useRef(false);
  useEffect(() => {
    const key = () => (keyboard.current = true);
    const pointer = () => (keyboard.current = false);
    window.addEventListener("keydown", key, true);
    window.addEventListener("pointerdown", pointer, true);
    return () => {
      window.removeEventListener("keydown", key, true);
      window.removeEventListener("pointerdown", pointer, true);
    };
  }, []);
  const closeReader = () => {
    const record = doc?.record;
    onCloseDoc();
    if (record && keyboard.current)
      requestAnimationFrame(() =>
        document
          .querySelector<HTMLElement>(`.caught[data-record="${CSS.escape(record)}"] .caught-title`)
          ?.focus(),
      );
  };

  // In the reader, ↑ ↓ open the article above or below in the alert's list
  // and Escape closes it.
  const shown = detail?.alert_id === currentID ? detail.matches.filter((m) => m.available) : [];
  const at = doc ? shown.findIndex((m) => m.record_id === doc.record) : -1;
  const step = (delta: 1 | -1) => {
    const next = shown[at < 0 ? 0 : at + delta];
    if (next) onOpen(next.record_id, next.version_id);
  };
  useEffect(() => {
    if (!doc) return;
    const listener = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement;
      if (["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName) || target.isContentEditable) return;
      if (document.querySelector("dialog[open]") || target.closest(".menu:has(.menu-panel)")) return;
      if (event.key === "Escape") {
        event.preventDefault();
        closeReader();
      } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        step(event.key === "ArrowDown" ? 1 : -1);
      }
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  });

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
          {status === "loading" && <LoadingState label="Chargement des alertes…" />}
          {status === "unavailable" && (
            <Notice tone="info" title="Les alertes ne sont pas activées sur ce déploiement.">
              L’administrateur peut les activer côté serveur. Le fil, la recherche, les sources et l’ajout
              de textes restent disponibles.
            </Notice>
          )}
          {status === "error" && (
            <Notice title="Les alertes n’ont pas pu être chargées." onRetry={() => setAttempt((n) => n + 1)}>
              {error}
            </Notice>
          )}
        </section>
      </main>
    );

  const shownDetail = detail?.alert_id === currentID ? detail : null;
  const unreadCaught = (caught.get(currentID || "") || []).filter(isUnread);
  return (
    <main className="board board-single alerts-board">
      <h1 className="visually-hidden">Alertes</h1>
      <section className="alerts-page" aria-labelledby="alerts-list-title">
        <div className="alerts-head">
          <h2 id="alerts-list-title" ref={listHeading} tabIndex={-1}>
            Vos alertes
          </h2>
          {alerts.length > 0 && (
            <span className="head-count">
              {alerts.length}
              <span className="visually-hidden"> alerte{alerts.length > 1 ? "s" : ""}</span>
            </span>
          )}
          <LiveBadge live={live} />
          <button type="button" className="button alerts-new" onClick={() => compose(null)}>
            <PlusIcon size={16} />
            Nouvelle alerte
          </button>
        </div>
        {alerts.length === 0 || !current || !stats ? (
          <div className="alerts-empty">
            <span className="kind-tile" aria-hidden="true">
              <AlertsIcon size={20} />
            </span>
            <h3>Aucune alerte pour l’instant</h3>
            <p>
              Une alerte lit chaque nouvel article et vous prévient dès qu’il parle de votre sujet, en mots
              précis ou décrit en une phrase.
            </p>
            <button type="button" className="button alerts-new" onClick={() => compose(null)}>
              <PlusIcon size={16} />
              Créer la première
            </button>
          </div>
        ) : (
          <>
            <div className="alerts-table-wrap">
              <table className="alerts-table" aria-label="Alertes">
                <thead>
                  <tr>
                    <th scope="col">Alerte</th>
                    <th scope="col" data-col="sources">
                      Sources
                    </th>
                    <th scope="col" data-col="week">
                      7 jours
                    </th>
                    <th scope="col">Repérés</th>
                    <th scope="col" data-col="last">
                      Dernier
                    </th>
                    <th scope="col" data-col="created">
                      Créée le
                    </th>
                    <th scope="col">État</th>
                  </tr>
                </thead>
                <tbody>
                  {alerts.map((alert) => (
                    <AlertRow
                      key={alert.alert_id}
                      alert={alert}
                      caught={caught.get(alert.alert_id) || []}
                      isUnread={isUnread}
                      every={sources}
                      logoOf={logoOf}
                      now={now}
                      selected={alert.alert_id === currentID}
                      highlight={grown.has(alert.alert_id)}
                      onSelect={() => onSelect(alert.alert_id)}
                    />
                  ))}
                </tbody>
              </table>
            </div>
            <div className="alerts-detail">
              <AlertSheet
                key={current.alert_id}
                alert={current}
                stats={stats}
                every={sources}
                logoOf={logoOf}
                now={now}
                onEdit={() => compose(current)}
                onChanged={(next, message) => {
                  upsert(next);
                  notify(message);
                }}
                onDeleted={() => removed(current)}
                onUnauthorized={onUnauthorized}
              />
              <section className="alert-caught panel" aria-labelledby="alert-caught-title">
                <div className="caught-head">
                  <h3 id="alert-caught-title" tabIndex={-1}>
                    Derniers articles repérés
                  </h3>
                  {unreadCaught.length > 0 && (
                    <button
                      type="button"
                      className="mark-read"
                      title="Marquer comme lus les articles de cette alerte"
                      onClick={() => {
                        onMarkAllRead(unreadCaught);
                        // The button goes once all is read: the focus moves to the list's title.
                        document.getElementById("alert-caught-title")?.focus();
                      }}
                    >
                      <ChecksIcon />
                      Tout marquer comme lu
                    </button>
                  )}
                  {shownDetail && shownDetail.matches.length > 0 && (
                    <span className="caught-count">
                      {shownDetail.matches.length < shownDetail.match_count
                        ? `${shownDetail.matches.length} sur ${shownDetail.match_count}${shownDetail.capped ? "+" : ""}`
                        : shownDetail.match_count}
                    </span>
                  )}
                </div>
                {!shownDetail ? (
                  detailError ? (
                    <Notice
                      title="Les articles de l’alerte n’ont pas pu être chargés."
                      onRetry={() => void reloadDetail(current.alert_id)}
                    >
                      {detailError}
                    </Notice>
                  ) : (
                    <LoadingState label="Chargement des articles…" />
                  )
                ) : shownDetail.matches.length === 0 ? (
                  <EmptyState className="caught-empty" icon={<AlertsIcon size={22} />} title="Rien pour l’instant.">
                    {current.enabled
                      ? `Les articles qui arrivent à partir de maintenant et correspondent ${
                          current.kind === "described"
                            ? "à la description s’afficheront ici. L’examen d’un article peut prendre une minute."
                            : "à l’alerte s’afficheront ici, en direct."
                        }`
                      : "L’alerte est en pause : les nouveaux articles ne sont pas examinés."}
                  </EmptyState>
                ) : (
                  <ul className="caught-list" aria-label="Articles trouvés">
                    {shownDetail.matches.map((article) => {
                      const item = byRecord.get(article.record_id);
                      return (
                        <CaughtItem
                          key={article.match_id}
                          article={article}
                          fresh={fresh.has(article.match_id)}
                          open={doc?.record === article.record_id}
                          unread={item ? isUnread(item) : false}
                          arrived={item && arrivedAt(item)}
                          connectorId={logoOf.get(article.source)}
                          now={now}
                          onOpen={() => onOpen(article.record_id, article.version_id)}
                        />
                      );
                    })}
                  </ul>
                )}
              </section>
            </div>
          </>
        )}
      </section>
      {composing && (
        <FormPanel onClose={closeForm}>
          <AlertForm
            described={described}
            editing={editing}
            sources={sources}
            logoOf={logoOf}
            feedItems={feedItems}
            onUnauthorized={onUnauthorized}
            onCancel={closeForm}
            onSaved={(alert, created) => {
              upsert(alert);
              closeForm();
              setGrown(new Set([alert.alert_id]));
              if (created) onSelect(alert.alert_id);
              else if (currentID === alert.alert_id) void reloadDetail(alert.alert_id);
              notify(
                created
                  ? `C’est noté : vous serez prévenu dès qu’un nouvel article correspondra à « ${alert.name} ».`
                  : `Alerte « ${alert.name} » mise à jour.`,
              );
            }}
          />
        </FormPanel>
      )}
      {doc && (
        // The peek stays while articles change inside it, so it slides in once.
        <div className="peek">
          <Reader
            key={doc.record + doc.version}
            doc={doc}
            item={byRecord.get(doc.record)}
            corpus={corpus}
            terms={(shown.find((m) => m.record_id === doc.record)?.terms || []).flatMap((t) => tokenize(t.term))}
            caught={alerts.filter((a) => matched[doc.record]?.includes(a.alert_id))}
            feedById={byRecord}
            logoOf={logoOf}
            onClose={closeReader}
            onStep={step}
            canStep={{ back: at > 0, forward: at >= 0 && at < shown.length - 1 }}
            onOpen={onOpen}
            onSimilar={onSimilar}
          />
        </div>
      )}
    </main>
  );
}

/**
 * The alert form in a panel over the right of the page, like the reader:
 * Escape (outside a field) or a click outside it closes it.
 */
function FormPanel({ onClose, children }: { onClose: () => void; children: ReactNode }) {
  const panel = useRef<HTMLElement>(null);
  // Closed, the panel gives the focus back to what opened it.
  const opener = useRef(document.activeElement as HTMLElement | null);
  useEffect(
    () => () => {
      if (opener.current?.isConnected) opener.current.focus();
    },
    [],
  );
  useEffect(() => {
    panel.current?.querySelector<HTMLElement>("h2")?.focus();
    const key = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement;
      if (event.key !== "Escape" || target.closest("input, textarea, select")) return;
      event.preventDefault();
      onClose();
    };
    const outside = (event: MouseEvent) => {
      const target = event.target as Element;
      if (!target.closest?.(".peek, .rail, .toast-region")) onClose();
    };
    window.addEventListener("keydown", key);
    document.addEventListener("mousedown", outside);
    return () => {
      window.removeEventListener("keydown", key);
      document.removeEventListener("mousedown", outside);
    };
  }, [onClose]);
  return (
    <div className="peek">
      <aside ref={panel} className="panel reader alert-panel" aria-label="Formulaire d’alerte">
        <div className="reader-top">
          <button type="button" className="reader-tool" title="Fermer (Échap)" onClick={onClose}>
            <ChevronsRightIcon />
            <span className="visually-hidden">Fermer</span>
          </button>
        </div>
        <div className="reader-scroll">{children}</div>
      </aside>
    </div>
  );
}

/** The alert's kind as an icon; its name is the tooltip. */
function KindTile({ kind, size = 15 }: { kind: string; size?: number }) {
  return (
    <span className="kind-tile" role="img" aria-label={KINDS[kind] || "Alerte"} title={KINDS[kind]}>
      {kind === "described" ? <DescribedIcon size={size} /> : <KeywordsIcon size={size} />}
    </span>
  );
}

function AlertRow({
  alert,
  caught,
  isUnread,
  every,
  logoOf,
  now,
  selected,
  highlight,
  onSelect,
}: {
  alert: Alert;
  /** The feed's articles it caught, newest first. */
  caught: FeedItem[];
  isUnread: (item: FeedItem) => boolean;
  every: string[];
  logoOf: Map<string, string>;
  now: number;
  selected: boolean;
  highlight: boolean;
  onSelect: () => void;
}) {
  const n = alert.match_count;
  const unread = caught.filter(isUnread).length;
  const week = daily(caught.map(arrivedAt), now);
  const most = Math.max(1, ...week.map((d) => d.count));
  const last = caught[0] && arrivedAt(caught[0]);
  return (
    <tr
      data-selected={selected || undefined}
      data-enabled={alert.enabled}
      data-highlight={highlight || undefined}
      onClick={(event) => {
        if (!(event.target as Element).closest("button")) onSelect();
      }}
    >
      <td className="name-cell">
        <div className="alert-name-cell">
          <KindTile kind={alert.kind} />
          <button
            type="button"
            className="alert-open"
            title={alert.name}
            aria-current={selected || undefined}
            onClick={onSelect}
          >
            {alert.name}
          </button>
        </div>
      </td>
      <td data-col="sources">
        <SourceLogos alert={alert} every={every} logoOf={logoOf} />
      </td>
      <td data-col="week">
        <div
          className="spark"
          role="img"
          tabIndex={0}
          data-tips
          aria-label={`Repérés sur 7 jours : ${week.map((d) => d.count).join(", ")}`}
        >
          {week.map((d, i) => (
            <span
              key={d.day}
              data-now={i === week.length - 1 || undefined}
              data-tip={`${dayLabel(d.day, now)} · ${plural(d.count, "repéré")}`}
              style={{ height: `${d.count ? Math.max(14, (d.count / most) * 100) : 8}%` }}
            />
          ))}
        </div>
      </td>
      <td className="cell-count">
        <b data-testid="alert-count" data-zero={n === 0 || undefined}>
          {n}
          {alert.capped ? "+" : ""}
        </b>
        {unread > 0 && (
          <span className="unread-pill" title={plural(unread, "article non lu", "articles non lus")}>
            {unread} nouv.
          </span>
        )}
      </td>
      <td data-col="last" className="muted">
        {last ? longTime(last, now) : "—"}
      </td>
      <td data-col="created" className="muted">
        {alert.created_at ? dayMonth.format(new Date(alert.created_at)) : "—"}
      </td>
      <td>
        <span className="alert-state" data-enabled={alert.enabled}>
          <i aria-hidden="true" />
          <span className="alert-state-label">{alert.enabled ? "Active" : "En pause"}</span>
        </span>
      </td>
    </tr>
  );
}

/** What the alert looks for: its words as chips, or its description. */
function Rule({ alert, every, logoOf }: { alert: Alert; every: string[]; logoOf: Map<string, string> }) {
  const after = (
    <>
      <span className="rule-sources">
        <SourceLogos alert={alert} every={every} logoOf={logoOf} />
      </span>
      {alert.created_at && (
        <span className="rule-created">créée le {dayMonth.format(new Date(alert.created_at))}</span>
      )}
    </>
  );
  if (alert.kind === "described")
    return (
      <p className="sheet-rule" data-kind="described">
        <span className="rule-quote">« {alert.expression.description} »</span>
        {after}
      </p>
    );
  if (alert.kind !== "keywords") return <p className="sheet-rule">Alerte d’un autre type{after}</p>;
  const form = decompose(alert.expression.match);
  if (!form)
    return (
      <p className="sheet-rule">
        <code className="alert-query">{print(alert.expression.match) ?? "Requête avancée"}</code>
        {after}
      </p>
    );
  return (
    <p className="sheet-rule">
      {form.words.map((word, i) => (
        <Fragment key={word}>
          {i > 0 && <span className="rule-op">{form.mode === "all" ? "et" : "ou"}</span>}
          <span className="kw">{word}</span>
        </Fragment>
      ))}
      {form.exclude.length > 0 && <span className="rule-op">sauf</span>}
      {form.exclude.map((word) => (
        <span key={word} className="kw" data-not="true">
          {word}
        </span>
      ))}
      {after}
    </p>
  );
}

/**
 * What the "7 jours" figure covers: since creation for a young alert, else
 * the last week, or only as far back as the feed goes.
 */
function weekLabel(s: AlertStats, now: number) {
  if (s.young) return "depuis sa création";
  if (s.feedSince === null || s.feedSince <= now - 7 * 86400000) return "7 jours";
  const hours = Math.max(1, Math.round((now - s.feedSince) / 3600000));
  return hours < 48 ? `depuis ${hours} h` : `depuis ${Math.round(hours / 24)} jours`;
}

/** A short line on what the numbers do not say at once, if anything. */
function insight(alert: Alert, s: AlertStats): { tone: "up" | "down" | "flat"; text: string } | null {
  if (!alert.enabled || !alert.match_count) return null;
  const g = s.growth;
  if (g && g.week && (g.change === null || g.change >= 30))
    return { tone: "up", text: `En hausse : ${g.week} cette semaine, ${g.before} la précédente` };
  if (g && g.change !== null && g.change <= -30)
    return { tone: "down", text: `En baisse : ${g.week} cette semaine, ${g.before} la précédente` };
  const streak = s.mode === "days" ? streakStart(s.bars) : null;
  if (streak) return { tone: "up", text: `Chaque jour depuis le ${streak.label}` };
  const period = busiestPeriod(s.hours);
  return period ? { tone: "flat", text: `Surtout ${period}` } : null;
}

function Trend({ s }: { s: AlertStats }) {
  const top = Math.max(1, ...s.bars.map((b) => b.count), ...(s.average || []));
  return (
    <div className="trend">
      <span className="sheet-label">{s.mode === "hours" ? "24 dernières heures" : `${s.bars.length} derniers jours`}</span>
      <div
        className="trend-plot"
        role="img"
        tabIndex={0}
        data-tips
        aria-label={`Repérés par ${s.mode === "hours" ? "heure" : "jour"} : ${s.bars.map((b) => b.count).join(", ")}`}
      >
        {s.bars.map((b, i) => (
          <span
            key={b.key}
            data-recent={b.recent || undefined}
            data-zero={!b.count || undefined}
            data-tip={`${b.label} · ${plural(b.count, "repéré")}${
              s.average ? ` · moyenne 7 j : ${s.average[i].toLocaleString("fr", { maximumFractionDigits: 1 })}` : ""
            }`}
            style={b.count ? { height: `${Math.max(6, (b.count / top) * 100)}%` } : undefined}
          />
        ))}
        {s.average && (
          <svg viewBox={`0 0 ${s.bars.length} 100`} preserveAspectRatio="none" aria-hidden="true">
            <title>Moyenne sur 7 jours</title>
            <polyline
              vectorEffect="non-scaling-stroke"
              points={s.average.map((v, i) => `${i + 0.5},${100 - (v / top) * 100}`).join(" ")}
            />
          </svg>
        )}
      </div>
      <div className="trend-axis">
        <span>{s.bars[0].label}</span>
        <span>{s.mode === "hours" ? "maintenant" : "aujourd’hui"}</span>
      </div>
    </div>
  );
}

function AlertSheet({
  alert,
  stats: s,
  every,
  logoOf,
  now,
  onEdit,
  onChanged,
  onDeleted,
  onUnauthorized,
}: {
  alert: Alert;
  stats: AlertStats;
  every: string[];
  logoOf: Map<string, string>;
  now: number;
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
  const n = alert.match_count;
  const editable = alert.kind === "keywords" || alert.kind === "described";
  const g = s.growth;
  const said = insight(alert, s);
  const feedTotal = s.sources.reduce((sum, x) => sum + x.count, 0);
  const shownSources = s.sources.length > 4 ? [...s.sources.slice(0, 3), { namespace: "", count: s.sources.slice(3).reduce((sum, x) => sum + x.count, 0) }] : s.sources;
  const busiest = Math.max(1, ...s.hours);
  return (
    <section className="alert-sheet panel" aria-labelledby="alert-title" data-enabled={alert.enabled}>
      <div className="sheet-head">
        <KindTile kind={alert.kind} size={17} />
        <h2 id="alert-title">{alert.name}</h2>
        <div className="sheet-actions" role="group" aria-label="Actions de l’alerte">
          <button
            type="button"
            role="switch"
            className="sheet-switch"
            aria-checked={alert.enabled}
            aria-label="Alerte active"
            disabled={busy}
            data-tip={alert.enabled ? "Mettre en pause" : "Reprendre"}
            onClick={() =>
              void run(async () => {
                const next = await (alert.enabled ? pauseAlert(alert.alert_id) : resumeAlert(alert.alert_id));
                onChanged(
                  next,
                  next.enabled
                    ? `« ${next.name} » surveille de nouveau les arrivées.`
                    : `« ${next.name} » est en pause : rien ne sera marqué jusqu’à la reprise.`,
                );
              })
            }
          >
            <span className="switch-track" aria-hidden="true">
              <span className="switch-knob" />
            </span>
            <span className="switch-label" aria-hidden="true">
              {alert.enabled ? "Active" : "En pause"}
            </span>
          </button>
          {editable && (
            <button type="button" className="icon-button" aria-label="Modifier" data-tip="Modifier" onClick={onEdit}>
              <PencilIcon />
            </button>
          )}
          <MoreMenu name={alert.name} className="sheet-more">
            {(close) => (
              <button
                type="button"
                role="menuitem"
                className="menu-option"
                data-tone="danger"
                onClick={() => {
                  close();
                  setConfirming(true);
                }}
              >
                Supprimer
              </button>
            )}
          </MoreMenu>
        </div>
      </div>
      <Rule alert={alert} every={every} logoOf={logoOf} />
      {confirming && (
        <div className="sheet-confirm" role="group" aria-label="Confirmer la suppression">
          <p>Supprimer « {alert.name} » ? Elle ne préviendra plus de rien et ses résultats ne seront plus affichés.</p>
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
            Supprimer définitivement
          </button>
          <button type="button" className="button small" onClick={() => setConfirming(false)}>
            Annuler
          </button>
        </div>
      )}
      {error && (
        <p className="error-text" role="alert">
          {error}
        </p>
      )}
      <dl className="kpis">
        <div className="kpi">
          <dt>au total</dt>
          <dd>
            {n}
            {alert.capped ? "+" : ""}
          </dd>
        </div>
        <div className="kpi">
          <dt>{weekLabel(s, now)}</dt>
          <dd>
            {s.week}
            {g && (g.week || g.before) ? (
              <small
                data-tone={g.change === null || g.change > 0 ? "up" : g.change < 0 ? "down" : undefined}
                title={`${g.week} contre ${g.before} les 7 jours d’avant`}
              >
                {g.change === null
                  ? "↗ depuis 0"
                  : g.change > 0
                    ? `↗ +${g.change} %`
                    : g.change < 0
                      ? `↘ −${-g.change} %`
                      : "= stable"}
              </small>
            ) : null}
          </dd>
        </div>
        <div className="kpi">
          <dt>{s.perDay !== null && s.perDay >= 24 ? "par heure" : "par jour"}</dt>
          <dd>{s.perDay === null ? "—" : decimal.format(s.perDay >= 24 ? s.perDay / 24 : s.perDay)}</dd>
        </div>
        <div className="kpi">
          <dt>du fil</dt>
          <dd>{s.share === null ? "—" : `${decimal.format(s.share * 100 < 10 ? s.share * 100 : Math.round(s.share * 100))} %`}</dd>
        </div>
      </dl>
      <Trend s={s} />
      {said && (
        <p className="insight" data-tone={said.tone}>
          {said.tone === "up" ? <TrendUpIcon /> : said.tone === "down" ? <TrendDownIcon /> : <ActivityIcon />}
          {said.text}
        </p>
      )}
      {feedTotal > 0 && (
        <div className="sheet-two">
          <div>
            <span className="sheet-label">Par source</span>
            <div className="source-stack" aria-hidden="true" data-tips>
              {shownSources.map((x, i) => (
                <i
                  key={x.namespace || "others"}
                  data-rank={i}
                  data-tip={`${x.namespace ? sourceName(x.namespace) : "Autres"} · ${plural(x.count, "article")} (${Math.round((x.count / feedTotal) * 100)} %)`}
                  style={{ width: `${(x.count / feedTotal) * 100}%` }}
                />
              ))}
            </div>
            <ul className="source-legend" aria-label="Par source">
              {shownSources.map((x, i) => (
                <li key={x.namespace || "others"} data-rank={i}>
                  {x.namespace ? (
                    <SourceLogo namespace={x.namespace} connectorId={logoOf.get(x.namespace)} size="small" />
                  ) : (
                    <span className="logo" data-size="small" data-kind="unknown" aria-hidden="true" />
                  )}
                  <span className="cell-clip">{x.namespace ? sourceName(x.namespace) : "Autres"}</span>
                  <b title={plural(x.count, "article")}>{Math.round((x.count / feedTotal) * 100)} %</b>
                </li>
              ))}
            </ul>
          </div>
          <div>
            <span className="sheet-label">Heure d’arrivée</span>
            <div
              className="hours"
              role="img"
              tabIndex={0}
              data-tips
              aria-label={`Repérés par heure de la journée : ${s.hours
                .map((c, h) => (c ? `${h} h : ${c}` : ""))
                .filter(Boolean)
                .join(", ")}`}
            >
              {s.hours.map((c, h) => (
                <span
                  key={h}
                  data-level={c ? Math.ceil((c / busiest) * 3) : 0}
                  data-tip={`${h} h – ${h + 1} h · ${plural(c, "repéré")}`}
                />
              ))}
            </div>
            <div className="trend-axis">
              <span>0 h</span>
              <span>12 h</span>
              <span>23 h</span>
            </div>
          </div>
        </div>
      )}
    </section>
  );
}
