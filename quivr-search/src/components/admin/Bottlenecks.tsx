// Goulots (THE-797): where documents spend their time. One row per step of
// the pipeline, with its p50 and p95 on a scale shared by every step, the
// documents waiting at it, and one sentence naming the step that slows
// things down now, judged against that step's own usual time.
import { useState } from "react";
import { CheckCircle, Hourglass, Moon, TrendUp } from "@phosphor-icons/react";
import { AdminSection, WindowPicker, type SectionProps } from "./AdminSection";
import { useAdminStats, type StatsWindow } from "../../lib/adminStats";
import { duration, short, type StepKey } from "../../lib/admin";
import { bottleneck, stepRows, type StepRow } from "../../lib/health";
import "../../health.css";

const HINTS: Record<StepKey, string> = {
  received: "de la réception au texte lu",
  cut: "du texte lu aux passages découpés",
  searchable: "des passages à la recherche par mots",
  vectors: "de trouvable aux vecteurs attachés",
  alerts: "appel au plugin d’alertes, par lot",
};

const ICONS = { ok: CheckCircle, warn: TrendUp, quiet: Moon };

// The shared scale: logarithmic, so a 40 ms step and a 40 s one both read.
const FLOOR_MS = 10;
const TICKS = [10, 100, 1000, 10000, 60000, 600000, 3600000];
const tickLabel = (ms: number) =>
  ms < 1000 ? `${ms} ms` : ms < 60000 ? `${ms / 1000} s` : `${ms / 60000} min`;

function scaleOf(rows: StepRow[]) {
  const top = Math.max(
    1000,
    ...rows.map((r) => Math.max(r.p95 ?? 0, r.recent ?? 0)),
  );
  const max = TICKS.find((t) => t >= top * 1.15) ?? top * 1.15;
  const span = Math.log10(max) - Math.log10(FLOOR_MS);
  return {
    ticks: TICKS.filter((t) => t <= max),
    at: (ms: number) =>
      Math.min(
        1,
        Math.max(
          0,
          (Math.log10(Math.max(ms, FLOOR_MS)) - Math.log10(FLOOR_MS)) / span,
        ),
      ),
  };
}

export function Bottlenecks({ onUnauthorized, stats }: SectionProps) {
  const [window, setWindow] = useState<StatsWindow>("1h");
  const steps = useAdminStats("steps", window, onUnauthorized);
  const plugins = useAdminStats("plugins", window, onUnauthorized);
  // A window switch keeps the previous numbers on show while the next ones
  // load, so the bars move to their new length instead of blinking.
  const status =
    steps.data && steps.status === "loading" ? "ready" : steps.status;
  // The alerts row reads plugin calls: only over the same window as the rest.
  const calls =
    plugins.data?.window === steps.data?.window ? plugins.data : null;
  const rows = stepRows(steps.data, calls, stats?.waiting_by_step);
  const now = Date.now();
  const verdict = bottleneck(rows, steps.data?.window ?? window, now);
  const scale = scaleOf(rows);
  const Icon = ICONS[verdict.tone];
  return (
    <AdminSection
      id="bottlenecks"
      title="Goulots par étape"
      status={status}
      error={steps.error}
      onRetry={steps.reload}
      aside={
        <WindowPicker
          value={window}
          onChange={setWindow}
          label="Période des goulots"
        />
      }
    >
      <p className="neck-verdict" data-tone={verdict.tone} role="status">
        <Icon size={18} weight="fill" aria-hidden="true" />
        <span>{verdict.text}</span>
      </p>
      <table
        className="neck-table"
        data-refreshing={steps.status === "loading" ? "" : undefined}
      >
        <caption className="visually-hidden">
          Durée de chaque étape sur la période : médiane (p50) et 95e centile
          (p95), et documents en attente.
        </caption>
        <thead>
          <tr>
            <th scope="col">Étape</th>
            <th scope="col" className="neck-axis-head">
              <span className="visually-hidden">Durée</span>
              <span className="neck-axis" aria-hidden="true">
                {scale.ticks.map((t) => (
                  <span key={t} style={{ left: `${scale.at(t) * 100}%` }}>
                    {tickLabel(t)}
                  </span>
                ))}
              </span>
            </th>
            <th scope="col" className="num">
              p50
            </th>
            <th scope="col" className="num">
              p95
            </th>
            <th scope="col" className="num">
              En attente
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <StepLine
              key={row.key}
              row={row}
              at={scale.at}
              ticks={scale.ticks}
              named={verdict.tone === "warn" && verdict.step === row.key}
              now={now}
            />
          ))}
        </tbody>
      </table>
      <p className="neck-legend">
        <span className="neck-key" data-part="p50" aria-hidden="true" /> p50
        <span className="neck-key" data-part="p95" aria-hidden="true" /> p95
        {rows.some((r) => r.slow) && (
          <>
            <span className="neck-key" data-part="usual" aria-hidden="true" />{" "}
            p95 habituel
            <span className="neck-key" data-part="now" aria-hidden="true" /> p95
            récent
          </>
        )}
        <span className="neck-legend-note">
          Chaque étape est mesurée depuis la précédente, attente comprise.
        </span>
      </p>
    </AdminSection>
  );
}

/** Shows the first `share` of a bar; clip-path keeps its round end. */
const reveal = (share: number) =>
  `inset(0 ${((1 - share) * 100).toFixed(2)}% 0 0 round 5px)`;

function StepLine({
  row,
  at,
  ticks,
  named,
  now,
}: {
  row: StepRow;
  at: (ms: number) => number;
  ticks: number[];
  named: boolean;
  now: number;
}) {
  const measured = row.p95 !== undefined;
  const state = row.slow ? "slow" : named ? "queue" : measured ? "ok" : "none";
  const waitMs = row.waiting.oldest_since
    ? Math.max(0, now - Date.parse(row.waiting.oldest_since))
    : undefined;
  return (
    <tr className="neck-row" data-state={state}>
      <th scope="row">
        <span className="neck-name">
          {row.label}
          {row.slow && (
            <span className="neck-chip" data-tone="warn">
              <TrendUp size={12} weight="bold" aria-hidden="true" />
              Ralenti
            </span>
          )}
          {!row.slow && named && (
            <span className="neck-chip" data-tone="warn">
              <Hourglass size={12} weight="bold" aria-hidden="true" />
              File d’attente
            </span>
          )}
        </span>
        <span className="neck-hint">{HINTS[row.key]}</span>
      </th>
      <td className="neck-bar-cell">
        <span className="neck-track" aria-hidden="true">
          {ticks.map((t) => (
            <span
              key={t}
              className="neck-grid"
              style={{ left: `${at(t) * 100}%` }}
            />
          ))}
          <span
            className="neck-bar"
            data-part="p95"
            style={{ clipPath: reveal(measured ? at(row.p95!) : 0) }}
          />
          <span
            className="neck-bar"
            data-part="p50"
            style={{
              clipPath: reveal(row.p50 !== undefined ? at(row.p50) : 0),
            }}
          />
          {row.slow && row.usual !== undefined && (
            <span
              className="neck-usual"
              style={{ left: `${at(row.usual) * 100}%` }}
              title={`p95 habituel : ${duration(row.usual)}`}
            />
          )}
          {row.slow && row.recent !== undefined && (
            <span
              className="neck-now"
              style={{ left: `${at(row.recent) * 100}%` }}
              title={`p95 récent : ${duration(row.recent)}`}
            />
          )}
        </span>
        {!measured && (
          <span className="neck-empty">Aucune mesure sur la période</span>
        )}
        {row.slow && row.recent !== undefined && row.usual !== undefined && (
          <span className="visually-hidden">
            Ralenti : {duration(row.recent)} au p95 récemment, contre{" "}
            {duration(row.usual)} d’habitude.
          </span>
        )}
      </td>
      <td className="num">{row.p50 !== undefined ? short(row.p50) : "—"}</td>
      <td className="num neck-p95">
        {measured ? short(row.p95!) : "—"}
        {row.slow && row.recent !== undefined && (
          <span className="neck-recent" aria-hidden="true">
            {short(row.recent)} récemment
          </span>
        )}
      </td>
      <td className="num neck-wait">
        {row.waiting.count ? (
          <>
            <span className="neck-wait-count">
              {row.waiting.count.toLocaleString("fr-FR")}
            </span>
            {waitMs !== undefined && (
              <span className="neck-wait-age">
                depuis {waitMs < 60000 ? "moins d’1 min" : short(waitMs)}
              </span>
            )}
          </>
        ) : (
          <span className="neck-none">aucun</span>
        )}
      </td>
    </tr>
  );
}
