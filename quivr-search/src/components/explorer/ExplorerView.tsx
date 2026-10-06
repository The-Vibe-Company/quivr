import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { MagnifyingGlass, X } from "@phosphor-icons/react";
import { APIError } from "../../lib/search";
import { COMMON_TYPES, fieldLabel, type Corpus, type Exclusion, type Field } from "../../lib/corpora";
import {
  countLabel,
  fetchExplore,
  fetchFacets,
  predicatesOf,
  rangeLabel,
  readState,
  TIMELINE_FIELD,
  valueLabel,
  writeState,
  type ExplorePage,
  type ExplorerState,
  type Facets,
  type Range,
} from "../../lib/explore";
import { EmptyState, InBar, Notice, type Bar } from "../ui";
import { FacetColumn } from "./Facets";
import { Preview } from "./Preview";
import { RecordPage } from "./RecordPage";
import { Timeline } from "./Timeline";
import { byDate, WireList, WireSkeleton } from "./WireList";
import "../../explorer.css";

// Below this width the page is one column and a row opens its document.
const NARROW = "(max-width: 1099px)";
const SEARCH_DELAY_MS = 300;

const narrowQuery = () => window.matchMedia(NARROW);
const subscribeNarrow = (change: () => void) => {
  const query = narrowQuery();
  query.addEventListener("change", change);
  return () => query.removeEventListener("change", change);
};

/**
 * The Explorer (THE-1171, THE-1204): a reading room for the corpora the demo
 * reads. On top, a search within the corpus picked and the corpus switcher;
 * then the timeline, whose range is a filter, and the active filters with
 * the live count. Below, three columns: the facets with their counts, the
 * wire list by day, and the preview of the document selected. The address
 * keeps the whole view, and a document opens on its own page.
 */
export function ExplorerView({
  bar,
  corpora,
  corporaRead,
  picked,
  onPicked,
  record,
  onRecord,
  initial,
  onState,
  onUnauthorized,
}: {
  /** The top bar: the search goes in its search's place, the corpora at its right. */
  bar: Bar;
  corpora: Corpus[];
  /** Whether the corpora were read, or could not be. */
  corporaRead: boolean;
  picked: string[];
  onPicked: (ids: string[]) => void;
  record: string | null;
  onRecord: (id: string | null) => void;
  /** The address the view starts from. */
  initial: string;
  /** The view's own part of the address, on each change. */
  onState: (params: string) => void;
  onUnauthorized: () => void;
}) {
  const [state, setState] = useState<ExplorerState>(() => readState(new URLSearchParams(initial)));
  const [input, setInput] = useState(state.q);
  const [page, setPage] = useState<ExplorePage | null>(null);
  const [status, setStatus] = useState<"loading" | "ready" | "error">("loading");
  const [error, setError] = useState("");
  const [more, setMore] = useState<"idle" | "loading" | "error">("idle");
  // With nothing selected, the preview shows the list's first row as first
  // drawn, by date and by rank: a later page putting a newer row on top
  // does not move it.
  const [lead, setLead] = useState<{ key: string; byDate?: string; byRank?: string } | null>(null);
  // The counts, with the corpora and filters they were read for.
  const [facets, setFacets] = useState<(Facets & { key: string }) | null>(null);
  const [facetsError, setFacetsError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const narrow = useSyncExternalStore(subscribeNarrow, () => narrowQuery().matches);

  useEffect(() => onState(writeState(state).toString()), [state, onState]);
  // Another corpus picked starts the list again, with nothing selected.
  const corporaShown = useRef(picked.join(","));
  useEffect(() => {
    if (corporaShown.current === picked.join(",")) return;
    corporaShown.current = picked.join(",");
    setState((s) => (s.selected ? { ...s, selected: null } : s));
  }, [picked]);
  // A search waits for a pause in typing.
  useEffect(() => {
    const q = input.trim();
    const timer = setTimeout(
      () => setState((s) => (s.q === q ? s : { ...s, q, selected: null, sort: q ? s.sort : "recent" })),
      SEARCH_DELAY_MS,
    );
    return () => clearTimeout(timer);
  }, [input]);

  // Every field of the corpora read, for its type: the picked corpora's
  // first, so a name two corpora share takes the type of the one browsed.
  const types = useMemo(() => {
    // The common fields' types are known before the corpora are read.
    const out = new Map<string, Field["type"]>(COMMON_TYPES);
    const order = [...corpora].sort(
      (a, b) => Number(!picked.includes(a.corpus_id)) - Number(!picked.includes(b.corpus_id)),
    );
    const own = new Set<string>();
    for (const c of order)
      for (const f of [...c.common, ...c.own])
        if (!own.has(f.name)) {
          own.add(f.name);
          out.set(f.name, f.type);
        }
    return out;
  }, [corpora, picked]);
  // Once the corpora are read, a field none of them declares leaves the
  // address: a shared link naming one filters nothing.
  useEffect(() => {
    if (!corpora.length) return;
    const unknown = Object.keys(state.selection).filter((field) => !types.has(field));
    if (!unknown.length) return;
    setState((s) => {
      const selection = { ...s.selection };
      for (const field of unknown) delete selection[field];
      return { ...s, selection };
    });
  }, [corpora, types, state.selection]);
  const predicates = useMemo(
    () => predicatesOf(state.selection, types, state.range),
    [state.selection, state.range, types],
  );
  const listKey = JSON.stringify([picked, predicates, state.q]);
  const facetsKey = JSON.stringify([picked, predicates, state.window]);
  // The list's current reads: a filter or corpus change aborts the page of
  // "more" still on its way, which belongs to the list it leaves.
  const reads = useRef<AbortController | null>(null);
  // The list's own scroll, on a desktop.
  const scroller = useRef<HTMLDivElement>(null);
  const listBox = useRef<HTMLElement>(null);
  // The corpora the facets were read for: others picked, their fields go at once.
  const facetsFor = useRef("");

  // Filters restored from the address wait for the fields' types, unless
  // the corpora could not be read.
  const typed = corporaRead || !Object.keys(state.selection).length;

  useEffect(() => {
    if (record || !typed) return;
    const controller = new AbortController();
    reads.current = controller;
    near.current = false;
    setStatus("loading");
    setMore("idle");
    fetchExplore(picked, predicates, { q: state.q }, controller.signal)
      .then((data) => {
        setPage(data);
        setLead({ key: listKey, byDate: byDate(data.items)[0]?.record_id, byRank: data.items[0]?.record_id });
        setStatus("ready");
        // Another list starts from its top: the list's own scroll on a
        // desktop, else the page brought back to the list when past it.
        scroller.current?.scrollTo({ top: 0 });
        if (listBox.current && listBox.current.getBoundingClientRect().top < 0)
          listBox.current.scrollIntoView({ block: "start" });
      })
      .catch((e) => {
        if (controller.signal.aborted) return;
        if (e instanceof APIError && e.status === 401) return onUnauthorized();
        setError(e instanceof Error ? e.message : "Les documents ne s’affichent pas.");
        setStatus("error");
      });
    return () => controller.abort();
    // The key holds the corpora, the predicates and the text.
  }, [listKey, attempt, record, typed, onUnauthorized]);

  useEffect(() => {
    if (record || !typed) return;
    const controller = new AbortController();
    const corporaKey = picked.join(",");
    if (facetsFor.current !== corporaKey) {
      facetsFor.current = corporaKey;
      setFacets(null);
    }
    setFacetsError("");
    fetchFacets(picked, predicates, state.window, controller.signal)
      .then((data) => setFacets({ ...data, key: facetsKey }))
      .catch((e) => {
        if (controller.signal.aborted) return;
        if (e instanceof APIError && e.status === 401) return onUnauthorized();
        // The list still shows; the facets keep their last values.
        setFacetsError(e instanceof Error ? e.message : "Les nombres ne s’affichent pas.");
      });
    return () => controller.abort();
    // The key holds the corpora, the predicates and the span shown.
  }, [facetsKey, attempt, record, typed, onUnauthorized]);

  const loadMore = () => {
    const controller = reads.current;
    if (!page?.next_cursor || more === "loading" || !controller) return;
    setMore("loading");
    fetchExplore(picked, predicates, { cursor: page.next_cursor }, controller.signal)
      .then((next) => {
        if (controller.signal.aborted) return;
        setPage((shown) => ({
          ...next,
          items: [
            ...(shown?.items || []),
            ...next.items.filter((i) => !shown?.items.some((s) => s.record_id === i.record_id)),
          ],
        }));
        setMore("idle");
      })
      .catch((e) => {
        if (controller.signal.aborted) return;
        if (e instanceof APIError && e.status === 401) return onUnauthorized();
        setMore("error");
      });
  };

  // The corpora that do not fit scroll sideways, the edge faded to say so.
  const switchRef = useRef<HTMLDivElement>(null);
  const [switchOverflow, setSwitchOverflow] = useState(false);
  useEffect(() => {
    const element = switchRef.current;
    if (!element) return;
    const measure = () => setSwitchOverflow(element.scrollWidth > element.clientWidth + 1);
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [narrow, corpora, bar.actions]);

  // The next page loads as the end of the list comes near, the button
  // staying as a fallback; a failed page waits for its "Réessayer". Only
  // coming near loads: rows a page puts back above the reader (by date)
  // leave the end in sight, and must not chain page after page unasked.
  const sentinel = useRef<HTMLDivElement>(null);
  const near = useRef(false);
  const loadNext = useRef(loadMore);
  loadNext.current = loadMore;
  const hasMore = !!page?.next_cursor && more === "idle" && status === "ready";
  useEffect(() => {
    const target = sentinel.current;
    if (!hasMore || !target) return;
    const observer = new IntersectionObserver(
      (entries) => {
        const now = entries.some((e) => e.isIntersecting);
        if (now && !near.current) loadNext.current();
        near.current = now;
      },
      // On a narrow screen the page scrolls, not the list.
      { root: narrow ? null : scroller.current, rootMargin: "0px 0px 600px 0px" },
    );
    observer.observe(target);
    return () => observer.disconnect();
  }, [hasMore, narrow, page]);

  // A filter change starts the list again, with nothing selected.
  const filter = (change: (s: ExplorerState) => Partial<ExplorerState>) =>
    setState((s) => ({ ...s, ...change(s), selected: null }));
  const toggle = (field: string, value: string) =>
    filter((s) => {
      const values = s.selection[field] || [];
      const next = values.includes(value) ? values.filter((v) => v !== value) : [...values, value];
      const selection = { ...s.selection, [field]: next };
      if (!next.length) delete selection[field];
      return { selection };
    });
  // A corpus's own date picks one period, or none.
  const pickPeriod = (field: string, period: string | undefined) =>
    filter((s) => {
      const selection = { ...s.selection, [field]: period ? [period] : [] };
      if (!period) delete selection[field];
      return { selection };
    });
  const setRange = (range: Range | undefined) => filter(() => ({ range }));
  const clearAll = () => filter(() => ({ selection: {}, range: undefined, window: undefined }));
  // On a narrow screen there is no preview: a click opens the document.
  const select = (id: string, by: "click" | "key") =>
    narrow && by === "click" ? onRecord(id) : setState((s) => ({ ...s, selected: id }));

  const nameOf = (id?: string) => corpora.find((c) => c.corpus_id === id)?.name || id || "";
  const single = picked.length === 1 ? corpora.find((c) => c.corpus_id === picked[0]) : undefined;
  const all = corpora.length > 0 && corpora.every((c) => picked.includes(c.corpus_id));
  // How many documents the corpora picked hold, once each is counted.
  const counted = corpora.filter((c) => picked.includes(c.corpus_id));
  const documents =
    counted.length === new Set(picked).size && counted.every((c) => c.documents !== undefined)
      ? counted.reduce((n, c) => n + c.documents!, 0)
      : undefined;
  const fresh = facets?.key === facetsKey ? facets : null;
  // Only filters the requests carry show as active: a field of unknown type is not.
  const picks = Object.entries(state.selection)
    .filter(([field]) => types.has(field))
    .flatMap(([field, values]) => values.map((value) => ({ field, value })));
  const active = picks.length + (state.range ? 1 : 0);
  // The corpora left out, as the list and the counts of the same filters report them.
  const excluded = mergeExclusions([...(page?.excluded_corpora || []), ...(fresh?.excluded_corpora || [])]);
  const timeline = facets?.fields.find((f) => f.field === TIMELINE_FIELD);
  // A search keeps its rank on request; otherwise rows go by date.
  const byDay = !state.q || state.sort !== "relevance";
  const items = useMemo(() => (byDay ? byDate(page?.items || []) : page?.items || []), [page, byDay]);
  const first = lead?.key === listKey ? (byDay ? lead.byDate : lead.byRank) : undefined;
  const previewed = state.selected || first || items[0]?.record_id || null;
  const count = state.q
    ? status === "ready" && page
      ? page.bounded
        ? `${countLabel(page.items.length)} premier${page.items.length > 1 ? "s" : ""} résultat${page.items.length > 1 ? "s" : ""}`
        : `${countLabel(page.items.length)} résultat${page.items.length > 1 ? "s" : ""}`
      : ""
    : !predicates.length && documents !== undefined
      ? `${countLabel(documents)} document${documents > 1 ? "s" : ""}`
      : fresh?.total !== undefined
        ? // Counts go by publication date: without a range, undated documents are not counted.
          `${countLabel(fresh.total)} document${fresh.total > 1 ? "s" : ""}${state.range ? "" : fresh.total > 1 ? " datés" : " daté"}`
        : "";

  if (record)
    return (
      <main className="board explorer-board" data-record>
        <h1 className="visually-hidden">Explorer</h1>
        <RecordPage id={record} onBack={() => onRecord(null)} onUnauthorized={onUnauthorized} />
      </main>
    );

  const facetColumn = (
    <FacetColumn
      facets={facets?.fields || null}
      stale={(facets !== null && facets.key !== facetsKey) || (!facets && !facetsError)}
      error={facetsError}
      onRetry={() => setAttempt((n) => n + 1)}
      single={single}
      excluded={status === "ready" ? excluded : []}
      corpora={corpora}
      selection={state.selection}
      onToggle={toggle}
      onPick={pickPeriod}
    />
  );

  const search = (
    <form
      role="search"
      className="bar-search explorer-search"
      data-active={!!input || undefined}
      onSubmit={(event) => {
        event.preventDefault();
        const q = input.trim();
        setState((s) => (s.q === q ? s : { ...s, q, selected: null }));
      }}
    >
      <span className="bar-search-icon" aria-hidden="true">
        <MagnifyingGlass size={16} />
      </span>
      <input
        id="explorer-search"
        type="search"
        aria-label="Chercher dans les documents"
        placeholder={
          documents !== undefined
            ? `Chercher dans ${countLabel(documents)} document${documents > 1 ? "s" : ""}`
            : "Chercher dans les documents"
        }
        value={input}
        maxLength={500}
        onChange={(event) => setInput(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Escape" && input) {
            event.preventDefault();
            setInput("");
          }
        }}
      />
      {input ? (
        <button type="button" className="bar-clear" onClick={() => setInput("")}>
          Effacer
        </button>
      ) : (
        <kbd className="bar-kbd" aria-hidden="true">
          /
        </kbd>
      )}
    </form>
  );

  const switcher = corpora.length > 1 && (
    <div
      className="explorer-switch"
      role="group"
      aria-label="Corpus"
      ref={switchRef}
      data-overflow={switchOverflow || undefined}
    >
      <button
        type="button"
        aria-pressed={all}
        onClick={() => onPicked(corpora.map((c) => c.corpus_id))}
      >
        Tous
        {corpora.every((c) => c.documents !== undefined) && (
          <span className="explorer-switch-count">
            {countLabel(corpora.reduce((n, c) => n + (c.documents || 0), 0))}
          </span>
        )}
      </button>
      {corpora.map((c) => (
        <button
          key={c.corpus_id}
          type="button"
          aria-pressed={single?.corpus_id === c.corpus_id}
          onClick={() => onPicked([c.corpus_id])}
        >
          {c.name}
          {c.documents !== undefined && <span className="explorer-switch-count">{countLabel(c.documents)}</span>}
        </button>
      ))}
    </div>
  );

  return (
    <main className="board explorer-board">
      <h1 className="visually-hidden">Explorer</h1>
      <InBar to={bar.search || null}>{search}</InBar>
      {!narrow && <InBar to={bar.actions}>{switcher}</InBar>}
      <div className="explorer">
        {narrow && switcher}
        <Timeline
          facet={timeline}
          stale={!fresh && !facetsError}
          range={state.range}
          window={state.window}
          onRange={setRange}
          onWindow={(window) => setState((s) => ({ ...s, window }))}
        />
        <div className="explorer-picks" role="group" aria-label="Filtres actifs">
          <span className="explorer-count" role="status">
            {count}
            {state.q && count && <span className="explorer-query"> pour « {state.q} »</span>}
          </span>
          {state.range && (
            <button type="button" className="pick-chip" onClick={() => filter(() => ({ range: undefined }))}>
              <span className="pick-field">Période</span> {rangeLabel(state.range)}
              <X size={12} aria-hidden="true" />
              <span className="visually-hidden">, retirer ce filtre</span>
            </button>
          )}
          {picks.map(({ field, value }) => (
            <button key={`${field}:${value}`} type="button" className="pick-chip" onClick={() => toggle(field, value)}>
              <span className="pick-field">{fieldLabel(field)}</span> {valueLabel(value, types.get(field), field)}
              <X size={12} aria-hidden="true" />
              <span className="visually-hidden">, retirer ce filtre</span>
            </button>
          ))}
          {active === 0 && !state.q && (
            <span className="explorer-hint">Aucun filtre : cochez une valeur à gauche ou glissez sur la chronologie.</span>
          )}
          {active > 0 && (
            <button type="button" className="link-button explorer-clear" onClick={clearAll}>
              Tout effacer
            </button>
          )}
        </div>
        <div className="explorer-columns">
          {narrow ? (
            <details className="facets-drawer">
              <summary>Filtres{active ? ` (${active})` : ""}</summary>
              {facetColumn}
            </details>
          ) : (
            facetColumn
          )}
          <section className="explorer-list" aria-label="Documents trouvés" ref={listBox}>
            {state.q && (
              <label className="explorer-sort">
                <span className="visually-hidden">Trier</span>
                <select
                  value={state.sort}
                  onChange={(event) =>
                    setState((s) => ({ ...s, sort: event.target.value === "relevance" ? "relevance" : "recent" }))
                  }
                >
                  <option value="recent">Plus récentes</option>
                  <option value="relevance">Pertinence</option>
                </select>
              </label>
            )}
            {!state.q && <span className="explorer-sort explorer-sort-fixed">Plus récentes</span>}
            <div className="explorer-scroll" ref={scroller}>
              {status === "loading" && !page ? (
                <WireSkeleton />
              ) : status === "error" ? (
                <Notice title="Les documents ne s’affichent pas." onRetry={() => setAttempt((n) => n + 1)}>
                  {error}
                </Notice>
              ) : page && items.length === 0 && !page.next_cursor ? (
                <EmptyState
                  title={state.q || active ? "Aucun document pour ces critères." : "Aucun document pour l’instant."}
                  actions={
                    <>
                      {state.range && (
                        <button type="button" className="button" onClick={() => filter(() => ({ range: undefined, window: undefined }))}>
                          Élargir la période
                        </button>
                      )}
                      {picks.length > 0 && (
                        <button type="button" className="button" onClick={() => filter(() => ({ selection: {} }))}>
                          Retirer les filtres
                        </button>
                      )}
                      {state.q && (
                        <button type="button" className="button" onClick={() => setInput("")}>
                          Effacer la recherche
                        </button>
                      )}
                    </>
                  }
                >
                  {state.q || active
                    ? "Élargissez la période ou retirez un filtre pour voir plus de documents."
                    : "Les documents de ce corpus apparaîtront ici dès leur arrivée."}
                </EmptyState>
              ) : (
                page && (
                  <div data-pending={status === "loading" || undefined} className="explorer-rows">
                    <WireList
                      items={items}
                      byDay={byDay}
                      selected={previewed}
                      corpusOf={(item) => (picked.length > 1 ? nameOf(item.corpus_id) : "")}
                      onSelect={select}
                      onOpen={(id) => onRecord(id)}
                    />
                    {page.next_cursor ? (
                      <div className="explorer-more" ref={sentinel}>
                        {more === "error" ? (
                          <>
                            <p className="error-text" role="alert">
                              La suite ne s’affiche pas.
                            </p>
                            <button type="button" className="button" onClick={loadMore}>
                              Réessayer
                            </button>
                          </>
                        ) : (
                          <button type="button" className="link-button" disabled={more === "loading"} onClick={loadMore}>
                            {more === "loading" ? "Chargement…" : "Afficher plus de documents"}
                          </button>
                        )}
                      </div>
                    ) : (
                      items.length > 0 && <p className="explorer-end">Fin de la liste · {countLabel(items.length)} document{items.length > 1 ? "s" : ""}</p>
                    )}
                  </div>
                )
              )}
            </div>
          </section>
          {!narrow && (
            <aside className="explorer-preview" aria-label="Aperçu">
              {previewed ? (
                <Preview id={previewed} onOpen={() => onRecord(previewed)} onUnauthorized={onUnauthorized} />
              ) : (
                <p className="facets-note">
                  {status === "ready" ? "Choisissez un document pour le lire ici." : ""}
                </p>
              )}
            </aside>
          )}
        </div>
      </div>
    </main>
  );
}

/** One exclusion per corpus, with every field it lacks. */
function mergeExclusions(list: Exclusion[]) {
  const out = new Map<string, Set<string>>();
  for (const e of list) out.set(e.corpus_id, new Set([...(out.get(e.corpus_id) || []), ...e.fields]));
  return [...out].map(([corpus_id, fields]) => ({ corpus_id, fields: [...fields] }));
}
