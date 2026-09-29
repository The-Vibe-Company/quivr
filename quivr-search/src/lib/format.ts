// Times and source states as the dashboard shows them, in plain French.
import type { DisplayState } from "../components/connectors/HealthBadge";

const clock = new Intl.DateTimeFormat("fr-FR", {
  hour: "2-digit",
  minute: "2-digit",
});
const day = new Intl.DateTimeFormat("fr-FR", { day: "numeric", month: "short" });

/** "HH:MM" */
export const hhmm = (value: string) => clock.format(new Date(value));

/** For a feed row: "maint.", "12 min", "21:41" today, "12 sept." before. */
export function shortTime(value: string, now = Date.now()) {
  const at = new Date(value);
  const minutes = Math.floor((now - at.getTime()) / 60000);
  if (minutes < 1) return "maint.";
  if (minutes < 60) return `${minutes} min`;
  return at.toDateString() === new Date(now).toDateString()
    ? clock.format(at)
    : day.format(at);
}

/** "à l’instant", "il y a 12 min", "il y a 3 h", "le 12 sept." */
export function longTime(value: string, now = Date.now()) {
  const minutes = Math.floor((now - new Date(value).getTime()) / 60000);
  if (minutes < 1) return "à l’instant";
  if (minutes < 60) return `il y a ${minutes} min`;
  if (minutes < 24 * 60) return `il y a ${Math.floor(minutes / 60)} h`;
  return `le ${day.format(new Date(value))}`;
}

/** "mardi 29 septembre" */
export const today = (now = Date.now()) =>
  new Date(now).toLocaleDateString("fr-FR", {
    weekday: "long",
    day: "numeric",
    month: "long",
  });

export const plural = (n: number, one: string, many = one + "s") =>
  `${n} ${n > 1 ? many : one}`;

export type Tone = "ok" | "quiet" | "warn" | "error";

const STATES: Record<DisplayState, { label: string; tone: Tone }> = {
  active: { label: "À jour", tone: "ok" },
  silent: { label: "Rien de neuf depuis un moment", tone: "ok" },
  starting: { label: "Premier relevé en cours…", tone: "quiet" },
  paused: { label: "En pause", tone: "quiet" },
  disabled: { label: "Désactivée", tone: "quiet" },
  failing: { label: "Ne répond plus", tone: "error" },
  access_error: { label: "Accès refusé", tone: "error" },
  credential_expiring: { label: "Connexion à renouveler", tone: "warn" },
};

/** How a source's state reads in a list: a few words and a tone. */
export const sourceState = (state: DisplayState) =>
  STATES[state] || { label: state, tone: "quiet" as Tone };

/** States that ask the reader to look at the source. */
export const needsCheck = (state: DisplayState) =>
  state === "failing" ||
  state === "access_error" ||
  state === "credential_expiring";

/** What to tell the reader about a source that needs a look. */
export function sourceProblem(state: DisplayState) {
  switch (state) {
    case "failing":
      return "Le site ne répond pas. Quivr réessaie tout seul ; les articles déjà reçus sont conservés.";
    case "access_error":
      return "L’accès a été refusé. Renouvelez la connexion pour recevoir de nouveau les articles.";
    case "credential_expiring":
      return "La connexion expire bientôt. Renouvelez-la pour continuer à recevoir les articles.";
    default:
      return "";
  }
}
