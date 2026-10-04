// Beside the live flow (Admin tab): the day at a glance, from the same
// rollups as the usage tab. Documents received per source, stacked by hour,
// and the searches per mode with the most frequent query. Each card opens
// the usage tab for the detail.
import { useRef } from "react";
import { Columns } from "./UsageChart";
import { useAdminStats } from "../../lib/adminStats";
import {
  binsOf,
  count,
  hourlySpan,
  modeRows,
  sourceSeries,
  spanOf,
} from "../../lib/usage";
import "../../usage.css";

const SOURCES_READ = 20;

/** The columns of a 24 h read: its own buckets, else the last 24 hours. */
function binsFor(data: { from?: string; to?: string; resolution_seconds?: number } | null, now: number) {
  const span =
    data?.from && data.to && data.resolution_seconds
      ? spanOf({ from: data.from, to: data.to, resolution_seconds: data.resolution_seconds })
      : hourlySpan("24h", now);
  return binsOf(span, "24h");
}

export function Glance({
  onUnauthorized,
  onDetail,
}: {
  onUnauthorized: () => void;
  /** Opens the usage tab. */
  onDetail: () => void;
}) {
  const received = useAdminStats("received", "24h", onUnauthorized, SOURCES_READ);
  const searches = useAdminStats("searches", "24h", onUnauthorized);
  const queries = useAdminStats("top-queries", "24h", onUnauthorized, 1);
  // A source keeps its colour for as long as the page is open.
  const slots = useRef(new Map<string, number>()).current;
  const now = Date.now();

  const docs = received.data;
  const docBins = binsFor(docs, now);
  const series = docs ? sourceSeries(docs, docBins, slots).series : [];
  const modes = searches.data ? modeRows(searches.data, binsFor(searches.data, now)) : [];
  const searched = modes.reduce((n, m) => n + m.count, 0);
  const top = queries.data?.recording ? queries.data.items[0] : undefined;

  return (
    <div className="glance">
      {received.status !== "unavailable" && (
        <section className="panel glance-card" aria-labelledby="glance-docs">
          <div className="glance-head">
            <h2 id="glance-docs">
              <button type="button" className="glance-link" onClick={onDetail}>
                Documents reçus
              </button>
            </h2>
            <span>{docs ? `${count(docs.total)} en 24 h` : "…"}</span>
          </div>
          {docs && docs.total > 0 ? (
            <Columns
              bins={docBins}
              window="24h"
              series={series}
              unit={["document", "documents"]}
              label="Documents reçus par source, 24 h"
            />
          ) : (
            <p className="glance-empty" role={received.status === "error" ? "alert" : undefined}>
              {docs ? "Aucun document en 24 h." : received.status === "error" ? "Chiffres indisponibles." : "Chargement…"}
            </p>
          )}
        </section>
      )}
      {searches.status !== "unavailable" && (
        <section className="panel glance-card" aria-labelledby="glance-searches">
          <div className="glance-head">
            <h2 id="glance-searches">
              <button type="button" className="glance-link" onClick={onDetail}>
                Recherches
              </button>
            </h2>
            <span>{searches.data ? `${count(searched)} en 24 h` : "…"}</span>
          </div>
          {modes.length > 0 ? (
            <ul className="glance-modes" aria-label="Recherches par mode, 24 h">
              {[...modes]
                .sort((a, b) => b.count - a.count)
                .map((m) => (
                  <li key={m.mode}>
                    <span className="usage-swatch" data-slot={m.slot} aria-hidden="true" />
                    {m.label}
                    <b>{count(m.count)}</b>
                  </li>
                ))}
            </ul>
          ) : (
            <p className="glance-empty" role={searches.status === "error" ? "alert" : undefined}>
              {searches.data
                ? "Aucune recherche en 24 h."
                : searches.status === "error"
                  ? "Chiffres indisponibles."
                  : "Chargement…"}
            </p>
          )}
          {top && (
            <p className="glance-foot" title={top.query}>
              Plus fréquente : « {top.query} » ({count(top.count)})
            </p>
          )}
        </section>
      )}
    </div>
  );
}
