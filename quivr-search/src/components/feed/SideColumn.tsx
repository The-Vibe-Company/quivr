import { plural } from "../../lib/format";
import type { FeedItem } from "../../lib/feed";
import { daily, dayLabel, hourly, shortDay } from "../../lib/moments";
import { topics } from "../../lib/topics";

/** "christa pike" reads "Christa Pike"; acronyms keep their capitals. */
const titleCase = (text: string) =>
  text.replace(/(^|[\s-])(\p{Ll})/gu, (_, before: string, letter: string) => before + letter.toUpperCase());

/**
 * Beside the feed: the topics of the moment, the words and names that come
 * back most in the feed's titles (a click searches them, a second click
 * clears the search), and the articles of the last seven days, one bar per
 * day (a click picks the day), or of the day picked, hour by hour.
 */
export function SideColumn({
  titles,
  query,
  span,
  counts,
  day,
  onDay,
  onSearch,
  now,
}: {
  /** The feed's titles of the period picked, whatever is searched. */
  titles: string[];
  query: string;
  /** The articles of every day, for the day-by-day chart. */
  span: FeedItem[];
  /** Articles per day as Quivr counts them, when no filter narrows the feed. */
  counts?: Map<string, number>;
  /** The day chosen, as "2026-10-03", or "" for every day. */
  day: string;
  onDay: (day: string) => void;
  onSearch: (text: string) => void;
  now: number;
}) {
  const subjects = topics(titles, 10);
  const top = Math.max(1, ...subjects.map((t) => t.count));
  const fold = (text: string) =>
    text.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase().trim();
  const times = span.map((i) => i.received_at || i.published_at);
  const week = daily(times, now).map((d) =>
    counts ? { ...d, count: counts.get(d.day) ?? d.count } : d,
  );
  const { counts: hours, today } = hourly(times, day || week[week.length - 1].day, now);
  const bars = day ? hours : week.map((d) => d.count);
  const total = bars.reduce((sum, n) => sum + n, 0);
  const most = Math.max(1, ...bars);
  return (
    <aside className="side" aria-label="Tendances">
      {subjects.length > 0 && (
        <section className="side-card" aria-labelledby="side-topics">
          <div className="side-head">
            <h2 id="side-topics">Sujets du moment</h2>
            <span className="side-total">{day ? dayLabel(day, now) : "Tous les jours"}</span>
          </div>
          <ol className="topics">
            {subjects.map((t, index) => {
              const active = fold(query) === fold(t.label);
              return (
                <li key={t.label}>
                  <button
                    type="button"
                    className="topic"
                    aria-pressed={active}
                    title={active ? "Effacer la recherche" : `Chercher « ${t.label} »`}
                    onClick={() => onSearch(active ? "" : t.label)}
                  >
                    <span className="topic-rank" aria-hidden="true">
                      {index + 1}
                    </span>
                    <span className="topic-label">{titleCase(t.label)}</span>
                    <span className="topic-count">
                      {t.count}
                      <span className="visually-hidden"> articles</span>
                    </span>
                    <span
                      className="topic-bar"
                      aria-hidden="true"
                      style={{ width: `${(t.count / top) * 100}%` }}
                    />
                  </button>
                </li>
              );
            })}
          </ol>
        </section>
      )}
      <section className="side-card" aria-labelledby="side-days">
        <div className="side-head">
          <h2 id="side-days">{day ? `${dayLabel(day, now)}, heure par heure` : "7 derniers jours"}</h2>
          {day ? (
            <button type="button" className="link-button" onClick={() => onDay("")}>
              7 jours
            </button>
          ) : (
            <span className="side-total">{plural(total, "article")}</span>
          )}
        </div>
        {day ? (
          <>
            <div
              className="pulse"
              role="img"
              tabIndex={0}
              data-tips
              aria-label={`${plural(total, "article")} ${today ? "depuis minuit" : "ce jour-là"}`}
            >
              {hours.map((count, hour) => (
                <span
                  key={hour}
                  className="pulse-bar"
                  data-now={(today && hour === hours.length - 1) || undefined}
                  data-empty={count === 0 || undefined}
                  style={count ? { height: `${Math.max(8, (count / most) * 100)}%` } : undefined}
                  data-tip={`${hour} h – ${today && hour === hours.length - 1 ? "maintenant" : `${hour + 1} h`} · ${plural(count, "article")}`}
                />
              ))}
            </div>
            <div className="pulse-axis" aria-hidden="true">
              <span>0 h</span>
              {hours.length > 6 && <span>{Math.round((hours.length - 1) / 2)} h</span>}
              <span>{today ? "maintenant" : "23 h"}</span>
            </div>
          </>
        ) : (
          <>
            <div className="pulse" role="group" data-tips aria-label="Articles des 7 derniers jours">
              {week.map(({ day: d, count }) => (
                <button
                  key={d}
                  type="button"
                  className="pulse-column"
                  data-tip={`${dayLabel(d, now)} · ${plural(count, "article")}`}
                  aria-label={`${dayLabel(d, now)} : ${plural(count, "article")}`}
                  disabled={count === 0}
                  onClick={() => onDay(d)}
                >
                  <span
                    className="pulse-bar"
                    data-empty={count === 0 || undefined}
                    style={count ? { height: `${Math.max(8, (count / most) * 100)}%` } : undefined}
                  />
                </button>
              ))}
            </div>
            <div className="pulse-axis pulse-days" aria-hidden="true">
              {week.map(({ day: d }) => (
                <span key={d}>{shortDay(d, now)}</span>
              ))}
            </div>
          </>
        )}
      </section>
    </aside>
  );
}
