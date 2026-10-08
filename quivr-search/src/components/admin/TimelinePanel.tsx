// One document's timeline, beside the flow: each step as a bar on a shared
// time axis from the document's arrival (a Gantt chart), with the plugin that
// ran it; pointing at or focusing a step shows its exact times. A step still
// running grows until it finishes. Échap closes the panel.
import { useEffect, useRef, useState } from "react";
import { ArrowRight, X } from "@phosphor-icons/react";
import { LoadingState, Notice } from "../ui";
import { APIError } from "../../lib/search";
import {
  STEPS,
  duration,
  fetchTimeline,
  short,
  sourceName,
  titleOf,
  type AdminRow,
  type StepKey,
  type Timeline,
} from "../../lib/admin";

// Which cell of the flow each core step is.
const CELL: Record<string, StepKey> = {
  materialized: "received",
  segmented: "cut",
  retrieval_ready: "searchable",
  enriched: "vectors",
  evaluated: "alerts",
};
const EXTRA: Record<string, string> = {
  accepted: "Arrivée",
  quarantined: "Quarantaine",
  withdrawn: "Retrait",
};
// Pipeline order: core steps and the flow's running or pending cells.
const ORDER = [
  "materialized",
  "received",
  "segmented",
  "cut",
  "retrieval_ready",
  "searchable",
  "enriched",
  "vectors",
  "evaluated",
  "alerts",
  "quarantined",
  "withdrawn",
];
const labelOf = (step: string) =>
  EXTRA[step] || STEPS.find((s) => s.key === CELL[step])?.label || step;
const clock = new Intl.DateTimeFormat("fr-FR", {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  fractionalSecondDigits: 3,
});
const at = (value: number) => clock.format(new Date(value));

/** Round tick spacing (1, 2 or 5 × 10^n ms) giving about three ticks. */
function ticks(span: number) {
  const raw = span / 3;
  const base = 10 ** Math.floor(Math.log10(Math.max(raw, 1)));
  const step = [1, 2, 5, 10].map((m) => m * base).find((s) => s >= raw) || base;
  const out = [];
  for (let t = 0; t <= span; t += step) out.push(t);
  return out;
}

interface Bar {
  key: string;
  label: string;
  state: "done" | "slow" | "run" | "todo" | "error" | "mark" | "none";
  from?: number;
  to?: number;
  ms?: number;
  since?: string;
  plugin?: string;
}

export function TimelinePanel({
  version,
  row,
  titles,
  now,
  onClose,
  onOpen,
  onUnauthorized,
}: {
  version: string;
  /** The document in the flow, when still shown there; its changes reread the timeline. */
  row?: AdminRow;
  titles: Map<string, string>;
  now: number;
  onClose: () => void;
  onOpen: (record: string, version: string) => void;
  onUnauthorized: () => void;
}) {
  const [timeline, setTimeline] = useState<Timeline | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const heading = useRef<HTMLHeadingElement>(null);
  // Steps, alert applicability and whether the Version is current or
  // replaced all change what the timeline shows.
  const revision = JSON.stringify([
    row?.steps,
    row?.evaluation,
    row?.is_current,
    row?.replaced,
  ]);

  useEffect(() => {
    const controller = new AbortController();
    setError("");
    fetchTimeline(version, controller.signal)
      .then(setTimeline)
      .catch((e) => {
        if (controller.signal.aborted) return;
        if (e instanceof APIError && e.status === 401) return onUnauthorized();
        setError(e instanceof Error ? e.message : "Parcours indisponible.");
      });
    return () => controller.abort();
  }, [version, revision, attempt, onUnauthorized]);

  // The panel is keyed by version: take the focus once its heading is there.
  const loaded = !!timeline;
  useEffect(() => {
    if (loaded) heading.current?.focus({ preventScroll: true });
  }, [loaded, version]);

  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || document.querySelector("dialog[open]"))
        return;
      event.preventDefault();
      onClose();
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, [onClose]);

  return (
    <aside
      id="timeline-panel"
      className="panel timeline-panel"
      aria-labelledby="timeline-title"
    >
      <div className="timeline-top">
        <span className="timeline-kicker">Parcours du document</span>
        <button
          type="button"
          className="icon-button"
          onClick={onClose}
          aria-label="Fermer le parcours"
          title="Fermer (Échap)"
        >
          <X size={18} aria-hidden="true" />
        </button>
      </div>
      <div className="timeline-scroll">
        {error && !timeline ? (
          <Notice
            title="Parcours indisponible."
            onRetry={() => setAttempt((n) => n + 1)}
          >
            {error}
          </Notice>
        ) : !timeline ? (
          <LoadingState label="Chargement du parcours…" rows={3} />
        ) : (
          <Content
            timeline={timeline}
            row={row}
            titles={titles}
            now={now}
            heading={heading}
            onOpen={onOpen}
          />
        )}
      </div>
    </aside>
  );
}

function Content({
  timeline,
  row,
  titles,
  now,
  heading,
  onOpen,
}: {
  timeline: Timeline;
  row?: AdminRow;
  titles: Map<string, string>;
  now: number;
  heading: React.RefObject<HTMLHeadingElement | null>;
  onOpen: (record: string, version: string) => void;
}) {
  const { document: doc, steps } = timeline;
  const time = Object.fromEntries(steps.map((s) => [s.step, Date.parse(s.at)]));
  const origin =
    time.accepted ?? Math.min(...steps.map((s) => Date.parse(s.at)));
  const cells = new Map((row?.flow || []).map((c) => [c.key, c]));

  const bars: Bar[] = [];
  for (const step of steps) {
    if (step.step === "accepted") continue;
    const end = Date.parse(step.at);
    const from = step.since ? time[step.since] : undefined;
    const cell = CELL[step.step] ? cells.get(CELL[step.step]) : undefined;
    bars.push({
      key: step.step,
      label: labelOf(step.step),
      state:
        step.step === "quarantined"
          ? "error"
          : from === undefined
            ? "mark"
            : cell?.state === "slow"
              ? "slow"
              : "done",
      from,
      to: end,
      ms: step.duration_ms,
      since: step.since,
      plugin: step.plugin_id
        ? `${step.plugin_id}${step.plugin_version ? ` ${step.plugin_version}` : ""}`
        : undefined,
    });
  }
  // Steps the flow shows running or still to come, in pipeline order; none
  // come after a quarantine or a withdrawal.
  const stopped = doc.state === "quarantined" || doc.state === "withdrawn";
  for (const s of stopped ? [] : STEPS) {
    const cell = cells.get(s.key);
    if (!cell || (cell.state !== "run" && cell.state !== "todo")) continue;
    const since = cell.since ? Date.parse(cell.since) : undefined;
    bars.push({
      key: s.key,
      label: s.label,
      state: cell.state,
      from: cell.state === "run" ? since : undefined,
      to:
        cell.state === "run" && since !== undefined
          ? Math.max(now, since)
          : undefined,
      ms:
        cell.state === "run" && since !== undefined
          ? Math.max(0, now - since)
          : undefined,
    });
  }
  // No alert covers the corpus: no decision is coming, and the panel says so
  // instead of a step running or left out.
  if (
    !stopped &&
    doc.evaluation === "not_applicable" &&
    time.evaluated === undefined &&
    !bars.some((b) => b.key === "alerts")
  )
    bars.push({ key: "alerts", label: labelOf("evaluated"), state: "none" });
  bars.sort((a, b) => ORDER.indexOf(a.key) - ORDER.indexOf(b.key));
  const end = Math.max(origin + 1, ...bars.map((b) => b.to ?? origin));
  const span = end - origin;
  const place = (t: number) => `${((t - origin) / span) * 100}%`;
  const ready = time.retrieval_ready;
  const running = bars.some((b) => b.state === "run");

  return (
    <>
      <h2
        id="timeline-title"
        className="timeline-title"
        ref={heading}
        tabIndex={-1}
      >
        {titleOf(doc, titles)}
      </h2>
      <p className="timeline-meta">
        <span className="flow-source">{sourceName(doc.source_namespace)}</span>
        {time.accepted !== undefined && <span>Reçu à {at(time.accepted)}</span>}
        {doc.replaced && <span>Version remplacée depuis</span>}
      </p>
      <p
        className="timeline-key"
        data-tone={
          doc.state === "quarantined"
            ? "bad"
            : ready === undefined
              ? "run"
              : "ok"
        }
      >
        {doc.state === "quarantined" ? (
          "Mis en quarantaine : pas trouvable"
        ) : ready !== undefined ? (
          <>
            Trouvable en <strong>{duration(ready - origin)}</strong>
          </>
        ) : doc.state === "withdrawn" ? (
          "Retiré avant d’être trouvable"
        ) : (
          <>
            En cours depuis <strong>{short(now - origin)}</strong>
          </>
        )}
      </p>
      <div className="gantt" data-running={running || undefined}>
        <div className="gantt-axis" aria-hidden="true">
          {ticks(span).map((t) => (
            <span key={t} style={{ left: place(origin + t) }}>
              {t === 0 ? "0" : short(t)}
            </span>
          ))}
        </div>
        <div className="gantt-grid" aria-hidden="true">
          {ticks(span).map((t) => (
            <span key={t} style={{ left: place(origin + t) }} />
          ))}
        </div>
        <ol className="gantt-rows" aria-label="Étapes">
          {bars.map((bar) => {
            const exact =
              bar.state === "none"
                ? "aucune alerte ne suit ce corpus"
                : bar.to === undefined
                  ? "à venir"
                  : bar.state === "run"
                    ? `depuis ${at(bar.from ?? bar.to)}`
                    : bar.from !== undefined
                      ? `${at(bar.from)} → ${at(bar.to)}`
                      : at(bar.to);
            return (
              <li
                key={bar.key}
                className="gantt-row"
                data-state={bar.state}
                tabIndex={0}
                aria-label={
                  bar.state === "none"
                    ? `${bar.label} : ${exact}`
                    : `${bar.label} : ${
                        bar.state === "todo"
                          ? "à venir"
                          : bar.state === "run"
                            ? `en cours depuis ${short(bar.ms ?? 0)}`
                            : bar.ms !== undefined
                              ? duration(bar.ms)
                              : "terminé"
                      }${bar.state === "slow" ? ", plus lent que d’habitude" : ""}, ${exact}${bar.plugin ? `, par ${bar.plugin}` : ""}`
                }
              >
                <span className="gantt-label">
                  {bar.label}
                  {bar.plugin && (
                    <span className="gantt-plugin" title={bar.plugin}>
                      {bar.plugin.split(" ")[0]}
                    </span>
                  )}
                </span>
                <span className="gantt-track">
                  {bar.state === "none" && (
                    <span className="gantt-none">Aucune alerte</span>
                  )}
                  {bar.to !== undefined &&
                    (bar.from !== undefined ? (
                      <span
                        className="gantt-bar"
                        style={{
                          left: place(bar.from),
                          width: `max(4px, ${((bar.to - bar.from) / span) * 100}%)`,
                        }}
                      />
                    ) : (
                      <span
                        className="gantt-mark"
                        style={{ left: place(bar.to) }}
                      />
                    ))}
                  <span className="gantt-tip" aria-hidden="true">
                    {exact}
                  </span>
                </span>
                <span className="gantt-ms">
                  {bar.state === "none"
                    ? ""
                    : bar.state === "todo"
                      ? "à venir"
                      : bar.ms !== undefined
                        ? bar.state === "run"
                          ? `${short(bar.ms)}…`
                          : duration(bar.ms)
                        : "—"}
                </span>
              </li>
            );
          })}
        </ol>
      </div>
      {doc.is_current &&
        doc.state !== "withdrawn" &&
        doc.state !== "quarantined" && (
          <div className="timeline-foot">
            <button
              type="button"
              className="button"
              onClick={() => onOpen(doc.record_id, doc.version_id)}
            >
              Lire dans le fil <ArrowRight size={16} aria-hidden="true" />
            </button>
          </div>
        )}
    </>
  );
}
