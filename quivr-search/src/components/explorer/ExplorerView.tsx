import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { APIError } from "../../lib/search";
import {
  corpusNames,
  exclusionNotice,
  fieldLabel,
  type Corpus,
  type Exclusion,
  type Field,
} from "../../lib/corpora";
import {
  countLabel,
  fetchExplore,
  fetchFacets,
  periodLabel,
  predicatesOf,
  valueLabel,
  type ExploreItem,
  type ExplorePage,
  type Facet,
  type Facets,
  type Interval,
  type Scalar,
  type Selection,
} from "../../lib/explore";
import { formatAbsolute } from "../../lib/connectors";
import { displayName } from "../../lib/sourceNames";
import { X } from "@phosphor-icons/react";
import { CorpusIcon } from "../RailIcons";
import { EmptyState, LoadingState, Notice } from "../ui";
import { RecordPage } from "./RecordPage";
import "../../explorer.css";

// A facet shows this many values, then offers the rest this many at a time.
const SHOWN_VALUES = 6;
const MORE_VALUES = 20;
// The fields a row shows under its title, when it has them.
const ROW_FIELDS = ["metadata.language", "metadata.subjects", "metadata.tags", "metadata.place"];

/**
 * The Explorer (THE-1171): the documents of the corpora picked, newest
 * first, narrowed by facets. The common fields apply to every corpus; a
 * corpus's own fields show when it is picked alone. Each value says how many
 * documents have it under the other filters, and a date is a histogram that
 * zooms into the period picked (THE-1184). A filter on a field some picked
 * corpora lack leaves them out, and a notice says which, as the engine
 * reports it. A document opens on its own page.
 */
export function ExplorerView({
  corpora,
  picked,
  onPicked,
  record,
  onRecord,
  onUnauthorized,
}: {
  corpora: Corpus[];
  picked: string[];
  onPicked: (ids: string[]) => void;
  record: string | null;
  onRecord: (id: string | null) => void;
  onUnauthorized: () => void;
}) {
  const [selection, setSelection] = useState<Selection>({});
  const [page, setPage] = useState<ExplorePage | null>(null);
  const [status, setStatus] = useState<"loading" | "ready" | "error">("loading");
  const [error, setError] = useState("");
  const [more, setMore] = useState<"idle" | "loading" | "error">("idle");
  // The counts, with the corpora and filters they were read for.
  const [facets, setFacets] = useState<(Facets & { key: string }) | null>(null);
  const [facetsError, setFacetsError] = useState("");
  const [attempt, setAttempt] = useState(0);

  // Every field of the corpora read, for its type: the picked corpora's
  // first, so a name two corpora share takes the type of the one browsed.
  const fields = useMemo(() => {
    const out = new Map<string, Field>();
    const order = [...corpora].sort(
      (a, b) => Number(!picked.includes(a.corpus_id)) - Number(!picked.includes(b.corpus_id)),
    );
    for (const c of order) for (const f of [...c.common, ...c.own]) if (!out.has(f.name)) out.set(f.name, f);
    return out;
  }, [corpora, picked]);
  const types = useMemo(
    () => new Map([...fields].map(([name, f]) => [name, f.type])),
    [fields],
  );
  const predicates = useMemo(() => predicatesOf(selection, types), [selection, types]);
  const key = JSON.stringify([picked, predicates]);
  // The list's current reads: a filter or corpus change aborts the page of
  // "more" still on its way, which belongs to the list it leaves.
  const reads = useRef<AbortController | null>(null);
  // The corpora the facets were read for: others picked, their fields go at once.
  const facetsFor = useRef("");

  useEffect(() => {
    if (record) return;
    const controller = new AbortController();
    reads.current = controller;
    setStatus("loading");
    setMore("idle");
    const corporaKey = picked.join(",");
    if (facetsFor.current !== corporaKey) {
      facetsFor.current = corporaKey;
      setFacets(null);
    }
    fetchExplore(picked, predicates, undefined, controller.signal)
      .then((data) => {
        setPage(data);
        setStatus("ready");
      })
      .catch((e) => {
        if (controller.signal.aborted) return;
        if (e instanceof APIError && e.status === 401) return onUnauthorized();
        setError(e instanceof Error ? e.message : "Les documents ne s’affichent pas.");
        setStatus("error");
      });
    setFacetsError("");
    fetchFacets(picked, predicates, controller.signal)
      .then((data) => setFacets({ ...data, key }))
      .catch((e) => {
        if (controller.signal.aborted) return;
        if (e instanceof APIError && e.status === 401) return onUnauthorized();
        // The list still shows; the facets keep their last values.
        setFacetsError(e instanceof Error ? e.message : "Les nombres ne s’affichent pas.");
      });
    return () => controller.abort();
    // The key holds the corpora and the predicates.
  }, [key, attempt, record, onUnauthorized]);

  const loadMore = () => {
    const controller = reads.current;
    if (!page?.next_cursor || more === "loading" || !controller) return;
    setMore("loading");
    fetchExplore(picked, predicates, page.next_cursor, controller.signal)
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

  const toggle = (field: string, value: Scalar) =>
    setSelection((current) => {
      const values = current[field] || [];
      // A date picks one period at a time; picked again, it widens to the
      // period around it (a day to its month, a month to its year).
      const date = types.get(field) === "datetime";
      const next = date
        ? values.includes(value)
          ? widen(String(value))
          : [value]
        : values.includes(value)
          ? values.filter((v) => v !== value)
          : [...values, value];
      const out = { ...current, [field]: next };
      if (!next.length) delete out[field];
      return out;
    });

  // An active filter's chip removes it, a date's period included.
  const remove = (field: string, value: Scalar) =>
    setSelection((current) => {
      const next = (current[field] || []).filter((v) => v !== value);
      const out = { ...current, [field]: next };
      if (!next.length) delete out[field];
      return out;
    });

  const nameOf = (id?: string) => corpora.find((c) => c.corpus_id === id)?.name || id || "";
  const single = picked.length === 1 ? corpora.find((c) => c.corpus_id === picked[0]) : undefined;
  const facetList = facets?.fields || [];
  const common = facetList.filter((f) => f.field.startsWith("metadata."));
  const own = facetList.filter((f) => !f.field.startsWith("metadata."));
  const picks = Object.entries(selection).flatMap(([field, values]) =>
    values.map((value) => ({ field, value })),
  );
  // The corpora left out, as the list and the counts of the same filters report them.
  const excluded = mergeExclusions([
    ...(page?.excluded_corpora || []),
    ...(facets?.key === key ? facets.excluded_corpora || [] : []),
  ]);
  const facetBox = (facet: Facet) =>
    facet.interval ? (
      <DateFacet
        key={facet.field}
        facet={facet}
        picked={selection[facet.field]?.[0]}
        onPick={(value) =>
          setSelection((current) => {
            const out = { ...current, [facet.field]: value === undefined ? [] : [value] };
            if (value === undefined) delete out[facet.field];
            return out;
          })
        }
        onToggle={(value) => toggle(facet.field, value)}
      />
    ) : (
      <FacetBox
        key={facet.field}
        facet={facet}
        picked={selection[facet.field] || []}
        onToggle={(value) => toggle(facet.field, value)}
      />
    );

  const corpusBar = (
    <div className="explorer-corpora" role="group" aria-label="Corpus explorés">
      <CorpusIcon size={16} />
      {corpora.map((c) => (
        <button
          key={c.corpus_id}
          type="button"
          className="chip"
          aria-pressed={picked.includes(c.corpus_id)}
          onClick={() => {
            const next = picked.includes(c.corpus_id)
              ? picked.filter((id) => id !== c.corpus_id)
              : [...picked, c.corpus_id];
            if (next.length) onPicked(next);
          }}
        >
          {c.name}
        </button>
      ))}
    </div>
  );

  if (record)
    return (
      <main className="board explorer-board">
        <h1 className="visually-hidden">Explorer</h1>
        <RecordPage id={record} onBack={() => onRecord(null)} onUnauthorized={onUnauthorized} />
      </main>
    );

  return (
    <main className="board explorer-board">
      <h1 className="visually-hidden">Explorer</h1>
      <div className="explorer">
        {corpusBar}
        <div className="explorer-body">
          <aside
            className="facets"
            aria-label="Filtres"
            aria-busy={(facets?.key !== key && !facetsError) || undefined}
            data-stale={(facets !== null && facets.key !== key) || undefined}
          >
            {facetsError && (
              <p className="facets-note" role="status">
                {facetsError}{" "}
                <button type="button" className="link-button" onClick={() => setAttempt((n) => n + 1)}>
                  Réessayer
                </button>
              </p>
            )}
            {!facets && !facetsError && <LoadingState label="Comptage des valeurs…" rows={4} />}
            {common.map(facetBox)}
            {single && own.length > 0 && (
              <>
                <h2 className="facets-corpus">Champs de {single.name}</h2>
                {own.map(facetBox)}
              </>
            )}
          </aside>
          <section className="panel explorer-list" aria-labelledby="explorer-title">
            <div className="panel-head">
              <h2 id="explorer-title">
                {single ? single.name : corpusNames(picked, corpora)}
              </h2>
              <span className="list-count">Les plus récents d’abord</span>
              {picks.length > 0 && (
                <div className="explorer-picks" role="group" aria-label="Filtres actifs">
                  {picks.map(({ field, value }) => (
                    <button
                      key={`${field}:${value}`}
                      type="button"
                      className="pick-chip"
                      onClick={() => remove(field, value)}
                    >
                      {fieldLabel(field)} : {valueLabel(value, types.get(field), field)}
                      <X size={13} aria-hidden="true" />
                      <span className="visually-hidden">, retirer ce filtre</span>
                    </button>
                  ))}
                  <button type="button" className="link-button" onClick={() => setSelection({})}>
                    Tout effacer
                  </button>
                </div>
              )}
            </div>
            {excluded.length > 0 && status === "ready" && (
              <p className="exclusion-note" role="note">
                {exclusionNotice(excluded, corpora)}
              </p>
            )}
            {status === "loading" && !page ? (
              <LoadingState label="Chargement des documents…" rows={5} />
            ) : status === "error" ? (
              <Notice title="Les documents ne s’affichent pas." onRetry={() => setAttempt((n) => n + 1)}>
                {error}
              </Notice>
            ) : page && page.items.length === 0 && !page.next_cursor ? (
              <EmptyState title={picks.length ? "Aucun document pour ces filtres." : "Aucun document pour l’instant."}>
                {picks.length
                  ? "Retirez un filtre pour élargir la liste."
                  : "Les documents de ce corpus apparaîtront ici dès leur arrivée."}
              </EmptyState>
            ) : (
              page && (
                <>
                  <ol
                    className="explorer-rows"
                    aria-label="Documents"
                    data-pending={status === "loading" || undefined}
                  >
                    {page.items.map((item) => (
                      <ExploreRow
                        key={item.record_id}
                        item={item}
                        corpus={picked.length > 1 ? nameOf(item.corpus_id) : ""}
                        types={types}
                        onOpen={() => onRecord(item.record_id)}
                      />
                    ))}
                  </ol>
                  {page.next_cursor && (
                    <div className="explorer-more">
                      <button
                        type="button"
                        className="button"
                        disabled={more === "loading"}
                        onClick={loadMore}
                      >
                        {more === "loading" ? "Chargement…" : "Afficher plus de documents"}
                      </button>
                      {more === "error" && (
                        <p className="error-text" role="alert">
                          La suite ne s’affiche pas. Réessayez.
                        </p>
                      )}
                    </div>
                  )}
                </>
              )
            )}
          </section>
        </div>
      </div>
    </main>
  );
}

/** One facet: its values to tick, the first few then the rest on demand. */
function FacetBox({
  facet,
  picked,
  onToggle,
}: {
  facet: Facet;
  picked: Scalar[];
  onToggle: (value: Scalar) => void;
}) {
  const [limit, setLimit] = useState(SHOWN_VALUES);
  // A value picked stays offered even when no document counted has it.
  const values: { value: Scalar; count?: number }[] = [
    ...picked.filter((v) => !facet.values.some((x) => x.value === v)).map((value) => ({ value })),
    ...facet.values,
  ];
  if (!values.length) return null;
  const shown = values.slice(0, limit);
  const rest = values.length - shown.length;
  const label = fieldLabel(facet.field);
  return (
    <section className="facet" aria-label={label}>
      <h3>{label}</h3>
      <ul>
        {shown.map(({ value, count }) => (
          <li key={String(value)}>
            <button
              type="button"
              className="facet-value"
              aria-pressed={picked.includes(value)}
              onClick={() => onToggle(value)}
            >
              <span className="menu-check" data-single={facet.type === "datetime" || undefined} aria-hidden="true" />
              <span className="facet-label">{valueLabel(value, facet.type, facet.field)}</span>
              {count !== undefined && <span className="menu-count">{countLabel(count)}</span>}
            </button>
          </li>
        ))}
      </ul>
      {rest > 0 ? (
        <button type="button" className="link-button" onClick={() => setLimit(limit + MORE_VALUES)}>
          {Math.min(rest, MORE_VALUES)} de plus
        </button>
      ) : (
        values.length > SHOWN_VALUES && (
          <button type="button" className="link-button" onClick={() => setLimit(SHOWN_VALUES)}>
            Moins de valeurs
          </button>
        )
      )}
    </section>
  );
}

/** The period around a picked one: a day's month, a month's year, nothing around a year. */
const widen = (period: string): Scalar[] => (period.length > 4 ? [period.slice(0, period.length - 3)] : []);

/** One exclusion per corpus, with every field it lacks. */
function mergeExclusions(list: Exclusion[]) {
  const out = new Map<string, Set<string>>();
  for (const e of list) out.set(e.corpus_id, new Set([...(out.get(e.corpus_id) || []), ...e.fields]));
  return [...out].map(([corpus_id, fields]) => ({ corpus_id, fields: [...fields] }));
}

// Beyond this many periods, the bars are those counted, without the gaps.
const MAX_BARS = 400;

/**
 * The periods between two, both included, at one step: "2026-09",
 * "2026-10"…; undefined when there would be more than MAX_BARS.
 */
function periodsBetween(first: string, last: string, interval: Interval) {
  const out: string[] = [];
  const at = new Date(`${first.padEnd(10, "-01").slice(0, 10)}T00:00:00Z`);
  const length = first.length;
  for (;;) {
    const period = at.toISOString().slice(0, length);
    out.push(period);
    if (period >= last) return out;
    if (out.length >= MAX_BARS) return undefined;
    if (interval === "year") at.setUTCFullYear(at.getUTCFullYear() + 1);
    else if (interval === "month") at.setUTCMonth(at.getUTCMonth() + 1);
    else at.setUTCDate(at.getUTCDate() + 1);
  }
}

const STEP: Record<Interval, string> = {
  year: "Documents par année",
  month: "Documents par mois",
  day: "Documents par jour",
};

/**
 * A date field: how many documents each period has, as bars in time order,
 * empty periods left as gaps. A bar picks its period; the counts then zoom in
 * (a year shows its months, a month its days), and the path above widens back.
 */
function DateFacet({
  facet,
  picked,
  onPick,
  onToggle,
}: {
  facet: Facet;
  picked?: Scalar;
  onPick: (period: string | undefined) => void;
  onToggle: (value: Scalar) => void;
}) {
  const [focused, setFocused] = useState<string>();
  // A pick from here zooms the bars once the new counts arrive: focus, if it
  // fell to the page meanwhile, goes to the new tab stop or to the period
  // picked. Focus taken elsewhere cancels it.
  const section = useRef<HTMLElement>(null);
  const refocus = useRef(false);
  useEffect(() => {
    const here = section.current;
    if (!refocus.current || !here) return;
    refocus.current = false;
    if (document.activeElement && document.activeElement !== document.body) return;
    (
      here.querySelector<HTMLElement>('.facet-histogram button[tabindex="0"]') ||
      here.querySelector<HTMLElement>("[aria-current]")
    )?.focus();
  }, [facet]);
  const interval = facet.interval!;
  const counts = new Map(facet.values.map(({ value, count }) => [String(value), count]));
  const periods = facet.values.length
    ? periodsBetween(String(facet.values[0].value), String(facet.values.at(-1)!.value), interval) ||
      facet.values.map(({ value }) => String(value))
    : [];
  const most = Math.max(1, ...counts.values());
  const label = fieldLabel(facet.field);
  const period = typeof picked === "string" ? picked : undefined;
  // The path to the period picked: every date, its year, its month.
  const path = period ? [4, 7, 10].filter((n) => n <= period.length).map((n) => period.slice(0, n)) : [];
  // One stop in the tab order: the bar last focused, else the period picked
  // or the latest; ← → Début Fin move along the bars that have documents.
  const focusable = periods.filter((p) => counts.get(p));
  const current =
    focused && focusable.includes(focused)
      ? focused
      : period && focusable.includes(period)
        ? period
        : focusable.at(-1);
  const move = (event: KeyboardEvent<HTMLDivElement>) => {
    const bars = [...event.currentTarget.querySelectorAll<HTMLButtonElement>("button:not(:disabled)")];
    const at = bars.indexOf(document.activeElement as HTMLButtonElement);
    const to =
      event.key === "ArrowLeft" ? at - 1
      : event.key === "ArrowRight" ? at + 1
      : event.key === "Home" ? 0
      : event.key === "End" ? bars.length - 1
      : undefined;
    if (to === undefined || at < 0) return;
    event.preventDefault();
    bars[Math.max(0, Math.min(bars.length - 1, to))]?.focus();
  };
  if (!periods.length && !period) return null;
  return (
    <section
      className="facet facet-dates"
      aria-label={label}
      ref={section}
      onBlur={(event) => {
        if (event.relatedTarget && !event.currentTarget.contains(event.relatedTarget)) refocus.current = false;
      }}
    >
      <h3>{label}</h3>
      {period && (
        <ol className="facet-path" aria-label="Période choisie">
          <li>
            <button
              type="button"
              className="link-button"
              onClick={() => {
                refocus.current = true;
                onPick(undefined);
              }}
            >
              Toutes les dates
            </button>
          </li>
          {path.map((p) => (
            <li key={p}>
              {p === period ? (
                <span aria-current="true" tabIndex={-1}>
                  {periodLabel(p)}
                </span>
              ) : (
                <button
                  type="button"
                  className="link-button"
                  onClick={() => {
                    refocus.current = true;
                    onPick(p);
                  }}
                >
                  {periodLabel(p)}
                </button>
              )}
            </li>
          ))}
        </ol>
      )}
      {periods.length > 0 ? (
        <>
          <div
            className="pulse facet-histogram"
            role="group"
            data-tips
            aria-label={STEP[interval]}
            onKeyDown={move}
          >
            {periods.map((p) => {
              const count = counts.get(p) || 0;
              const documents = `${countLabel(count)} ${count > 1 ? "documents" : "document"}`;
              return (
                <button
                  key={p}
                  type="button"
                  className="pulse-column"
                  aria-pressed={p === period}
                  tabIndex={p === current ? 0 : -1}
                  onFocus={() => setFocused(p)}
                  aria-label={`${periodLabel(p)} : ${documents}`}
                  data-tip={`${periodLabel(p)} · ${documents}`}
                  disabled={count === 0}
                  onClick={() => {
                    refocus.current = true;
                    onToggle(p);
                  }}
                >
                  <span
                    className="pulse-bar"
                    data-empty={count === 0 || undefined}
                    style={count ? { height: `${Math.max(8, (count / most) * 100)}%` } : undefined}
                  />
                </button>
              );
            })}
          </div>
          <div className="pulse-axis" aria-hidden="true">
            <span>{periodLabel(periods[0])}</span>
            {periods.length > 1 && <span>{periodLabel(periods.at(-1)!)}</span>}
          </div>
        </>
      ) : (
        <p className="facets-note">Aucun document daté dans cette période.</p>
      )}
    </section>
  );
}

function ExploreRow({
  item,
  corpus,
  types,
  onOpen,
}: {
  item: ExploreItem;
  corpus: string;
  types: Map<string, Field["type"]>;
  onOpen: () => void;
}) {
  const at = item.received_at;
  const tags = ROW_FIELDS.flatMap((field) =>
    (item.metadata[field] || []).slice(0, 3).map((value) => ({ field, value })),
  ).slice(0, 5);
  return (
    <li className="explorer-row">
      <h3>
        <a
          href={`?view=explorer&record=${encodeURIComponent(item.record_id)}`}
          onClick={(event) => {
            if (event.metaKey || event.ctrlKey || event.shiftKey) return;
            event.preventDefault();
            onOpen();
          }}
        >
          {item.title || "Sans titre"}
        </a>
      </h3>
      <div className="row-meta">
        {corpus && <span className="row-corpus">{corpus}</span>}
        {item.namespace && <span className="row-source">{displayName(item.namespace)}</span>}
        {at && (
          <time className="row-when" dateTime={at}>
            {formatAbsolute(at)}
          </time>
        )}
      </div>
      {item.excerpt && <p className="explorer-excerpt">{item.excerpt}</p>}
      {tags.length > 0 && (
        <ul className="explorer-tags" aria-label="Métadonnées">
          {tags.map(({ field, value }) => (
            <li key={`${field}:${value}`} title={fieldLabel(field)}>
              {valueLabel(value, types.get(field), field)}
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}
