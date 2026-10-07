import { useEffect, useRef, type KeyboardEvent } from "react";
import type { ExploreItem } from "../../lib/explore";

/** When a document dates from: its publication, else its arrival. */
export const dateOf = (item: ExploreItem) =>
  String(item.metadata["metadata.published_at"]?.[0] || item.published_at || item.received_at || "");

const sameYear = (at: Date) => at.getFullYear() === new Date().getFullYear();

/** "22 décembre", the year added outside the current one. */
export function dayLabel(iso: string) {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return "Sans date";
  return at.toLocaleDateString("fr-FR", { day: "numeric", month: "long", ...(sameYear(at) ? {} : { year: "numeric" }) });
}

/** "Lundi 22 décembre", for a day's header in the list. */
function dayHeading(iso: string) {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return "Sans date";
  const label = at.toLocaleDateString("fr-FR", {
    weekday: "long",
    day: "numeric",
    month: "long",
    ...(sameYear(at) ? {} : { year: "numeric" }),
  });
  return label.charAt(0).toUpperCase() + label.slice(1);
}

/** "09:41", in the reader's time. */
export const timeLabel = (iso: string) => {
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? "" : at.toLocaleTimeString("fr-FR", { hour: "2-digit", minute: "2-digit" });
};

/** "22/12", the day a row dates from when the list is not grouped by day. */
const shortDay = (iso: string) => {
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? "" : at.toLocaleDateString("fr-FR", { day: "2-digit", month: "2-digit" });
};

const dayKey = (iso: string) => {
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? "" : at.toLocaleDateString("sv-SE");
};

/**
 * Documents newest first by when they date from. The engine lists them by
 * arrival, so a correction that arrived later than its neighbours, or an
 * archive read out of order, would break the day headers: what is loaded is
 * put back in date order, undated documents last, ties kept as listed.
 */
export function byDate(items: ExploreItem[]) {
  const at = (item: ExploreItem) => Date.parse(dateOf(item));
  return items
    .map((item, i) => ({ item, i, t: at(item) }))
    .sort((a, b) =>
      Number.isNaN(a.t) || Number.isNaN(b.t)
        ? Number(Number.isNaN(a.t)) - Number(Number.isNaN(b.t)) || a.i - b.i
        : b.t - a.t || a.i - b.i,
    )
    .map(({ item }) => item);
}

/**
 * The wire list (THE-1204, THE-1211): one dense row per document under a
 * day header that stays on top while its rows scroll, or one run of rows by
 * relevance. A row shows its time, language, version when corrected,
 * headline and subjects. It is a listbox: a click or ↑/↓ selects a row for
 * the preview, Enter or a double click opens it.
 */
export function WireList({
  items,
  byDay,
  selected,
  corpusOf,
  onSelect,
  onOpen,
}: {
  items: ExploreItem[];
  /** Grouped under day headers; otherwise in the order given, each row dated. */
  byDay: boolean;
  selected: string | null;
  corpusOf: (item: ExploreItem) => string;
  /** A row picked, by a click or by the keys. */
  onSelect: (id: string, by: "click" | "key") => void;
  onOpen: (id: string) => void;
}) {
  const list = useRef<HTMLDivElement>(null);
  // Keys move the focus with the selection; a click selects where it is.
  const follow = useRef(false);
  useEffect(() => {
    if (!follow.current || !selected) return;
    follow.current = false;
    list.current?.querySelector<HTMLElement>(`[data-record="${CSS.escape(selected)}"]`)?.focus();
  }, [selected]);

  const groups: { key: string; label: string; items: ExploreItem[] }[] = [];
  for (const item of items) {
    const key = byDay ? dayKey(dateOf(item)) : "all";
    if (groups.at(-1)?.key !== key)
      groups.push({ key, label: byDay ? dayHeading(dateOf(item)) : "Les plus pertinents d’abord", items: [] });
    groups.at(-1)!.items.push(item);
  }
  // The row previewed: the one selected when loaded, else the first; a
  // document restored from the address and not loaded yet marks none, and
  // the keys start from the first row.
  const current = !selected ? items[0]?.record_id : items.some((i) => i.record_id === selected) ? selected : undefined;
  const stop = current || items[0]?.record_id;

  const keys = (event: KeyboardEvent<HTMLDivElement>) => {
    const at = items.findIndex((i) => i.record_id === current);
    const to =
      event.key === "ArrowDown" ? at + 1
      : event.key === "ArrowUp" ? at - 1
      : event.key === "Home" ? 0
      : event.key === "End" ? items.length - 1
      : undefined;
    if (event.key === "Enter" && current) {
      event.preventDefault();
      onOpen(current);
      return;
    }
    if (to === undefined) return;
    event.preventDefault();
    const next = items[Math.max(0, Math.min(items.length - 1, to))];
    if (!next) return;
    follow.current = true;
    onSelect(next.record_id, "key");
  };

  return (
    <div className="wire-list" role="listbox" aria-label="Documents" ref={list} onKeyDown={keys} data-by-day={byDay || undefined}>
      {groups.map((group, g) => (
        <div key={`${group.key}-${g}`} className="wire-group" role="group" aria-labelledby={`wire-day-${g}`}>
          <div className="wire-day" id={`wire-day-${g}`}>
            {group.label}
          </div>
          {group.items.map((item) => {
            const at = dateOf(item);
            const language = String(item.metadata["metadata.language"]?.[0] || "");
            const subjects = (item.metadata["metadata.subjects"] || item.metadata["metadata.tags"] || [])
              .slice(0, 4)
              .map(String);
            const corpus = corpusOf(item);
            const on = item.record_id === current;
            return (
              <div
                key={item.record_id}
                role="option"
                className="wire-row"
                data-record={item.record_id}
                aria-selected={on}
                tabIndex={item.record_id === stop ? 0 : -1}
                onClick={() => onSelect(item.record_id, "click")}
                onDoubleClick={() => onOpen(item.record_id)}
              >
                <time className="wire-time" dateTime={at}>
                  {!byDay && <span className="wire-date">{shortDay(at)}</span>}
                  {timeLabel(at)}
                </time>
                <span className="wire-lang" title={language ? `Langue : ${language}` : undefined}>
                  {language.slice(0, 3).toUpperCase()}
                </span>
                <span className="wire-text">
                  <span className="wire-headline">
                    {(item.version || 1) > 1 && (
                      <span className="wire-version" title={`Version ${item.version}`}>
                        v{item.version}
                      </span>
                    )}
                    <span className="wire-title" dir="auto">
                      {item.title || "Sans titre"}
                    </span>
                  </span>
                  {(subjects.length > 0 || corpus) && (
                    <span className="wire-subjects">
                      {corpus && <span className="wire-corpus">{corpus}</span>}
                      {subjects.join(" · ")}
                    </span>
                  )}
                </span>
              </div>
            );
          })}
        </div>
      ))}
    </div>
  );
}

/** Rows shaped like the list's while the first page loads; the label is announced once. */
export function WireSkeleton({ rows = 12 }: { rows?: number }) {
  return (
    <div className="wire-skeleton">
      <p role="status" className="visually-hidden">
        Chargement des documents…
      </p>
      <div aria-hidden="true">
        <div className="wire-day">
          <span className="skeleton-bar" style={{ width: 120 }} />
        </div>
        {Array.from({ length: rows }, (_, i) => (
          <div className="wire-row" key={i}>
            <span className="skeleton-bar" style={{ width: 34 }} />
            <span className="skeleton-bar" style={{ width: 18 }} />
            <span className="wire-text">
              <span className="skeleton-bar" style={{ width: `${55 + ((i * 37) % 40)}%` }} />
              <span className="skeleton-bar skeleton-soft" style={{ width: `${18 + ((i * 23) % 20)}%` }} />
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}
