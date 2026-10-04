import {
  Fragment,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from "react";
import { Broadcast, Coins, NotePencil, Plus } from "@phosphor-icons/react";
import { APIError, search, tokenize } from "../../lib/search";
import { asPercent, elapsed, explain, paid, type Why } from "../../lib/deep";
import type { SearchUsage } from "../../types";
import { HAND_NAMESPACE, type FeedItem } from "../../lib/feed";
import { createAlert, alertMessage, type Alert } from "../../lib/alerts";
import { NotationError, parse } from "../../lib/notation";
import { sourceName } from "../../lib/alertForm";
import { longTime, plural, shortTime } from "../../lib/format";
import { dayLabel, dayOf, moments } from "../../lib/moments";
import { loadMuted, saveMuted } from "../../lib/muted";
import { formatAbsolute } from "../../lib/connectors";
import type { Connector } from "../../lib/connectors";
import type { useAlertList, useFeedStream } from "../../lib/workspace";
import type { useReadState } from "../../lib/readState";
import type { Doc } from "../../App";
import { Highlight } from "../Highlight";
import {
  AlertsIcon,
  CalendarIcon,
  ChevronDownIcon,
  EyeIcon,
  EyeOffIcon,
  ChecksIcon,
  SourcesIcon,
} from "../RailIcons";
import { EmptyState, LoadingState, Notice } from "../ui";
import { groupSources } from "../connectors/SourceList";
import { Reader } from "./Reader";
import { SideColumn } from "./SideColumn";
import { SourceLogo, logoIds } from "./SourceLogo";
import { FilterMenu, MenuOption } from "./FilterMenu";

/**
 * What the feed shows: every article or the unread ones; then, when any are
 * picked, the catches of those alerts and the articles of those sources.
 */
export interface Filter {
  read: "all" | "unread";
  alerts: string[];
  sources: string[];
}
export const ALL: Filter = { read: "all", alerts: [], sources: [] };
type Facet = keyof Filter;

/** One article the search found: its best passage, in the engine's order. */
interface Hit {
  record_id: string;
  version_id: string;
  excerpt: string;
  /** Found by meaning only: the passage has none of the words typed. */
  near: boolean;
  /** Why a deep search ranked it here. */
  why: Why | null;
}

type Row = { item: FeedItem; hit?: Hit };

const SEARCH_LIMIT = 50;
const DEBOUNCE_MS = 250;
// A deep search may make a paid call: it waits for a longer pause in typing.
const DEEP_DEBOUNCE_MS = 800;

const fold = (text: string) =>
  text.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();

const when = (item: FeedItem) => item.received_at || item.published_at || "";

/** "il y a 12 min" within the hour, then "13:42" today and "12 sept." before. */
const rowTime = (value: string, now: number) =>
  now - Date.parse(value) < 3600000 ? longTime(value, now) : shortTime(value, now);

export function FeedPage({
  corpus,
  query,
  near,
  onNear,
  deepOffered,
  deep,
  onDeep,
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
  /** The engine answers a `deep` profile served by a plugin. */
  deepOffered: boolean;
  deep: boolean;
  onDeep: (deep: boolean) => void;
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
  // The engine returns at most SEARCH_LIMIT passages and no total: when it
  // fills them all, more articles match than are shown.
  const [capped, setCapped] = useState(false);
  // The sources the engine ranked the hits within, or none for every source.
  const [searchedSources, setSearchedSources] = useState<string[]>([]);
  const [searching, setSearching] = useState<"idle" | "loading" | "ready" | "error">("idle");
  const [searchError, setSearchError] = useState("");
  // The shown results come from a deep search, with what it spent if said.
  const [deepShown, setDeepShown] = useState<{ usage?: SearchUsage } | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [following, setFollowing] = useState(false);
  // One day of the feed, as "2026-10-03", or "" for every day.
  const [day, setDay] = useState("");
  const [muted, setMuted] = useState(loadMuted);
  // The moments of the timeline folded away, by key.
  const [folded, setFolded] = useState<Set<string>>(() => new Set());
  const [now, setNow] = useState(() => Date.now());
  const opener = useRef<string | null>(null);
  const terms = useMemo(() => tokenize(query), [query]);

  useEffect(() => {
    const clock = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(clock);
  }, []);
  useEffect(() => setNow(Date.now()), [feed.items]);

  // Search: lexical, or hybrid when "Idées proches" is on. The top 50
  // passages are grouped by article, in the engine's order. Chosen sources
  // are searched on their own, so their best matches come back even when
  // other sources rank higher. A corpus the engine cannot filter yet falls
  // back to narrowing the top 50 of every source. A deep search ("Recherche
  // approfondie") always fetches hybrid candidates, then the plugin re-ranks
  // them; it is never remembered beyond this page's life.
  const sourcesKey = filter.sources.join("\n");
  const deepOn = deepOffered && deep;
  const meaning = near || deepOn;
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
      const mode = meaning ? "hybrid" : "lexical";
      const sources = sourcesKey ? sourcesKey.split("\n") : [];
      const run = (within: string[]) =>
        search(
          query,
          mode,
          corpus,
          controller.signal,
          SEARCH_LIMIT,
          within,
          deepOn ? "deep" : "default",
        ).then((data) => ({ data, within }));
      run(sources)
        .catch((error) => {
          if (sources.length && error instanceof APIError && error.code === "source_filter_unavailable")
            return run([]);
          throw error;
        })
        .then(({ data, within }) => {
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
                meaning &&
                words.length > 0 &&
                !words.some((w) => text.includes(w)),
              why: deepOn ? explain(r.explanation) : null,
            });
          }
          setHits([...seen.values()]);
          setDeepShown(deepOn ? { usage: data.usage } : null);
          setCapped(data.items.length >= SEARCH_LIMIT);
          setSearchedSources(within);
          setSearching("ready");
        })
        .catch((error) => {
          if (controller.signal.aborted) return;
          if (error instanceof APIError && error.status === 401)
            return onUnauthorized();
          setSearchError(
            deepOn && error instanceof APIError
              ? deepError(error)
              : error instanceof APIError && error.status === 503 && near
                ? "La recherche par idées proches est momentanément indisponible. Cherchez les mots exacts, ou réessayez."
                : error.message,
          );
          setSearching("error");
        });
    }, deepOn ? DEEP_DEBOUNCE_MS : DEBOUNCE_MS);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [query, near, meaning, deepOn, corpus, sourcesKey, attempt, onUnauthorized]);

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

  // The connector whose site gives each source its logo.
  const logoOf = useMemo(() => logoIds(connectors), [connectors]);

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
        namespace: searchedSources.length === 1 ? searchedSources[0] : "",
          title: hit.excerpt.slice(0, 110),
          excerpt: hit.excerpt,
        },
      };
    });
    return sort === "recent"
      ? [...rows].sort((a, b) => when(b.item).localeCompare(when(a.item)))
      : rows;
  }, [query, hits, searchedSources, feed.items, byId, sort, terms]);

  // A row passes every facet of the filter but `skip`, so each facet's
  // counts read what picking it would show.
  const pass = (row: Row, skip?: Facet) => {
    const id = row.item.record_id;
    if (skip !== "read") {
      if (filter.read === "unread" && !reading.isUnread(row.item)) return false;
    }
    if (skip !== "alerts" && filter.alerts.length) {
      const caught = list?.matched[id] || [];
      if (!filter.alerts.some((a) => caught.includes(a))) return false;
    }
    // The engine already searched within the chosen sources, so its hits pass
    // even when they are older than the feed and their source is unknown here.
    const engineNarrowed = !!query && searchedSources.length > 0;
    if (
      skip !== "sources" &&
      filter.sources.length &&
      !engineNarrowed &&
      !filter.sources.includes(row.item.namespace)
    )
      return false;
    return true;
  };
  // Muted sources leave the feed, not the search, unless picked.
  const shown = query
    ? base
    : base.filter(
        (r) => !muted.has(r.item.namespace) || filter.sources.includes(r.item.namespace),
      );
  const dated = day
    ? shown.filter((r) => when(r.item) && dayOf(when(r.item)) === day)
    : shown;
  const rows = dated.filter((r) => pass(r));
  const facet = (skip: Facet) => dated.filter((r) => pass(r, skip)).map((r) => r.item);
  const readable = facet("read");
  // Every day of the list, for the date menu and the day-by-day chart.
  const span = shown.filter((r) => pass(r)).map((r) => r.item);
  const days = new Map<string, number>();
  for (const item of span) {
    const at = when(item);
    if (at) days.set(dayOf(at), (days.get(dayOf(at)) || 0) + 1);
  }
  if (day && !days.has(day)) days.set(day, 0);
  const pick = (picked: string[], value: string) =>
    picked.includes(value) ? picked.filter((v) => v !== value) : [...picked, value];
  const alertItems = facet("alerts");
  const sourceItems = facet("sources");
  const sourceRows = [
    ...groupSources(connectors).map((c) => c.source_namespace),
    ...(sourceItems.some((i) => i.namespace === HAND_NAMESPACE) ||
    filter.sources.includes(HAND_NAMESPACE) ||
    muted.has(HAND_NAMESPACE)
      ? [HAND_NAMESPACE]
      : []),
  ];
  const named = (list: string[], one: (v: string) => string, many: string) =>
    list.length === 0 ? undefined : list.length === 1 ? one(list[0]) : `${list.length} ${many}`;
  const everyAlert = (list?.items || []).map((a) => a.alert_id);
  const anyFilter =
    filter.read !== "all" || !!day || filter.alerts.length > 0 || filter.sources.length > 0;
  const mute = (namespace: string) => {
    const next = new Set(muted);
    if (!next.delete(namespace)) next.add(namespace);
    setMuted(next);
    saveMuted(next);
  };
  const pickDay = (value: string) => {
    setDay(value);
    scroller.current?.scrollTo({ top: 0 });
  };
  const timeline = query ? [] : moments(rows, (r) => when(r.item) || undefined, now);
  const toggle = (key: string) =>
    setFolded((current) => {
      const next = new Set(current);
      if (!next.delete(key)) next.add(key);
      return next;
    });

  // ↑ ↓ open the next shown article, Escape closes it (then clears the search).
  const ids = (
    query ? rows : timeline.flatMap((m) => (folded.has(m.key) ? [] : m.rows))
  ).map((r) => r.item);
  const at = ids.findIndex((i) => i.record_id === doc?.record);
  const step = (delta: 1 | -1) => {
    if (!ids.length) return;
    const next =
      delta > 0 ? Math.min(ids.length - 1, at + 1) : Math.max(0, at < 0 ? 0 : at - 1);
    opener.current = ids[next].record_id;
    onOpen(ids[next].record_id, ids[next].version_id);
    document
      .querySelector(`[data-record="${CSS.escape(ids[next].record_id)}"]`)
      ?.scrollIntoView({ block: "nearest" });
  };
  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement;
      const typing =
        ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName) ||
        target.isContentEditable;
      // An open filter menu handles its own keys.
      if (document.querySelector("dialog[open]") || target.closest(".menu:has(.menu-panel)"))
        return;
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
      step(event.key === "ArrowDown" ? 1 : -1);
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  });

  // Closing the reader from the keyboard puts focus back on the article's
  // row; closing it with the mouse leaves focus where the click put it.
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
  const wasOpen = useRef(!!doc);
  useEffect(() => {
    if (wasOpen.current && !doc && opener.current && keyboard.current)
      document
        .querySelector<HTMLAnchorElement>(
          `[data-record="${CSS.escape(opener.current)}"] .row-link`,
        )
        ?.focus();
    wasOpen.current = !!doc;
  }, [doc]);

  // A click on the article already open closes it.
  const open = (item: FeedItem) => {
    opener.current = item.record_id;
    if (doc?.record === item.record_id) onClose();
    else onOpen(item.record_id, item.version_id);
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

  const selected = doc
    ? byId.get(doc.record) ||
      base.find((r) => r.item.record_id === doc.record)?.item
    : undefined;

  let empty: { title: string; text: string } | null = null;
  if (query && searching === "ready" && !rows.length)
    empty = base.length || searchedSources.length
      ? { title: "Rien ici pour ce filtre.", text: "Retirez un filtre pour voir tous les résultats." }
      : {
          title: "Aucun article ne parle de ça.",
          text: meaning
            ? "Essayez un autre mot, ou créez une alerte : vous serez prévenu dès qu’un article en parlera."
            : "Activez « Idées proches » pour trouver aussi les articles qui en parlent avec d’autres mots.",
        };
  else if (!query && feed.items.length && !rows.length)
    empty =
      filter.read === "unread" && !filter.alerts.length && !filter.sources.length && !day
        ? { title: "Vous avez tout lu.", text: "Les prochains articles arriveront ici." }
        : { title: "Rien ici pour ce filtre.", text: "Retirez un filtre pour voir tout le fil." };

  const row = ({ item, hit }: Row) => (
    <FeedRow
      key={item.record_id}
      item={item}
      hit={hit}
      terms={terms}
      now={now}
      logo={logoOf.get(item.namespace)}
      unread={reading.isUnread(item)}
      selected={doc?.record === item.record_id}
      fresh={feed.fresh.has(item.record_id)}
      caught={caughtBy(item.record_id)}
      picked={filter.alerts}
      onAlert={(id) =>
        onFilter({
          ...filter,
          alerts: filter.alerts.length === 1 && filter.alerts[0] === id ? [] : [id],
        })
      }
      onOpen={() => open(item)}
    />
  );
  return (
    <main className="board feed-board" data-reading={!!doc || undefined}>
      <h1 className="visually-hidden">Fil</h1>
      <p className="visually-hidden" role="status" aria-live="polite">
        {feed.announcement}
      </p>
      <section className="panel feed-panel" aria-labelledby="feed-title">
        <div className="panel-head feed-head">
          {query ? (
            <h2 id="feed-title" className="feed-title">
              {searching === "ready"
                ? `${rows.length}${capped && rows.length === dated.length ? "+" : ""} article${rows.length > 1 ? "s" : ""} sur « ${query} »`
                : `« ${query} »`}
            </h2>
          ) : (
            <h2 id="feed-title" className="visually-hidden">
              Derniers articles
            </h2>
          )}
          {query && (
            <div className="search-tools">
              <button
                type="button"
                role="switch"
                aria-checked={meaning}
                aria-disabled={deepOn || undefined}
                aria-describedby={deepOn ? "near-locked" : undefined}
                className="near-switch"
                title={
                  deepOn
                    ? "La recherche approfondie cherche toujours aussi les idées proches"
                    : "Trouve aussi les articles qui parlent du même sujet avec d’autres mots"
                }
                onClick={() => {
                  if (!deepOn) onNear(!near);
                }}
              >
                <span className="switch-track" aria-hidden="true">
                  <span className="switch-knob" />
                </span>
                Idées proches
              </button>
              {deepOn && (
                <span id="near-locked" className="visually-hidden">
                  Toujours actif pendant une recherche approfondie.
                </span>
              )}
              {deepOffered && (
                <>
                  <button
                    type="button"
                    role="switch"
                    aria-checked={deep}
                    aria-describedby="deep-hint"
                    className="near-switch deep-switch"
                    title={DEEP_HINT}
                    onClick={() => onDeep(!deep)}
                  >
                    <span className="switch-track" aria-hidden="true">
                      <span className="switch-knob" />
                    </span>
                    Recherche approfondie
                  </button>
                  <span id="deep-hint" className="visually-hidden">
                    {DEEP_HINT}
                  </span>
                </>
              )}
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
          )}
          <div className="filters" role="group" aria-label="Filtrer le fil">
            <span className="segments">
              {(
                [
                  ["all", "Tout", readable.length],
                  ["unread", "Non lus", readable.filter((i) => reading.isUnread(i)).length],
                ] as const
              ).map(([value, label, n]) => (
                <button
                  key={value}
                  type="button"
                  className="chip"
                  aria-pressed={filter.read === value}
                  onClick={() => onFilter({ ...filter, read: value })}
                >
                  {label}
                  <span className="chip-count">{n}</span>
                </button>
              ))}
            </span>
            <FilterMenu
              title="Date"
              icon={<CalendarIcon />}
              summary={day ? dayLabel(day, now) : undefined}
            >
              <MenuOption single pressed={!day} onClick={() => pickDay("")} count={span.length}>
                Tous les jours
              </MenuOption>
              {[...days]
                .sort((a, b) => b[0].localeCompare(a[0]))
                .map(([d, n]) => (
                  <MenuOption key={d} single pressed={d === day} onClick={() => pickDay(d)} count={n}>
                    {dayLabel(d, now)}
                  </MenuOption>
                ))}
            </FilterMenu>
            {list?.available && (
              <FilterMenu
                title="Alertes"
                icon={<AlertsIcon size={15} />}
                summary={
                  everyAlert.length > 1 && filter.alerts.length === everyAlert.length
                    ? "Toutes les alertes"
                    : named(filter.alerts, (id) => alertsById.get(id)?.name || "Alerte", "alertes")
                }
              >
                {everyAlert.length > 1 && (
                  <MenuOption
                    pressed={filter.alerts.length === everyAlert.length}
                    onClick={() =>
                      onFilter({
                        ...filter,
                        alerts: filter.alerts.length === everyAlert.length ? [] : everyAlert,
                      })
                    }
                    count={alertItems.filter((i) => caughtBy(i.record_id).length > 0).length}
                  >
                    Toutes les alertes
                  </MenuOption>
                )}
                {list.items.map((a) => (
                  <MenuOption
                    key={a.alert_id}
                    pressed={filter.alerts.includes(a.alert_id)}
                    onClick={() => onFilter({ ...filter, alerts: pick(filter.alerts, a.alert_id) })}
                    count={alertItems.filter((i) => list.matched[i.record_id]?.includes(a.alert_id)).length}
                  >
                    {a.name}
                  </MenuOption>
                ))}
                {list.items.length === 0 && <p className="menu-empty">Aucune alerte pour l’instant.</p>}
                <button type="button" className="menu-footer" onClick={onAlerts}>
                  Gérer les alertes
                </button>
              </FilterMenu>
            )}
            <FilterMenu
              title="Sources"
              icon={<SourcesIcon size={15} />}
              summary={named(filter.sources, sourceName, "sources")}
            >
              {sourceRows.map((ns) => {
                const silenced = muted.has(ns);
                return (
                  <div key={ns} className="menu-row" data-muted={silenced || undefined}>
                    <MenuOption
                      pressed={filter.sources.includes(ns)}
                      onClick={() => onFilter({ ...filter, sources: pick(filter.sources, ns) })}
                      count={sourceItems.filter((i) => i.namespace === ns).length}
                      lead={<SourceLogo namespace={ns} connectorId={logoOf.get(ns)} size="small" />}
                    >
                      {sourceName(ns)}
                      {silenced && <span className="menu-note">Masquée du fil</span>}
                    </MenuOption>
                    <button
                      type="button"
                      className="source-mute"
                      aria-pressed={silenced}
                      title={silenced ? "Remettre dans le fil" : "Masquer du fil"}
                      onClick={() => mute(ns)}
                    >
                      {silenced ? <EyeOffIcon /> : <EyeIcon />}
                      <span className="visually-hidden">
                        {silenced ? "Remettre" : "Masquer"} {sourceName(ns)}{" "}
                        {silenced ? "dans le fil" : "du fil"}
                      </span>
                    </button>
                  </div>
                );
              })}
              {sourceRows.length === 0 && <p className="menu-empty">Aucune source pour l’instant.</p>}
              <button type="button" className="menu-footer" onClick={() => onSources()}>
                Gérer les sources
              </button>
            </FilterMenu>
            {anyFilter && (
              <button
                type="button"
                className="link-button filters-clear"
                onClick={() => {
                  onFilter(ALL);
                  pickDay("");
                }}
              >
                Tout effacer
              </button>
            )}
            {readable.some((i) => reading.isUnread(i)) && (
              <button
                type="button"
                className="mark-read"
                title="Marquer comme lus les articles affichés"
                onClick={() => {
                  reading.markAllRead(readable.filter((i) => reading.isUnread(i)));
                  // The button goes once all is read: the focus moves to the filters.
                  document.querySelector<HTMLElement>(".filters .chip")?.focus();
                }}
              >
                <ChecksIcon />
                Tout marquer comme lu
              </button>
            )}
          </div>
        </div>
        <div
          className="feed-scroll"
          ref={scroller}
          onScroll={(event) =>
            event.currentTarget.toggleAttribute("data-scrolled", event.currentTarget.scrollTop > 4)
          }
        >
          {!query && feed.status === "loading" && <LoadingState rows={5} />}
          {!query && feed.status === "error" && (
            <Notice title="Le fil ne s’affiche pas." onRetry={feed.retry}>
              {feed.error}
            </Notice>
          )}
          {query && searching === "loading" && deepOn && (
            <p className="deep-wait" aria-hidden="true">
              Re-classement des passages en cours… quelques secondes.
            </p>
          )}
          {query && searching === "loading" && (
            <LoadingState
              label={
                deepOn
                  ? "Recherche approfondie en cours, quelques secondes…"
                  : "Recherche en cours…"
              }
              rows={4}
            />
          )}
          {query && searching === "error" && (
            <Notice
              title="La recherche n’a pas abouti."
              onRetry={() => setAttempt((a) => a + 1)}
              actions={
                deepOn ? (
                  <button className="text-button" onClick={() => onDeep(false)}>
                    Revenir à la recherche normale
                  </button>
                ) : (
                  near && (
                    <button className="text-button" onClick={() => onNear(false)}>
                      Chercher les mots exacts
                    </button>
                  )
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
          {query && (
            <div className="deep-status" role="status">
              {searching === "ready" && deepShown && (
                <DeepSummary usage={deepShown.usage} hits={hits || []} />
              )}
            </div>
          )}
          {empty && (
            <div className="feed-none">
              <h3>{empty.title}</h3>
              {empty.text && <p>{empty.text}</p>}
            </div>
          )}
          {rows.length > 0 && (!query || searching === "ready") && (
            <ol className="feed-rows" aria-label="Derniers éléments">
              {query
                ? rows.map(row)
                : timeline.map((moment) => (
                    <Fragment key={moment.key}>
                      <li className="moment" role="none">
                        <button
                          type="button"
                          className="moment-pill"
                          aria-expanded={!folded.has(moment.key)}
                          onClick={() => toggle(moment.key)}
                        >
                          <span className="moment-chevron" aria-hidden="true">
                            <ChevronDownIcon size={13} />
                          </span>
                          {moment.label}
                          <span className="moment-count">
                            {moment.rows.length}
                            <span className="visually-hidden">
                              {" "}
                              {plural(moment.rows.length, "article").replace(/^\d+ /, "")}
                            </span>
                          </span>
                        </button>
                      </li>
                      {!folded.has(moment.key) && moment.rows.map(row)}
                    </Fragment>
                  ))}
            </ol>
          )}
        </div>
      </section>
      <SideColumn
        titles={feed.items
          .filter((i) => !muted.has(i.namespace) && (!day || dayOf(when(i)) === day))
          .map((i) => i.title)}
        query={query}
        span={span}
        day={day}
        onDay={pickDay}
        onSearch={(text) => {
          onQuery(text);
          onClose();
        }}
        now={now}
      />
      {doc && (
        // The peek stays while articles change inside it, so it slides in once.
        <div className="peek">
          <Reader
            key={doc.record + doc.version}
            doc={doc}
            item={selected}
            corpus={corpus}
            terms={terms}
            caught={caughtBy(doc.record)}
            feedById={byId}
            logoOf={logoOf}
            onClose={onClose}
            onStep={step}
            canStep={{ back: at > 0, forward: at >= 0 && at < ids.length - 1 }}
            onOpen={(record, version) => {
              opener.current = record;
              onOpen(record, version);
            }}
            onSimilar={(text) => {
              onQuery(text);
              onNear(true);
              onFilter(ALL);
              setSort("relevance");
              onClose();
            }}
          />
        </div>
      )}
    </main>
  );
}

function FeedRow({
  item,
  hit,
  terms,
  now,
  logo,
  unread,
  selected,
  fresh,
  caught,
  picked,
  onAlert,
  onOpen,
}: {
  item: FeedItem;
  hit?: Hit;
  terms: string[];
  now: number;
  logo?: string;
  unread: boolean;
  selected: boolean;
  fresh: boolean;
  caught: Alert[];
  /** The alerts the feed is filtered on. */
  picked: string[];
  /** Filters the feed on one alert, from its tag. */
  onAlert: (id: string) => void;
  onOpen: () => void;
}) {
  const at = item.received_at || item.published_at;
  const href = `?record=${encodeURIComponent(item.record_id)}&doc=${encodeURIComponent(item.version_id)}`;
  // A title cut to one line shows whole in a tooltip, on hover or focus.
  const [clipped, setClipped] = useState(false);
  const measure = (event: { currentTarget: HTMLElement }) =>
    setClipped(event.currentTarget.scrollWidth > event.currentTarget.clientWidth);
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
      <span className="row-node">
        {unread && <span className="row-unread" aria-hidden="true" />}
        <SourceLogo namespace={item.namespace} connectorId={logo} />
      </span>
      <div className="row-body">
        <h3
          className="row-title"
          data-clipped={clipped || undefined}
          onMouseEnter={measure}
          onFocus={measure}
        >
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
        {clipped && (
          <span className="row-tip" aria-hidden="true">
            {item.title}
          </span>
        )}
        <div className="row-meta">
          {unread && <span className="visually-hidden">Non lu.</span>}
          {item.namespace && (
            <span className="row-source">{sourceName(item.namespace)}</span>
          )}
          {at ? (
            <time
              className="row-when"
              dateTime={at}
              title={
                (item.received_at ? "Arrivé le " : "Publié le ") +
                formatAbsolute(at)
              }
            >
              {rowTime(at, now)}
            </time>
          ) : (
            <span className="row-when" title="Déjà présent avant l’ouverture du fil">
              —
            </span>
          )}
          {caught.length > 0 && (
            <ul className="row-tags" aria-label="Alertes déclenchées">
              {caught.map((a) => (
                <li key={a.alert_id}>
                  <button
                    type="button"
                    aria-pressed={picked.includes(a.alert_id)}
                    title={`Voir les articles de l’alerte « ${a.name} »`}
                    onClick={() => onAlert(a.alert_id)}
                  >
                    <AlertsIcon size={13} />
                    <span className="row-tag-name">{a.name}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
          {hit?.near && <span className="row-near">Même sujet, autres mots</span>}
          {hit?.why && <WhyTag why={hit.why} />}
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

const DEEP_HINT =
  "Re-classe les résultats selon leur chance de répondre à la recherche. Plus lent, quelques secondes, et peut faire un appel payant.";

function deepError(error: APIError) {
  if (error.code === "search_deadline_exceeded" || error.status === 504)
    return "La recherche approfondie a pris trop de temps. Réessayez, ou revenez à la recherche normale.";
  if (error.code === "unsupported_profile")
    return "Le moteur ne propose plus la recherche approfondie. Revenez à la recherche normale.";
  if (error.code === "retrieval_plugin_invalid" || error.status === 502)
    return "Le re-classement a renvoyé une réponse invalide. Réessayez, ou revenez à la recherche normale.";
  if (error.status === 503)
    return "La recherche approfondie est momentanément indisponible. Réessayez, ou revenez à la recherche normale.";
  return error.message;
}

/** One line on what a deep search did: re-ranked or not, time, paid calls. */
function DeepSummary({ usage, hits }: { usage?: SearchUsage; hits: Hit[] }) {
  const fallback = hits.find((h) => h.why?.kind === "fallback")?.why;
  return (
    <div className="deep-summary" data-fallback={!!fallback || undefined}>
      <p>
        <span className="deep-summary-name">Recherche approfondie</span>
        <span aria-hidden="true"> · </span>
        <span>
          {fallback ? "non re-classée" : "re-classée par pertinence"}
        </span>
        {usage && (
          <>
            <span aria-hidden="true"> · </span>
            <span>en {elapsed(usage.elapsed_ms)}</span>
            <span aria-hidden="true"> · </span>
            <span className="deep-paid">
              <Coins size={13} aria-hidden="true" />
              {paid(usage)}
            </span>
          </>
        )}
      </p>
      {fallback?.kind === "fallback" && (
        <p className="deep-fallback">
          Le re-classement n’a pas pu se faire : {fallback.reason}. Les
          résultats suivent l’ordre de la recherche avec idées proches.
        </p>
      )}
    </div>
  );
}

/** Why a deep search ranked this article here. */
function WhyTag({ why }: { why: Why }) {
  if (why.kind === "score")
    return (
      <span
        className="row-why"
        title={`Probabilité, estimée par le re-classement, que ce passage réponde à la recherche (${why.text})`}
      >
        <span className="why-meter" aria-hidden="true">
          <span style={{ width: `${Math.round(why.probability * 100)}%` }} />
        </span>
        {asPercent(why.probability)} de chances de répondre
      </span>
    );
  if (why.kind === "fallback")
    return (
      <span className="row-why" data-kind="fallback" title={why.text}>
        Non re-classé
      </span>
    );
  return (
    <span className="row-why" data-kind="other" title={why.text}>
      {why.text.length > 80 ? why.text.slice(0, 79) + "…" : why.text}
    </span>
  );
}
