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

/** "09:41", in the reader's time. */
export const timeLabel = (iso: string) => {
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? "" : at.toLocaleTimeString("fr-FR", { hour: "2-digit", minute: "2-digit" });
};

const dayKey = (iso: string) => {
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? "" : at.toLocaleDateString("sv-SE");
};

/**
 * The wire list (THE-1204): one dense row per document under a day header
 * that stays on top while its rows scroll. A row shows its time, language,
 * version when corrected, headline and subjects. It is a listbox: a click or
 * ↑/↓ selects a row for the preview, Enter or a double click opens it.
 */
export function WireList({
  items,
  selected,
  corpusOf,
  onSelect,
  onOpen,
}: {
  items: ExploreItem[];
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
    const at = dateOf(item);
    const key = dayKey(at);
    if (groups.at(-1)?.key !== key) groups.push({ key, label: dayLabel(at), items: [] });
    groups.at(-1)!.items.push(item);
  }
  const current = selected && items.some((i) => i.record_id === selected) ? selected : items[0]?.record_id;

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
    <div className="wire-list" role="listbox" aria-label="Documents" ref={list} onKeyDown={keys}>
      {groups.map((group, g) => (
        <div key={`${group.key}-${g}`} role="group" aria-labelledby={`wire-day-${g}`}>
          <div className="wire-day" id={`wire-day-${g}`}>
            {group.label}
          </div>
          {group.items.map((item) => {
            const at = dateOf(item);
            const language = String(item.metadata["metadata.language"]?.[0] || "");
            const subjects = (item.metadata["metadata.subjects"] || []).slice(0, 3).map(String);
            const corpus = corpusOf(item);
            const on = item.record_id === current;
            return (
              <div
                key={item.record_id}
                role="option"
                className="wire-row"
                data-record={item.record_id}
                aria-selected={on}
                tabIndex={on ? 0 : -1}
                onClick={() => onSelect(item.record_id, "click")}
                onDoubleClick={() => onOpen(item.record_id)}
              >
                <time className="wire-time" dateTime={at}>
                  {timeLabel(at)}
                </time>
                <span className="wire-tags">
                  {language && (
                    <span className="wire-lang" title={`Langue : ${language}`}>
                      {language.slice(0, 3).toUpperCase()}
                    </span>
                  )}
                  {(item.version || 1) > 1 && <span className="wire-version">v{item.version}</span>}
                </span>
                <span className="wire-text">
                  <span className="wire-title">{item.title || "Sans titre"}</span>
                  {(subjects.length > 0 || corpus) && (
                    <span className="wire-subjects">{[corpus, ...subjects].filter(Boolean).join(" · ")}</span>
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
