import { useRef, useState } from "react";
import { BellRinging, CircleNotch, Lightbulb } from "@phosphor-icons/react";
import { APIError } from "../../lib/search";
import { alertMessage, createAlert, type Alert } from "../../lib/alerts";
import { QueryPreview, useParsed } from "./QueryPreview";

// Neutral examples, one per construct of the notation.
const EXAMPLES = [
  "orage OR tempête",
  '"marché aux fleurs"',
  "vendanges NOT football",
  "(port OR quai) AND grève",
  "source:web-demo",
];

export function AlertComposer({
  onCreated,
  onUnauthorized,
}: {
  onCreated: (alert: Alert) => void;
  onUnauthorized: () => void;
}) {
  const [query, setQuery] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [tried, setTried] = useState(false);
  // One key per alert being written, so a retried submit is not a second alert.
  const idempotency = useRef(crypto.randomUUID());
  const input = useRef<HTMLInputElement>(null);
  const parsed = useParsed(query);
  const fallbackName = query.trim().slice(0, 80);

  async function submit() {
    setTried(true);
    setError("");
    if (parsed.state !== "valid") {
      input.current?.focus();
      return;
    }
    setBusy(true);
    try {
      const alert = await createAlert(
        name.trim() || fallbackName,
        parsed.expression,
        idempotency.current,
      );
      idempotency.current = crypto.randomUUID();
      setQuery("");
      setName("");
      setTried(false);
      onCreated(alert);
    } catch (e) {
      if (e instanceof APIError && e.status === 401) onUnauthorized();
      setError(alertMessage(e));
    } finally {
      setBusy(false);
    }
  }

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
            setError("");
            idempotency.current = crypto.randomUUID();
          }}
          placeholder="orage AND (grêle OR vent) NOT football"
          autoComplete="off"
          spellCheck={false}
          aria-describedby="alert-query-preview"
          aria-invalid={tried && parsed.state !== "valid"}
        />
        <button className="button primary" disabled={busy}>
          {busy ? (
            <CircleNotch size={17} className="spin" aria-hidden="true" />
          ) : null}
          {busy ? "Création…" : "Créer l’alerte"}
        </button>
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
          placeholder={fallbackName || "Par défaut, la requête"}
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
        <ul aria-label="Exemples de requêtes">
          {EXAMPLES.map((example) => (
            <li key={example}>
              <button
                type="button"
                className="example-chip"
                onClick={() => {
                  setQuery(example);
                  setError("");
                  idempotency.current = crypto.randomUUID();
                  input.current?.focus();
                }}
              >
                <code>{example}</code>
              </button>
            </li>
          ))}
        </ul>
      </div>
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
          ponctuation ne comptent pas : <code>greve</code> trouve « Grève ». Les
          mots se comparent entiers : <code>bus</code> ne trouve pas « Airbus ».
          Le titre et le texte sont examinés, pour chaque article arrivé après
          la création de l’alerte.
        </p>
      </details>
    </form>
  );
}
