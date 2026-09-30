// Documents received per hour over the last 24 hours, one bar per hour. The
// part of a bar that ended in quarantine shows in the error tone. Pointing at
// a bar (or moving with the arrow keys) reads its value in the header.
import { useState } from "react";
import type { AdminStats } from "../../lib/admin";

const hour = new Intl.DateTimeFormat("fr-FR", {
  hour: "2-digit",
  minute: "2-digit",
});
const HOUR = 3600000;
const docs = (n: number) => `${n} document${n > 1 ? "s" : ""}`;

export function Throughput({ stats, now }: { stats: AdminStats; now: number }) {
  const { hours } = stats;
  const [point, setPoint] = useState<number | null>(null);
  const total = hours.reduce((sum, h) => sum + h.count, 0);
  const peak = hours.reduce(
    (best, h) => (h.count > best.count ? h : best),
    hours[0],
  );
  const max = Math.max(1, peak?.count || 0);
  const from = stats.counted_from ? Date.parse(stats.counted_from) : undefined;
  const uncounted = (start: number) =>
    from !== undefined && start + HOUR <= from;
  const range = (start: number) =>
    `${hour.format(new Date(start))}–${hour.format(new Date(start + HOUR))}`;
  const summary = total
    ? `${docs(total)} ${stats.hours_of === "searchable" ? "devenus trouvables" : "reçus"} en 24 h, au plus ${peak.count} entre ${range(Date.parse(peak.start))}.`
    : "Aucun document reçu en 24 h.";
  const shown = point === null ? null : hours[point];
  const readout = shown
    ? uncounted(Date.parse(shown.start))
      ? `${range(Date.parse(shown.start))} · non compté`
      : `${range(Date.parse(shown.start))} · ${docs(shown.count)}${shown.errors ? ` · ${shown.errors} erreur${shown.errors > 1 ? "s" : ""}` : ""}`
    : `${from ? "au moins " : ""}${docs(total)} en 24 h`;
  return (
    <section className="panel admin-chart" aria-labelledby="chart-title">
      <div className="admin-chart-head">
        <h2 id="chart-title">
          {stats.hours_of === "searchable"
            ? "Documents devenus trouvables par heure"
            : "Documents reçus par heure"}
        </h2>
        <span
          className="chart-readout"
          data-point={shown ? "" : undefined}
          aria-live="polite"
        >
          {readout}
        </span>
      </div>
      <div
        className="chart-bars"
        role="img"
        aria-label={summary}
        tabIndex={0}
        onMouseLeave={() => setPoint(null)}
        onBlur={() => setPoint(null)}
        onKeyDown={(event) => {
          if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
          event.preventDefault();
          const step = event.key === "ArrowLeft" ? -1 : 1;
          setPoint((p) => Math.min(23, Math.max(0, (p ?? 24) + step)));
        }}
      >
        {hours.map((h, index) => {
          const start = Date.parse(h.start);
          return (
            <div
              key={h.start}
              className="chart-slot"
              data-uncounted={uncounted(start) || undefined}
              data-current={start + HOUR > now || undefined}
              data-point={point === index || undefined}
              onMouseEnter={() => setPoint(index)}
            >
              {!uncounted(start) && h.count > 0 && (
                <div
                  className="chart-bar"
                  style={{ height: `${(h.count / max) * 100}%` }}
                >
                  {h.errors > 0 && (
                    <div
                      className="chart-bar-errors"
                      style={{
                        height: `${Math.min(100, (h.errors / h.count) * 100)}%`,
                      }}
                    />
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>
      {total === 0 && !from && (
        <p className="chart-empty">
          Rien pour l’instant : chaque document compté apparaîtra ici.
        </p>
      )}
      <div className="chart-axis" aria-hidden="true">
        <span>{hour.format(new Date(Date.parse(hours[0]?.start) || now))}</span>
        <span>
          {hour.format(new Date(Date.parse(hours[12]?.start) || now))}
        </span>
        <span>maintenant</span>
      </div>
      {from !== undefined && (
        <p className="chart-note">
          Trop de documents pour tout compter : les heures avant{" "}
          {hour.format(new Date(from))} ne le sont pas.
        </p>
      )}
    </section>
  );
}
