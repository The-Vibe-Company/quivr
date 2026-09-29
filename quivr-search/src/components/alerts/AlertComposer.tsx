import { useRef, useState } from "react";
import {
  BellRinging,
  CircleNotch,
  Lightbulb,
  Sparkle,
} from "@phosphor-icons/react";
import { APIError } from "../../lib/search";
import {
  DESCRIPTION,
  alertMessage,
  createAlert,
  type Alert,
} from "../../lib/alerts";
import { QueryPreview, useParsed } from "./QueryPreview";
import { DescribedNote } from "./DescribedNote";

// Neutral examples, one per construct of the notation.
const EXAMPLES = [
  "orage OR tempête",
  '"marché aux fleurs"',
  "vendanges NOT football",
  "(port OR quai) AND grève",
  "source:web-demo",
];

// Neutral descriptions, as a reader would put them to a colleague.
const DESCRIBED_EXAMPLES = [
  "Des grèves dans les ports",
  "Des inondations après de fortes pluies",
  "Les tensions diplomatiques autour des visas",
];

const KINDS = [
  {
    value: "keywords",
    label: "Mots-clés",
    hint: "Les articles qui contiennent ces mots.",
  },
  {
    value: "described",
    label: "Décrite",
    hint: "Les articles qui parlent de ce que vous décrivez, même en d’autres mots.",
  },
] as const;

type Kind = Alert["kind"];

/** Whether a description can be sent, and what to say when it cannot. */
export function describedState(text: string) {
  const value = text.trim();
  return value.length === 0
    ? "empty"
    : value.length < DESCRIPTION.min
      ? "short"
      : "valid";
}

export function DescribedError({ text }: { text: string }) {
  const state = describedState(text);
  if (state === "valid") return null;
  return (
    <p className="error-text" role="alert">
      {state === "empty"
        ? "Décrivez ce que l’alerte doit surveiller."
        : `Écrivez au moins ${DESCRIPTION.min} caractères.`}
    </p>
  );
}

export function AlertComposer({
  described,
  onCreated,
  onUnauthorized,
}: {
  /** Whether the deployment offers described alerts (it has a classifier). */
  described: boolean;
  onCreated: (alert: Alert) => void;
  onUnauthorized: () => void;
}) {
  const [kind, setKind] = useState<Kind>("keywords");
  const [query, setQuery] = useState("");
  const [description, setDescription] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [tried, setTried] = useState(false);
  // One key per alert being written, so a retried submit is not a second alert.
  const idempotency = useRef(crypto.randomUUID());
  const input = useRef<HTMLInputElement>(null);
  const parsed = useParsed(query);
  const isDescribed = described && kind === "described";
  const fallbackName = (isDescribed ? description : query).trim().slice(0, 80);

  const changed = () => {
    setError("");
    idempotency.current = crypto.randomUUID();
  };

  async function submit() {
    setTried(true);
    setError("");
    const expression = isDescribed
      ? describedState(description) === "valid"
        ? { kind: "described" as const, description: description.trim() }
        : null
      : parsed.state === "valid"
        ? parsed.expression
        : null;
    if (!expression) {
      input.current?.focus();
      return;
    }
    setBusy(true);
    try {
      const alert = await createAlert(
        name.trim() || fallbackName,
        expression,
        idempotency.current,
      );
      idempotency.current = crypto.randomUUID();
      setQuery("");
      setDescription("");
      setName("");
      setTried(false);
      onCreated(alert);
    } catch (e) {
      if (e instanceof APIError && e.status === 401) onUnauthorized();
      setError(alertMessage(e, expression.kind));
    } finally {
      setBusy(false);
    }
  }

  const submitButton = (
    <button className="button primary" disabled={busy}>
      {busy ? (
        <CircleNotch size={17} className="spin" aria-hidden="true" />
      ) : null}
      {busy ? "Création…" : "Créer l’alerte"}
    </button>
  );

  return (
    <form
      className="alert-composer"
      aria-label="Créer une alerte"
      noValidate
      onSubmit={(event) => {
        event.preventDefault();
        if (!busy) void submit();
      }}
    >
      {described ? (
        <div
          className="mode-switch alert-kind"
          role="group"
          aria-label="Type d’alerte"
        >
          {KINDS.map((item) => (
            <button
              key={item.value}
              type="button"
              aria-pressed={kind === item.value}
              title={item.hint}
              onClick={() => {
                setKind(item.value);
                setTried(false);
                changed();
              }}
            >
              {item.label}
            </button>
          ))}
        </div>
      ) : (
        <p className="alert-kind-off muted">
          Les alertes décrites en langage courant ne sont pas activées sur ce
          déploiement : elles demandent un classifieur externe.
        </p>
      )}
      {isDescribed ? (
        <>
          <label className="visually-hidden" htmlFor="alert-description">
            Description de l’alerte
          </label>
          <div className="source-field alert-field" data-kind="described">
            <Sparkle
              size={20}
              className="source-field-icon"
              aria-hidden="true"
            />
            <input
              id="alert-description"
              ref={input}
              className="source-input"
              value={description}
              maxLength={DESCRIPTION.max}
              onChange={(event) => {
                setDescription(event.target.value);
                changed();
              }}
              placeholder="Des grèves dans les ports"
              autoComplete="off"
              aria-describedby="alert-described-note"
              aria-invalid={tried && describedState(description) !== "valid"}
            />
            {submitButton}
          </div>
          {tried && <DescribedError text={description} />}
          <DescribedNote id="alert-described-note" />
        </>
      ) : (
        <>
          <label className="visually-hidden" htmlFor="alert-query">
            Mots-clés de l’alerte
          </label>
          <div className="source-field alert-field">
            <BellRinging
              size={20}
              className="source-field-icon"
              aria-hidden="true"
            />
            <input
              id="alert-query"
              ref={input}
              className="source-input"
              value={query}
              onChange={(event) => {
                setQuery(event.target.value);
                changed();
              }}
              placeholder="orage AND (grêle OR vent) NOT football"
              autoComplete="off"
              spellCheck={false}
              aria-describedby="alert-query-preview"
              aria-invalid={tried && parsed.state !== "valid"}
            />
            {submitButton}
          </div>
          <QueryPreview id="alert-query-preview" parsed={parsed} />
          {tried && parsed.state === "empty" && (
            <p className="error-text" role="alert">
              Écrivez les mots à surveiller.
            </p>
          )}
          {tried && parsed.state === "invalid" && (
            <p className="visually-hidden" role="alert">
              {parsed.message}
            </p>
          )}
        </>
      )}
      <div className="alert-name">
        <label className="field-label" htmlFor="alert-name">
          Nom de l’alerte <span className="muted">(facultatif)</span>
        </label>
        <input
          id="alert-name"
          value={name}
          maxLength={120}
          onChange={(event) => {
            setName(event.target.value);
            idempotency.current = crypto.randomUUID();
          }}
          placeholder={
            fallbackName ||
            (isDescribed
              ? "Par défaut, la description"
              : "Par défaut, la requête")
          }
        />
      </div>
      {error && (
        <p className="source-error" role="alert">
          {error}
        </p>
      )}
      <div className="alert-examples">
        <span className="muted">
          <Lightbulb size={15} aria-hidden="true" /> Exemples
        </span>
        <ul
          aria-label={
            isDescribed ? "Exemples de descriptions" : "Exemples de requêtes"
          }
        >
          {(isDescribed ? DESCRIBED_EXAMPLES : EXAMPLES).map((example) => (
            <li key={example}>
              <button
                type="button"
                className="example-chip"
                onClick={() => {
                  if (isDescribed) setDescription(example);
                  else setQuery(example);
                  changed();
                  input.current?.focus();
                }}
              >
                {isDescribed ? example : <code>{example}</code>}
              </button>
            </li>
          ))}
        </ul>
      </div>
      {!isDescribed && (
        <details className="alert-help">
          <summary>Comment écrire une requête</summary>
          <dl>
            <div>
              <dt>
                <code>orage grêle</code>
              </dt>
              <dd>tous les mots, n’importe où (comme AND)</dd>
            </div>
            <div>
              <dt>
                <code>orage OR tempête</code>
              </dt>
              <dd>au moins un des mots</dd>
            </div>
            <div>
              <dt>
                <code>orage NOT football</code>
              </dt>
              <dd>sans ce mot</dd>
            </div>
            <div>
              <dt>
                <code>"marché aux fleurs"</code>
              </dt>
              <dd>l’expression exacte</dd>
            </div>
            <div>
              <dt>
                <code>(port OR quai) AND grève</code>
              </dt>
              <dd>des groupes entre parenthèses</dd>
            </div>
            <div>
              <dt>
                <code>source:web-demo</code>
              </dt>
              <dd>
                un filtre sur la source (<code>connector_kind:rss</code>,{" "}
                <code>origin:client</code>…)
              </dd>
            </div>
          </dl>
          <p className="muted">
            AND, OR et NOT s’écrivent en majuscules. Majuscules, accents et
            ponctuation ne comptent pas : <code>greve</code> trouve « Grève ».
            Les mots se comparent entiers : <code>bus</code> ne trouve pas «
            Airbus ». Le titre et le texte sont examinés, pour chaque article
            arrivé après la création de l’alerte.
          </p>
        </details>
      )}
    </form>
  );
}
