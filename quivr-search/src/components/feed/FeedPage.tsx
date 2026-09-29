import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from "react";
import { Broadcast, NotePencil, Plus } from "@phosphor-icons/react";
import { APIError, search, tokenize } from "../../lib/search";
import { HAND_NAMESPACE, type FeedItem } from "../../lib/feed";
import { createAlert, alertMessage, type Alert } from "../../lib/alerts";
import { NotationError, parse } from "../../lib/notation";
import { sourceName } from "../../lib/alertForm";
import { plural, shortTime, today } from "../../lib/format";
import { formatAbsolute } from "../../lib/connectors";
import type { Connector } from "../../lib/connectors";
import type { useAlertList, useFeedStream } from "../../lib/workspace";
import type { useReadState } from "../../lib/readState";
import type { Doc } from "../../App";
import { Highlight } from "../Highlight";
import { EmptyState, LoadingState, Notice } from "../ui";
import { Reader } from "./Reader";
import { SideColumn } from "./SideColumn";

export type Filter =
  | { kind: "all" }
  | { kind: "unread" }
  | { kind: "caught" }
  | { kind: "alert"; id: string }
  | { kind: "source"; namespace: string };

/** One article the search found: its best passage, in the engine's order. */
interface Hit {
  record_id: string;
  version_id: string;
  excerpt: string;
  /** Found by meaning only: the passage has none of the words typed. */
  near: boolean;
}

type Row = { item: FeedItem; hit?: Hit };

const SEARCH_LIMIT = 50;
const DEBOUNCE_MS = 250;

const fold = (text: string) =>
  text.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();

const when = (item: FeedItem) => item.received_at || item.published_at || "";

export function FeedPage({
  corpus,
  query,
  near,
  onNear,
  onQuery,
  filter,
  onFilter,
  doc,
  onOpen,
  onClose,
  feed,
  alerts,
  connectors,
  reading,
  scroller,
  onAdd,
  onAlerts,
  onSources,
  notify,
  onUnauthorized,
}: {
  corpus: string;
  query: string;
  near: boolean;
  onNear: (near: boolean) => void;
  onQuery: (query: string) => void;
  filter: Filter;
  onFilter: (filter: Filter) => void;
  doc: Doc | null;
  onOpen: (record: string, version: string) => void;
  onClose: () => void;
  feed: ReturnType<typeof useFeedStream>;
  alerts: ReturnType<typeof useAlertList>;
  connectors: Connector[];
  reading: ReturnType<typeof useReadState>;
  scroller: RefObject<HTMLDivElement | null>;
  onAdd: () => void;
  onAlerts: () => void;
  onSources: (connectorId?: string) => void;
  notify: (text: string) => void;
  onUnauthorized: () => void;
}) {
  const [sort, setSort] = useState<"relevance" | "recent">("relevance");
  const [hits, setHits] = useState<Hit[] | null>(null);
  const [searching, setSearching] = useState<"idle" | "loading" | "ready" | "error">("idle");
  const [searchError, setSearchError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [following, setFollowing] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const opener = useRef<string | null>(null);
  const terms = useMemo(() => tokenize(query), [query]);

  useEffect(() => {
    const clock = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(clock);
  }, []);
  useEffect(() => setNow(Date.now()), [feed.items]);

  // Search: lexical, or hybrid when "Idées proches" is on. The top 50
  // passages are grouped by article, in the engine's order.
  useEffect(() => {
    if (!query) {
      setHits(null);
      setSearching("idle");
      return;
    }
    const controller = new AbortController();
    setSearching("loading");
    setSearchError("");
    const timer = setTimeout(() => {
      search(
        query,
        near ? "hybrid" : "lexical",
        corpus,
        controller.signal,
        SEARCH_LIMIT,
      )
        .then((data) => {
          const words = tokenize(query).filter((w) => w.length > 2);
          const seen = new Map<string, Hit>();
          for (const r of data.items) {
            if (seen.has(r.record_id)) continue;
            const text = fold(r.excerpt.text);
            seen.set(r.record_id, {
              record_id: r.record_id,
              version_id: r.version_id,
              excerpt: r.excerpt.text.replace(/\s+/g, " ").trim(),
              near:
                near &&
                words.length > 0 &&
                !words.some((w) => text.includes(w)),
            });
          }
          setHits([...seen.values()]);
          setSearching("ready");
        })
        .catch((error) => {
          if (controller.signal.aborted) return;
          if (error instanceof APIError && error.status === 401)
            return onUnauthorized();
          setSearchError(
            error instanceof APIError && error.status === 503 && near
              ? "La recherche par idées proches est momentanément indisponible. Cherchez les mots exacts, ou réessayez."
              : error.message,
          );
          setSearching("error");
        });
    }, DEBOUNCE_MS);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [query, near, corpus, attempt, onUnauthorized]);

  const byId = useMemo(() => {
    const map = new Map<string, FeedItem>();
    for (const item of [...feed.pending, ...feed.items])
      if (!map.has(item.record_id)) map.set(item.record_id, item);
    return map;
  }, [feed.items, feed.pending]);

  const list = alerts.list;
  const alertsById = useMemo(
    () => new Map((list?.items || []).map((a) => [a.alert_id, a])),
    [list],
  );
  const caughtBy = useCallback(
    (record: string) =>
      (list?.matched[record] || [])
        .map((id) => alertsById.get(id))
        .filter((a): a is Alert => !!a),
    [list, alertsById],
  );

  const base: Row[] = useMemo(() => {
    if (!query) return feed.items.map((item) => ({ item }));
    if (!hits) return [];
    const words = terms.filter((w) => w.length > 2);
    const rows = hits.map((hit) => {
      const known = byId.get(hit.record_id);
      // Same subject, other words: neither the passage nor the title has one.
      const near =
        hit.near && !(known && words.some((w) => fold(known.title).includes(w)));
      return {
        hit: near === hit.near ? hit : { ...hit, near },
        item: known || {
        // An article older than the feed's window: its passage stands in.
        record_id: hit.record_id,
        version_id: hit.version_id,
        namespace: "",
          title: hit.excerpt.slice(0, 110),
          excerpt: hit.excerpt,
        },
      };
    });
    return sort === "recent"
      ? [...rows].sort((a, b) => when(b.item).localeCompare(when(a.item)))
      : rows;
  }, [query, hits, feed.items, byId, sort, terms]);

  const pass = useCallback(
    (row: Row) => {
      const id = row.item.record_id;
      switch (filter.kind) {
        case "unread":
          return reading.isUnread(row.item);
        case "caught":
          return caughtBy(id).length > 0;
        case "alert":
          return (list?.matched[id] || []).includes(filter.id);
        case "source":
          return row.item.namespace === filter.namespace;
        default:
          return true;
      }
    },
    [filter, reading, caughtBy, list],
  );
  const rows = base.filter(pass);
  const count = (test: (row: Row) => boolean) => base.filter(test).length;

  // ↑ ↓ open the next article, Escape closes it (then clears the search).
  const ids = rows.map((r) => r.item);
  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement;
      const typing =
        ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName) ||
        target.isContentEditable;
      if (document.querySelector("dialog[open]")) return;
      if (event.key === "Escape") {
        if (doc) {
          event.preventDefault();
          onClose();
        } else if (query && !typing) onQuery("");
        return;
      }
      if (typing || (event.key !== "ArrowDown" && event.key !== "ArrowUp"))
        return;
      if (!ids.length) return;
      event.preventDefault();
      const at = ids.findIndex((i) => i.record_id === doc?.record);
      const next =
        event.key === "ArrowDown"
          ? Math.min(ids.length - 1, at + 1)
          : Math.max(0, at < 0 ? 0 : at - 1);
      opener.current = ids[next].record_id;
      onOpen(ids[next].record_id, ids[next].version_id);
      document
        .querySelector(`[data-record="${CSS.escape(ids[next].record_id)}"]`)
        ?.scrollIntoView({ block: "nearest" });
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  });

  // Closing the reader puts keyboard focus back on the article's row.
  const wasOpen = useRef(!!doc);
  useEffect(() => {
    if (wasOpen.current && !doc && opener.current)
      document
        .querySelector<HTMLAnchorElement>(
          `[data-record="${CSS.escape(opener.current)}"] .row-link`,
        )
        ?.focus();
    wasOpen.current = !!doc;
  }, [doc]);

  const open = (item: FeedItem) => {
    opener.current = item.record_id;
    onOpen(item.record_id, item.version_id);
  };

  const followed = (list?.items || []).some(
    (a) => fold(a.name) === fold(query),
  );
  async function follow() {
    if (followed || following || !query) return;
    let expression;
    try {
      expression = parse(query);
    } catch (e) {
      if (!(e instanceof NotationError)) throw e;
      // Not the query notation: the whole text is one expression.
      expression = { kind: "keywords" as const, match: { term: query } };
    }
    setFollowing(true);
    try {
      await createAlert(query.slice(0, 120), expression, crypto.randomUUID());
      await alerts.reload();
      notify(
        `C’est noté : vous serez prévenu dès qu’un nouvel article parlera de « ${query} ».`,
      );
    } catch (e) {
      if (e instanceof APIError && e.status === 401) onUnauthorized();
      notify(alertMessage(e));
    } finally {
      setFollowing(false);
    }
  }

  const chip = (
    key: string,
    active: boolean,
    label: string,
    n: number,
    onClick: () => void,
    extra?: { alert?: boolean; closable?: boolean; tone?: "alert" | "source" },
  ) => (
    <button
      key={key}
      type="button"
      className="chip"
      data-tone={extra?.tone}
      aria-pressed={active}
      onClick={onClick}
    >
      {extra?.alert && <span className="diamond" aria-hidden="true" />}
      {label}
      <span className="chip-count">{n}</span>
      {extra?.closable && (
        <span className="chip-close" aria-hidden="true">
          ×
        </span>
      )}
    </button>
  );
  const is = (kind: Filter["kind"]) => filter.kind === kind;
  const activeAlert =
    filter.kind === "alert" ? alertsById.get(filter.id) : undefined;

  const selected = doc
    ? byId.get(doc.record) ||
      base.find((r) => r.item.record_id === doc.record)?.item
    : undefined;

  let empty: { title: string; text: string } | null = null;
  if (query && searching === "ready" && !rows.length)
    empty = base.length
      ? { title: "Rien ici pour ce filtre.", text: "Retirez le filtre pour voir tous les résultats." }
      : {
          title: "Aucun article ne parle de ça.",
          text: near
            ? "Essayez un autre mot, ou créez une alerte : vous serez prévenu dès qu’un article en parlera."
            : "Activez « Idées proches » pour trouver aussi les articles qui en parlent avec d’autres mots.",
        };
  else if (!query && feed.items.length && !rows.length)
    empty =
      filter.kind === "unread"
        ? { title: "Vous avez tout lu.", text: "Les prochains articles arriveront ici." }
        : { title: "Rien ici pour l’instant.", text: "Les nouveaux articles apparaîtront ici dès leur arrivée." };

  const n = feed.pending.length;
  return (
    <main className="board" data-reading={!!doc || undefined}>
      <h1 className="visually-hidden">Fil</h1>
      <p className="visually-hidden" role="status" aria-live="polite">
        {feed.announcement}
      </p>
      <section className="panel feed-panel" aria-labelledby="feed-title">
        <div className="panel-head feed-head">
          <h2 id="feed-title" className="feed-title">
            {query
              ? searching === "ready"
                ? `${plural(rows.length, "article")} sur « ${query} »`
                : `« ${query} »`
              : "Aujourd’hui"}
          </h2>
          <div className="chips" role="group" aria-label="Filtrer le fil">
            {chip("all", is("all"), "Tout", base.length, () =>
              onFilter({ kind: "all" }),
            )}
            {chip(
              "unread",
              is("unread"),
              "Non lus",
              count((r) => reading.isUnread(r.item)),
              () => onFilter({ kind: "unread" }),
            )}
            {list?.available &&
              chip(
                "caught",
                is("caught"),
                "Attrapés par une alerte",
                count((r) => caughtBy(r.item.record_id).length > 0),
                () => onFilter({ kind: "caught" }),
                { alert: true },
              )}
            {activeAlert &&
              chip(
                "alert",
                true,
                activeAlert.name,
                count((r) =>
                  (list?.matched[r.item.record_id] || []).includes(
                    activeAlert.alert_id,
                  ),
                ),
                () => onFilter({ kind: "all" }),
                { alert: true, closable: true, tone: "alert" },
              )}
            {filter.kind === "source" &&
              chip(
                "source",
                true,
                sourceName(filter.namespace),
                count((r) => r.item.namespace === filter.namespace),
                () => onFilter({ kind: "all" }),
                { closable: true, tone: "source" },
              )}
          </div>
          {query ? (
            <div className="search-tools">
              <button
                type="button"
                role="switch"
                aria-checked={near}
                className="near-switch"
                title="Trouve aussi les articles qui parlent du même sujet avec d’autres mots"
                onClick={() => onNear(!near)}
              >
                <span className="switch-track" aria-hidden="true">
                  <span className="switch-knob" />
                </span>
                Idées proches
              </button>
              <div className="sorts" role="group" aria-label="Trier">
                {(
                  [
                    ["relevance", "Pertinence"],
                    ["recent", "Récents"],
                  ] as const
                ).map(([value, label]) => (
                  <button
                    key={value}
                    type="button"
                    aria-pressed={sort === value}
                    onClick={() => setSort(value)}
                  >
                    {label}
                  </button>
                ))}
              </div>
              {list?.available && (
                <button
                  type="button"
                  className="follow"
                  data-done={followed || undefined}
                  disabled={following}
                  aria-disabled={followed || undefined}
                  onClick={() => void follow()}
                >
                  {followed
                    ? "Alerte créée ✓"
                    : following
                      ? "Création…"
                      : "Créer une alerte"}
                </button>
              )}
            </div>
          ) : (
            <span className="feed-day">{today(now)}</span>
          )}
        </div>
        <div className="feed-scroll" ref={scroller}>
          {n > 0 && (
            <div className="pending-wrap">
              <button
                type="button"
                className="pending"
                onClick={() => {
                  onQuery("");
                  onFilter({ kind: "all" });
                  feed.showPending();
                  scroller.current?.scrollTo({ top: 0 });
                  window.scrollTo({ top: 0 });
                }}
              >
                ↑ {n} nouvel{n > 1 ? "s" : ""} article{n > 1 ? "s" : ""} ·
                Afficher
              </button>
            </div>
          )}
          {!query && feed.status === "loading" && <LoadingState rows={5} />}
          {!query && feed.status === "error" && (
            <Notice title="Le fil ne s’affiche pas." onRetry={feed.retry}>
              {feed.error}
            </Notice>
          )}
          {query && searching === "loading" && (
            <LoadingState label="Recherche en cours…" rows={4} />
          )}
          {query && searching === "error" && (
            <Notice
              title="La recherche n’a pas abouti."
              onRetry={() => setAttempt((a) => a + 1)}
              actions={
                near && (
                  <button className="text-button" onClick={() => onNear(false)}>
                    Chercher les mots exacts
                  </button>
                )
              }
            >
              {searchError}
            </Notice>
          )}
          {!query && feed.status === "ready" && feed.items.length === 0 && (
            <EmptyState
              className="feed-empty"
              icon={<Broadcast size={26} aria-hidden="true" />}
              title="Rien n’est encore arrivé."
              actions={
                <>
                  <button className="button primary" onClick={() => onSources()}>
                    <Plus size={17} weight="bold" aria-hidden="true" />
                    Ajouter une source
                  </button>
                  <button className="button" onClick={onAdd}>
                    <NotePencil size={17} aria-hidden="true" />
                    Ajouter du texte
                  </button>
                </>
              }
            >
              Ajoutez une source, par exemple un flux RSS, ou collez un texte.
              Chaque nouvel article apparaît ici en quelques secondes, sans
              recharger la page.
            </EmptyState>
          )}
          {empty && (
            <div className="feed-none">
              <h3>{empty.title}</h3>
              {empty.text && <p>{empty.text}</p>}
            </div>
          )}
          {rows.length > 0 && (!query || searching === "ready") && (
            <ol className="feed-rows" aria-label="Derniers éléments">
              {rows.map(({ item, hit }) => (
                <FeedRow
                  key={item.record_id}
                  item={item}
                  hit={hit}
                  terms={terms}
                  now={now}
                  unread={reading.isUnread(item)}
                  selected={doc?.record === item.record_id}
                  fresh={feed.fresh.has(item.record_id)}
                  caught={caughtBy(item.record_id)}
                  onOpen={() => open(item)}
                />
              ))}
            </ol>
          )}
        </div>
      </section>
      {doc ? (
        <Reader
          key={doc.record + doc.version}
          doc={doc}
          item={selected}
          corpus={corpus}
          terms={terms}
          caught={caughtBy(doc.record)}
          feedById={byId}
          onClose={onClose}
          onOpen={(record, version) => {
            opener.current = record;
            onOpen(record, version);
          }}
          onSimilar={(text) => {
            onQuery(text);
            onNear(true);
            onFilter({ kind: "all" });
            setSort("relevance");
            onClose();
          }}
        />
      ) : (
        <SideColumn
          alerts={list}
          connectors={connectors}
          items={[...feed.pending, ...feed.items]}
          filter={filter}
          now={now}
          onFilter={(f) => {
            onFilter(f);
            if (f.kind === "alert") onQuery("");
            scroller.current?.scrollTo({ top: 0 });
          }}
          onOpen={(item) => open(item)}
          onAlerts={onAlerts}
          onSources={onSources}
        />
      )}
    </main>
  );
}

function FeedRow({
  item,
  hit,
  terms,
  now,
  unread,
  selected,
  fresh,
  caught,
  onOpen,
}: {
  item: FeedItem;
  hit?: Hit;
  terms: string[];
  now: number;
  unread: boolean;
  selected: boolean;
  fresh: boolean;
  caught: Alert[];
  onOpen: () => void;
}) {
  const at = item.received_at || item.published_at;
  const href = `?record=${encodeURIComponent(item.record_id)}&doc=${encodeURIComponent(item.version_id)}`;
  return (
    <li
      className="row"
      data-record={item.record_id}
      data-unread={unread || undefined}
      data-selected={selected || undefined}
      data-fresh={fresh || undefined}
      data-alerted={caught.length > 0 || undefined}
      onClick={(event) => {
        if ((event.target as HTMLElement).closest("a, button")) return;
        onOpen();
      }}
    >
      <div className="row-when">
        {unread && (
          <span className="row-dot" title="Non lu">
            <span className="visually-hidden">Non lu. </span>
          </span>
        )}
        {at ? (
          <time
            dateTime={at}
            title={
              (item.received_at ? "Arrivé le " : "Publié le ") +
              formatAbsolute(at)
            }
          >
            {shortTime(at, now)}
          </time>
        ) : (
          <span title="Déjà présent avant l’ouverture du fil">—</span>
        )}
      </div>
      <div className="row-body">
        <h3 className="row-title">
          <a
            className="row-link"
            href={href}
            aria-current={selected || undefined}
            onClick={(event) => {
              if (!event.metaKey && !event.ctrlKey && !event.shiftKey) {
                event.preventDefault();
                onOpen();
              }
            }}
          >
            <Highlight text={item.title || "Sans titre"} terms={terms} />
          </a>
        </h3>
        <div className="row-meta">
          {item.namespace && (
            <span className="row-source">
              {item.namespace === HAND_NAMESPACE
                ? "Ajouté à la main"
                : item.namespace}
            </span>
          )}
          {caught.length > 0 && (
            <ul className="row-tags" aria-label="Alertes déclenchées">
              {caught.map((a) => (
                <li key={a.alert_id}>
                  <span className="diamond" aria-hidden="true" />
                  {a.name}
                </li>
              ))}
            </ul>
          )}
          {hit?.near && <span className="row-near">Même sujet, autres mots</span>}
          {item.updated_at && <span className="row-updated">Corrigé</span>}
        </div>
        {hit && hit.excerpt && (
          <p className="row-excerpt">
            <Highlight text={hit.excerpt} terms={terms} />
          </p>
        )}
      </div>
    </li>
  );
}
