import { useEffect, useRef, useState, type CSSProperties, type KeyboardEvent } from "react";
import { WarningCircle } from "@phosphor-icons/react";
import { exclusionNotice, fieldLabel, type Corpus, type Exclusion } from "../../lib/corpora";
import {
  countLabel,
  periodLabel,
  periodsBetween,
  TIMELINE_FIELD,
  valueLabel,
  type Facet,
  type Interval,
  type Scalar,
} from "../../lib/explore";
import { LoadingState } from "../ui";

// A facet shows this many values, then offers the rest this many at a time.
const SHOWN_VALUES = 6;
const MORE_VALUES = 20;
// The common fields shown first, in this order; the others wait in a group.
const FIRST = ["metadata.language", "metadata.subjects", "metadata.country"];

/**
 * The filters column (THE-1204): Langue, Sujets, Pays, then the corpus's own
 * fields when one corpus is picked, then the other common fields. Each value
 * says how many documents have it, with a bar against the largest. A notice
 * names the corpora a filter left out.
 */
export function FacetColumn({
  facets,
  stale,
  error,
  onRetry,
  single,
  excluded,
  corpora,
  selection,
  onToggle,
  onPick,
}: {
  facets: Facet[] | null;
  stale: boolean;
  error: string;
  onRetry: () => void;
  single?: Corpus;
  excluded: Exclusion[];
  corpora: Corpus[];
  selection: Record<string, string[]>;
  onToggle: (field: string, value: string) => void;
  onPick: (field: string, period: string | undefined) => void;
}) {
  const list = (facets || []).filter((f) => f.field !== TIMELINE_FIELD);
  const common = list.filter((f) => f.field.startsWith("metadata."));
  const first = FIRST.map((name) => common.find((f) => f.field === name)).filter((f): f is Facet => !!f);
  const rest = common.filter((f) => !FIRST.includes(f.field));
  const own = single ? list.filter((f) => !f.field.startsWith("metadata.")) : [];
  const box = (facet: Facet) =>
    facet.interval ? (
      <DateFacet
        key={facet.field}
        facet={facet}
        picked={selection[facet.field]?.[0]}
        onPick={(period) => onPick(facet.field, period)}
      />
    ) : (
      <FacetBox
        key={facet.field}
        facet={facet}
        picked={selection[facet.field] || []}
        onToggle={(value) => onToggle(facet.field, value)}
      />
    );
  return (
    <aside className="facets" aria-label="Filtres" aria-busy={stale || undefined} data-stale={stale || undefined}>
      {excluded.length > 0 && (
        <p className="facets-warning" role="note">
          <WarningCircle size={16} aria-hidden="true" />
          <span>{exclusionNotice(excluded, corpora)}</span>
        </p>
      )}
      {error && (
        <p className="facets-note" role="status">
          {error}{" "}
          <button type="button" className="link-button" onClick={onRetry}>
            Réessayer
          </button>
        </p>
      )}
      {!facets && !error && <LoadingState label="Comptage des valeurs…" rows={4} />}
      {first.map(box)}
      {own.length > 0 && (
        <>
          <h2 className="facets-corpus">Champs de {single!.name}</h2>
          {own.map(box)}
        </>
      )}
      {rest.some((f) => f.values.length || selection[f.field]?.length) && (
        <details className="facets-more" open={rest.some((f) => selection[f.field]?.length) || undefined}>
          <summary>Autres champs</summary>
          {rest.map(box)}
        </details>
      )}
    </aside>
  );
}

/** One facet: its values to tick, with their counts and share, the first few then the rest on demand. */
function FacetBox({
  facet,
  picked,
  onToggle,
}: {
  facet: Facet;
  picked: string[];
  onToggle: (value: string) => void;
}) {
  const [limit, setLimit] = useState(SHOWN_VALUES);
  const box = useRef<HTMLElement>(null);
  // A value picked stays offered even when no document counted has it.
  const values: { value: Scalar; count?: number }[] = [
    ...picked.filter((v) => !facet.values.some((x) => String(x.value) === v)).map((value) => ({ value })),
    ...facet.values,
  ];
  if (!values.length) return null;
  const most = Math.max(1, ...facet.values.map((v) => v.count));
  const shown = values.slice(0, limit);
  const rest = values.length - shown.length;
  const label = fieldLabel(facet.field);
  return (
    <section className="facet" aria-label={label} ref={box}>
      <div className="facet-head">
        <h3>{label}</h3>
        {picked.length > 0 && (
          <button
            type="button"
            className="facet-clear"
            onClick={() => {
              picked.forEach(onToggle);
              // The button goes with the picks: the focus moves to the values.
              box.current?.querySelector<HTMLElement>(".facet-value")?.focus();
            }}
          >
            Effacer<span className="visually-hidden"> {label}</span>
          </button>
        )}
      </div>
      <ul>
        {shown.map(({ value, count }) => (
          <li key={String(value)}>
            <button
              type="button"
              className="facet-value"
              aria-pressed={picked.includes(String(value))}
              onClick={() => onToggle(String(value))}
            >
              <span className="menu-check" aria-hidden="true" />
              <span className="facet-label" dir="auto">
                {valueLabel(value, facet.type, facet.field)}
              </span>
              {count !== undefined && <span className="menu-count">{countLabel(count)}</span>}
              {count !== undefined && (
                <span
                  className="facet-share"
                  aria-hidden="true"
                  style={{ "--share": `${Math.max(2, (count / most) * 100)}%` } as CSSProperties}
                />
              )}
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

const STEP: Record<Interval, string> = {
  year: "Documents par année",
  month: "Documents par mois",
  day: "Documents par jour",
};

/** The period around a picked one: a day's month, a month's year, nothing around a year. */
const widen = (period: string) => (period.length > 4 ? period.slice(0, period.length - 3) : undefined);

/**
 * A corpus's own date field: how many documents each period has, as bars in
 * time order. A bar picks its period; the counts then zoom in (a year shows
 * its months, a month its days), and the path above widens back.
 */
function DateFacet({
  facet,
  picked,
  onPick,
}: {
  facet: Facet;
  picked?: string;
  onPick: (period: string | undefined) => void;
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
  // The path to the period picked: every date, its year, its month.
  const path = picked ? [4, 7, 10].filter((n) => n <= picked.length).map((n) => picked.slice(0, n)) : [];
  const focusable = periods.filter((p) => counts.get(p));
  const current =
    focused && focusable.includes(focused)
      ? focused
      : picked && focusable.includes(picked)
        ? picked
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
  const pick = (period: string | undefined) => {
    refocus.current = true;
    onPick(period);
  };
  if (!periods.length && !picked) return null;
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
      {picked && (
        <ol className="facet-path" aria-label="Période choisie">
          <li>
            <button type="button" className="link-button" onClick={() => pick(undefined)}>
              Toutes les dates
            </button>
          </li>
          {path.map((p) => (
            <li key={p}>
              {p === picked ? (
                <span aria-current="true" tabIndex={-1}>
                  {periodLabel(p)}
                </span>
              ) : (
                <button type="button" className="link-button" onClick={() => pick(p)}>
                  {periodLabel(p)}
                </button>
              )}
            </li>
          ))}
        </ol>
      )}
      {periods.length > 0 ? (
        <>
          <div className="pulse facet-histogram" role="group" data-tips aria-label={STEP[interval]} onKeyDown={move}>
            {periods.map((p) => {
              const count = counts.get(p) || 0;
              const documents = `${countLabel(count)} ${count > 1 ? "documents" : "document"}`;
              return (
                <button
                  key={p}
                  type="button"
                  className="pulse-column"
                  aria-pressed={p === picked}
                  tabIndex={p === current ? 0 : -1}
                  onFocus={() => setFocused(p)}
                  aria-label={`${periodLabel(p)} : ${documents}`}
                  data-tip={`${periodLabel(p)} · ${documents}`}
                  disabled={count === 0}
                  onClick={() => pick(p === picked ? widen(p) : p)}
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
