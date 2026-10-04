// Goulots (THE-797): where documents spend their time. The pipeline, one
// card per step in order, with its p50 and p95 on a scale shared by every
// step, the documents waiting at it, and one sentence naming the step that
// slows things down now, judged against that step's own usual time.
import { useState, type ReactNode } from "react";
import { CheckCircle, Hourglass, Moon, TrendUp } from "@phosphor-icons/react";
import { AdminSection, WindowPicker, type SectionProps } from "./AdminSection";
import { useAdminStats, type StatsWindow } from "../../lib/adminStats";
import { duration, short, type StepKey } from "../../lib/admin";
import {
  bottleneck,
  pluginRows,
  stepRows,
  type PluginRow,
  type PluginState,
  type StepRow,
} from "../../lib/health";
import { useActivePlugins } from "./Plugins";
import "../../health.css";

const HINTS: Record<StepKey, string> = {
  received: "de la réception au texte lu",
  cut: "du texte lu aux passages découpés",
  searchable: "des passages à la recherche par mots",
  vectors: "de trouvable aux vecteurs attachés",
  alerts: "appel au plugin d’alertes, par lot",
};

const ICONS = { ok: CheckCircle, warn: TrendUp, quiet: Moon };

// Which plugins run each step, by the role the engine gives them.
const ROLES: Record<StepKey, string[]> = {
  received: ["connector", "normalizer"],
  cut: ["ingestion"],
  searchable: ["ingestion"],
  vectors: ["ingestion"],
  alerts: ["subscription"],
};
const STATE_WORDS: Record<PluginState, string> = {
  ok: "opérationnel",
  degraded: "dégradé",
  down: "en panne",
  idle: "au repos",
};
// The dots of a step: its p95 on the scale every step shares.
const DOTS = 6;

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

export function Bottlenecks({
  onUnauthorized,
  stats,
  bare,
  lead,
}: SectionProps & {
  /** Shown first, on the line of the window picker: the page's state. */
  lead?: ReactNode;
}) {
  const [window, setWindow] = useState<StatsWindow>("1h");
  const steps = useAdminStats("steps", window, onUnauthorized);
  const pluginCalls = useAdminStats("plugins", window, onUnauthorized);
  // A window switch keeps the previous numbers on show while the next ones
  // load, so the bars move to their new length instead of blinking.
  const status =
    steps.data && steps.status === "loading" ? "ready" : steps.status;
  // The alerts row reads plugin calls: only over the same window as the rest.
  const calls =
    pluginCalls.data?.window === steps.data?.window ? pluginCalls.data : null;
  const rows = stepRows(steps.data, calls, stats?.waiting_by_step);
  const active = useActivePlugins(onUnauthorized);
  const hour = useAdminStats("plugins", "1h", onUnauthorized);
  const plugins = pluginRows(active.items || [], pluginCalls.data, hour.data);
  const now = Date.now();
  const verdict = bottleneck(rows, steps.data?.window ?? window, now);
  const scale = scaleOf(rows);
  const Icon = ICONS[verdict.tone];
  return (
    <AdminSection
      id="bottlenecks"
      title="Goulots par étape"
      bare={bare}
      lead={lead}
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
      {/* All flowing well, the page's state above says so: the sentence stays for assistive technology. */}
      <p
        className={verdict.tone === "ok" ? "neck-verdict visually-hidden" : "neck-verdict"}
        data-tone={verdict.tone}
        role="status"
      >
        <Icon size={18} weight="fill" aria-hidden="true" />
        <span>{verdict.text}</span>
      </p>
      <ol
        className="pipe"
        aria-label="Étapes, dans l’ordre"
        data-refreshing={steps.status === "loading" ? "" : undefined}
        title={`Échelle commune, de ${tickLabel(FLOOR_MS)} à ${tickLabel(scale.ticks.at(-1) || 1000)}`}
      >
        {rows.map((row, index) => (
          <StepCard
            key={row.key}
            index={index}
            row={row}
            at={scale.at}
            named={verdict.tone === "warn" && verdict.step === row.key}
            now={now}
            plugins={plugins.filter((p) => p.roles.some((r) => ROLES[row.key].includes(r.split(":")[0])))}
          />
        ))}
      </ol>
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

function StepCard({
  index,
  row,
  at,
  named,
  now,
  plugins,
}: {
  index: number;
  row: StepRow;
  at: (ms: number) => number;
  named: boolean;
  now: number;
  /** The plugins that run this step. */
  plugins: PluginRow[];
}) {
  const measured = row.p95 !== undefined;
  const state = row.slow ? "slow" : named ? "queue" : measured ? "ok" : "none";
  const waitMs = row.waiting.oldest_since
    ? Math.max(0, now - Date.parse(row.waiting.oldest_since))
    : undefined;
  return (
    <li className="stage" data-state={state}>
      <div className="stage-head">
        <span className="stage-n" aria-hidden="true">
          {index + 1}
        </span>
        <h3>{row.label}</h3>
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
      </div>
      <p className="stage-hint">{HINTS[row.key]}</p>
      <dl className="stage-times">
        <div className="stage-p50">
          <dt>p50</dt>
          <dd>{row.p50 !== undefined ? short(row.p50) : "—"}</dd>
        </div>
        <div>
          <dt>p95</dt>
          <dd>
            {measured ? short(row.p95!) : "—"}
            {row.slow && row.recent !== undefined && (
              <span className="neck-recent" aria-hidden="true">
                {" "}
                · {short(row.recent)} récemment
              </span>
            )}
          </dd>
        </div>
      </dl>
      <span
        className="stage-dots"
        aria-hidden="true"
        title={measured ? `p95 de ${duration(row.p95!)}, sur une échelle commune aux étapes` : undefined}
      >
        {Array.from({ length: DOTS }, (_, i) => (
          <i key={i} data-on={measured && i < Math.max(1, Math.round(at(row.p95!) * DOTS)) ? "" : undefined} />
        ))}
      </span>
      {row.slow && row.recent !== undefined && row.usual !== undefined && (
        <span className="visually-hidden">
          Ralenti : {duration(row.recent)} au p95 récemment, contre {duration(row.usual)} d’habitude.
        </span>
      )}
      {!measured && <p className="stage-wait">Aucune mesure sur la période</p>}
      {row.waiting.count > 0 && (
        <p className="stage-wait" data-waiting="">
          <b>{row.waiting.count.toLocaleString("fr-FR")}</b> en attente
          {waitMs !== undefined && <> depuis {waitMs < 60000 ? "moins d’1 min" : short(waitMs)}</>}
        </p>
      )}
      {plugins.length > 0 && (
        <ul className="stage-plugins" aria-label={`Plugins de l’étape ${row.label}`}>
          {(plugins.some((p) => p.state !== "idle") ? plugins.filter((p) => p.state !== "idle") : plugins)
            .slice(0, 3)
            .map((p) => (
              <li key={p.plugin_id} data-state={p.state} title={p.reason || STATE_WORDS[p.state]}>
                <i aria-hidden="true" />
                {p.plugin_id}
                <span className="visually-hidden"> : {STATE_WORDS[p.state]}</span>
              </li>
            ))}
        </ul>
      )}
    </li>
  );
}
