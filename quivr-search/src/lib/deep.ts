// The "Recherche approfondie": the `deep` search profile, when a retrieval
// plugin serves it, and how its results explain their rank.
import type { SearchProfile, SearchUsage } from "../types";
import { plural } from "./format";

/** Deep search is offered only when a plugin answers a `deep` profile. */
export const offersDeep = (profiles: SearchProfile[]) =>
  profiles.some((p) => p.name === "deep" && p.provider.kind === "plugin");

/** Why a hit ranks where it does, read from its explanation. */
export type Why =
  | { kind: "score"; probability: number; text: string }
  | { kind: "fallback"; reason: string; text: string }
  | { kind: "other"; text: string };

// The Jev re-ranker (plugins/jev-rerank) ends a scored hit's explanation
// with "; noul=<probability>" and starts a fallback with "re-ranker
// unavailable: <reason>". Any other explanation is shown as written.
const REASONS: [RegExp, string][] = [
  [/^API key not configured$/, "le service de re-classement n’est pas configuré"],
  [/^deadline$/, "il n’a pas répondu dans le délai prévu"],
  [/^cost bound$/, "la recherche aurait dépassé son budget"],
  [/^provider refused \(payment\)$/, "le service a refusé le paiement"],
  [/^invalid answer$/, "le service a renvoyé une réponse invalide"],
  [/^(request|response) size bound$/, "la demande dépassait la taille permise"],
  [/^transport/, "le service n’a pas pu être joint"],
  [/^tokenizer/, "les passages n’ont pas pu être préparés"],
  [/^HTTP (\d+)$/, "le service a répondu par une erreur ($1)"],
];

export function explain(explanation: string | undefined): Why | null {
  const text = explanation?.trim();
  if (!text) return null;
  const fallback = text.match(/^re-ranker unavailable:\s*(.*)$/i);
  if (fallback) {
    const raw = fallback[1].trim();
    const known = REASONS.find(([pattern]) => pattern.test(raw));
    return {
      kind: "fallback",
      reason: known ? raw.replace(known[0], known[1]) : raw || "raison inconnue",
      text,
    };
  }
  const score = text.match(/noul=([0-9.eE+-]+)\s*$/);
  const probability = score ? Number(score[1]) : NaN;
  if (Number.isFinite(probability) && probability >= 0 && probability <= 1)
    return { kind: "score", probability, text };
  return { kind: "other", text };
}

const percent = new Intl.NumberFormat("fr-FR", {
  style: "percent",
  maximumFractionDigits: 0,
});
const decimal = new Intl.NumberFormat("fr-FR", { maximumFractionDigits: 2 });

/** "87 %" */
export const asPercent = (p: number) => percent.format(p);

/** "420 ms", "1,4 s" */
export const elapsed = (ms: number) =>
  ms < 1000 ? `${Math.round(ms)} ms` : `${decimal.format(Math.round(ms / 100) / 10)} s`;

/** "1 appel payant (0,4 centime)", "aucun appel payant" */
export function paid(usage: SearchUsage) {
  if (!usage.paid_calls) return "aucun appel payant";
  const cents = usage.cost_cents;
  const cost =
    cents > 0
      ? ` (${decimal.format(cents)} centime${cents >= 2 ? "s" : ""})`
      : "";
  return plural(usage.paid_calls, "appel payant", "appels payants") + cost;
}
