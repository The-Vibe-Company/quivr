import { useEffect, useMemo, useRef, useState } from "react";
import { APIError } from "../../lib/search";
import {
  corpusNames,
  exclusionNotice,
  fieldLabel,
  type Corpus,
  type Field,
} from "../../lib/corpora";
import {
  fetchExplore,
  fetchFacets,
  predicatesOf,
  valueLabel,
  type ExploreItem,
  type ExplorePage,
  type Facet,
  type Facets,
  type Scalar,
  type Selection,
} from "../../lib/explore";
import { formatAbsolute } from "../../lib/connectors";
import { plural } from "../../lib/format";
import { displayName } from "../../lib/sourceNames";
import { X } from "@phosphor-icons/react";
import { CorpusIcon } from "../RailIcons";
import { EmptyState, LoadingState, Notice } from "../ui";
import { RecordPage } from "./RecordPage";
import "../../explorer.css";

// A facet shows this many values, then offers the rest.
const SHOWN_VALUES = 6;
// The fields a row shows under its title, when it has them.
const ROW_FIELDS = ["metadata.language", "metadata.subjects", "metadata.tags", "metadata.place"];

/**
 * The Explorer (THE-1171): the documents of the corpora picked, newest
 * first, narrowed by facets. The common fields apply to every corpus; a
 * corpus's own fields show when it is picked alone. A filter on a field some
 * picked corpora lack leaves them out, and a notice says which, as the
 * engine reports it. A document opens on its own page.
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
  const [facets, setFacets] = useState<Facets | null>(null);
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
    fetchFacets(picked, predicates, controller.signal)
      .then(setFacets)
      .catch(() => {
        // The list still shows; the facets keep their last values.
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
      // A date picks one month at a time.
      const next = values.includes(value)
        ? values.filter((v) => v !== value)
        : types.get(field) === "datetime"
          ? [value]
          : [...values, value];
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
  const excluded = page?.excluded_corpora || [];

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
          <aside className="facets" aria-label="Filtres">
            {common.map((facet) => (
              <FacetBox
                key={facet.field}
                facet={facet}
                picked={selection[facet.field] || []}
                onToggle={(value) => toggle(facet.field, value)}
              />
            ))}
            {single && own.length > 0 && (
              <>
                <h2 className="facets-corpus">Champs de {single.name}</h2>
                {own.map((facet) => (
                  <FacetBox
                    key={facet.field}
                    facet={facet}
                    picked={selection[facet.field] || []}
                    onToggle={(value) => toggle(facet.field, value)}
                  />
                ))}
              </>
            )}
            {facets && !facets.counted && (
              <p className="facets-note">
                Valeurs lues dans {plural(facets.sample, "document récent", "documents récents")} ;
                les nombres viendront quand Quivr comptera chaque valeur.
              </p>
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
                      onClick={() => toggle(field, value)}
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
  const [open, setOpen] = useState(false);
  // A value picked stays offered even when the newest documents lack it.
  const values = [
    ...picked.filter((v) => !facet.values.some((x) => x.value === v)).map((value) => ({ value, count: undefined })),
    ...facet.values,
  ];
  if (!values.length) return null;
  const shown = open ? values : values.slice(0, SHOWN_VALUES);
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
              {count !== undefined && <span className="menu-count">{count}</span>}
            </button>
          </li>
        ))}
      </ul>
      {values.length > SHOWN_VALUES && (
        <button type="button" className="link-button" onClick={() => setOpen(!open)}>
          {open ? "Moins de valeurs" : `${values.length - SHOWN_VALUES} de plus`}
        </button>
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
