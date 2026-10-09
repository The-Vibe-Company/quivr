import { useEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from "react";
import { WarningCircle } from "@phosphor-icons/react";
import { exclusionNotice, fieldLabel, type Corpus, type Exclusion } from "../../lib/corpora";
import {
  ageLabel,
  earliest,
  estimateLabel,
  periodLabel,
  periodsBetween,
  valueLabel,
  type Facet,
  type Interval,
  type Scalar,
} from "../../lib/explore";
import { LoadingState } from "../ui";
import { useNow, type FieldCount } from "./useFacetCounts";

// A facet shows this many values, then offers the rest this many at a time.
const SHOWN_VALUES = 6;
const MORE_VALUES = 20;
// The common fields shown first, in this order; the others wait in a group.
export const FIRST = ["metadata.language", "metadata.subjects", "metadata.country"];

/**
 * The filters column (THE-1204): Langue, Sujets, Pays, then the corpus's own
 * fields when one corpus is picked, then the other common fields, counted
 * once opened. Each field arrives on its own (THE-1387). Each value says how
 * many documents have it, with a bar against the largest; an estimate reads
 * "≈" until its exact count arrives, and stored counts say their age. A
 * notice names the corpora a filter left out.
 */
export function FacetColumn({
  counts,
  current,
  fields,
  more,
  onMore,
  onRetry,
  single,
  excluded,
  corpora,
  selection,
  onToggle,
  onPick,
}: {
  /** Each field's counts, as last read. */
  counts: Map<string, FieldCount>;
  /** The corpora, filters and span the counts are wanted for. */
  current: string;
  /** The corpus's own fields, and the common fields after the first ones. */
  fields: { own: string[]; rest: string[] };
  /** Whether the other common fields are counted. */
  more: boolean;
  /** The other common fields were opened: count them. */
  onMore: () => void;
  onRetry: () => void;
  single?: Corpus;
  excluded: Exclusion[];
  corpora: Corpus[];
  selection: Record<string, string[]>;
  onToggle: (field: string, value: string) => void;
  onPick: (field: string, period: string | undefined) => void;
}) {
  // The oldest stored counts shown date the column, read again as time passes.
  const asOf = earliest([...counts.values()].filter((c) => c.key === current && c.as_of).map((c) => c.as_of!));
  const now = useNow(!!asOf);
  const box = (field: string) => {
    const count = counts.get(field);
    const facet = count?.fields?.[0];
    const label = fieldLabel(field);
    if (!facet)
      return count?.error ? (
        <section key={field} className="facet" aria-label={label}>
          <h3>{label}</h3>
          <p className="facets-note" role="status">
            {count.error}{" "}
            <button type="button" className="link-button" onClick={onRetry}>
              Réessayer<span className="visually-hidden"> {label}</span>
            </button>
          </p>
        </section>
      ) : (
        <section key={field} className="facet" aria-label={label} aria-busy="true">
          <h3>{label}</h3>
          <LoadingState rows={1} />
        </section>
      );
    const shown = {
      facet,
      stale: count.key !== current,
      approximate: count.approximate,
      refining: count.refining,
      error: count.error || count.refineError,
      onRetry,
    };
    return facet.interval ? (
      <DateFacet
        key={field}
        {...shown}
        picked={selection[field]?.[0]}
        onPick={(period) => onPick(field, period)}
      />
    ) : (
      <FacetBox
        key={field}
        {...shown}
        picked={selection[field] || []}
        onToggle={(value) => onToggle(field, value)}
      />
    );
  };
  return (
    <aside className="facets" aria-label="Filtres" tabIndex={-1}>
      {excluded.length > 0 && (
        <p className="facets-warning" role="note">
          <WarningCircle size={16} aria-hidden="true" />
          <span>{exclusionNotice(excluded, corpora)}</span>
        </p>
      )}
      {asOf && ageLabel(asOf, now) && (
        <p className="facets-note facets-age" title={new Date(asOf).toLocaleString("fr-FR")}>
          Nombres comptés {ageLabel(asOf, now)}
        </p>
      )}
      {FIRST.map(box)}
      {single && fields.own.length > 0 && (
        <>
          <h2 className="facets-corpus">Champs de {single.name}</h2>
          {fields.own.map(box)}
        </>
      )}
      {fields.rest.length > 0 && (
        <details
          className="facets-more"
          open={fields.rest.some((field) => selection[field]?.length) || undefined}
          onToggle={(event) => event.currentTarget.open && onMore()}
        >
          <summary>Autres champs</summary>
          {more && fields.rest.map(box)}
          {more &&
            fields.rest.every((field) => counts.get(field)?.fields?.[0]?.values.length === 0 && !selection[field]?.length) && (
              <p className="facets-note">Aucune valeur pour ces filtres.</p>
            )}
        </details>
      )}
    </aside>
  );
}

/** How a facet's counts were read, for its box. */
interface Shown {
  facet: Facet;
  /** Counts read for other filters, while new ones load. */
  stale: boolean;
  approximate?: boolean;
  refining?: boolean;
  /** Why the counts, or an estimate's exact counts, could not be read. */
  error?: string;
  onRetry: () => void;
}

/**
 * The head of a facet: its name, whether its counts are estimated, and
 * why the last read failed, with a way to ask again.
 */
function FacetHead({
  label,
  stale,
  approximate,
  refining,
  error,
  onRetry,
  children,
}: Omit<Shown, "facet"> & { label: string; children?: ReactNode }) {
  return (
    <>
      <div className="facet-head">
        <h3>{label}</h3>
        {approximate && (
          <span className="facet-estimate" title="Nombres estimés sur un échantillon des documents">
            {refining && !stale && !error ? "estimation, calcul exact…" : "estimation"}
          </span>
        )}
        {children}
      </div>
      {error && (
        <p className="facets-note facet-error" role="status">
          {error}{" "}
          <button type="button" className="link-button" onClick={onRetry}>
            Réessayer<span className="visually-hidden"> {label}</span>
          </button>
        </p>
      )}
    </>
  );
}

/** One facet: its values to tick, with their counts and share, the first few then the rest on demand. */
function FacetBox({
  facet,
  picked,
  onToggle,
  ...head
}: Shown & {
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
  if (!values.length && !head.error) return null;
  const most = Math.max(1, ...facet.values.map((v) => v.count));
  const shown = values.slice(0, limit);
  const rest = values.length - shown.length;
  const label = fieldLabel(facet.field);
  return (
    <section className="facet" aria-label={label} ref={box} aria-busy={head.stale || undefined} data-stale={head.stale || undefined}>
      <FacetHead label={label} {...head}>
        {picked.length > 0 && (
          <button
            type="button"
            className="facet-clear"
            onClick={() => {
              const column = box.current?.closest<HTMLElement>(".facets");
              picked.forEach(onToggle);
              // The button goes with the picks: once drawn again, the focus
              // moves to the values left, else to the filters column.
              requestAnimationFrame(() =>
                (box.current?.querySelector<HTMLElement>(".facet-value") || column)?.focus(),
              );
            }}
          >
            Effacer<span className="visually-hidden"> {label}</span>
          </button>
        )}
      </FacetHead>
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
              {count !== undefined && <span className="menu-count">{estimateLabel(count, head.approximate)}</span>}
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
  ...head
}: Shown & {
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
  if (!periods.length && !picked && !head.error) return null;
  return (
    <section
      className="facet facet-dates"
      aria-label={label}
      ref={section}
      aria-busy={head.stale || undefined}
      data-stale={head.stale || undefined}
      onBlur={(event) => {
        if (event.relatedTarget && !event.currentTarget.contains(event.relatedTarget)) refocus.current = false;
      }}
    >
      <FacetHead label={label} {...head} />
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
              const documents = `${estimateLabel(count, head.approximate)} ${count > 1 ? "documents" : "document"}`;
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
