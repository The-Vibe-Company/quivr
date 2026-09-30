import { useEffect, useId, useRef, useState } from "react";
import { APIError } from "../../lib/search";
import {
  DESCRIPTION,
  alertMessage,
  createAlert,
  editAlert,
  type Alert,
  type AlertExpression,
} from "../../lib/alerts";
import { print } from "../../lib/notation";
import {
  cleanWords,
  compose,
  decompose,
  sourceName,
  splitList,
} from "../../lib/alertForm";
import { Interpretation } from "./Interpretation";
import { QueryPreview, useParsed } from "./QueryPreview";
import { DescribedNote } from "./DescribedNote";
import { AlertPreview } from "./AlertPreview";

type Kind = "keywords" | "described";

/**
 * Writes or edits an alert: words to watch as chips (any or all of them),
 * words to ignore and the sources to watch; or a described subject when the
 * deployment has a classifier; or, for anything else, an advanced query in
 * the plugin's notation.
 */
export function AlertForm({
  described,
  editing,
  sources,
  onSaved,
  onCancel,
  onUnauthorized,
}: {
  described: boolean;
  editing: Alert | null;
  /** Source Namespaces an alert can watch. */
  sources: string[];
  onSaved: (alert: Alert, created: boolean) => void;
  onCancel: () => void;
  onUnauthorized: () => void;
}) {
  const id = useId();
  const [kind, setKind] = useState<Kind>("keywords");
  const [words, setWords] = useState<string[]>([]);
  const [wordInput, setWordInput] = useState("");
  const [mode, setMode] = useState<"any" | "all">("any");
  const [exclude, setExclude] = useState("");
  const [watched, setWatched] = useState<string[]>([]);
  const [advanced, setAdvanced] = useState(false);
  const [query, setQuery] = useState("");
  const [description, setDescription] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // One key per alert being written, so a retried submit is not a second alert.
  const idempotency = useRef(crypto.randomUUID());
  const wordField = useRef<HTMLInputElement>(null);
  const parsed = useParsed(query);

  const reset = (next: Alert | null) => {
    setError("");
    setWordInput("");
    idempotency.current = crypto.randomUUID();
    setName(next?.name || "");
    setDescription(
      next?.kind === "described" ? next.expression.description : "",
    );
    setKind(next?.kind === "described" ? "described" : "keywords");
    const form =
      next?.kind === "keywords" ? decompose(next.expression.match) : null;
    setWords(form?.words || []);
    setMode(form?.mode || "any");
    setExclude(form?.exclude.join(", ") || "");
    setWatched(
      next?.kind === "described"
        ? next.expression.sources || []
        : form?.sources || [],
    );
    const needsAdvanced = next?.kind === "keywords" && !form;
    setAdvanced(needsAdvanced);
    setQuery(
      needsAdvanced && next?.kind === "keywords"
        ? (print(next.expression.match) ?? "")
        : "",
    );
  };
  useEffect(() => reset(editing), [editing]);

  const changed = () => {
    setError("");
    if (!editing) idempotency.current = crypto.randomUUID();
  };
  const allWords = cleanWords([...words, ...splitList(wordInput)]);
  const chipsExpression = compose({
    words: allWords,
    mode,
    exclude: splitList(exclude),
    sources: watched,
  });
  const isDescribed = kind === "described";
  const expression: AlertExpression | null = isDescribed
    ? description.trim().length >= DESCRIPTION.min
      ? {
          kind: "described",
          description: description.trim(),
          ...(watched.length ? { sources: watched } : {}),
        }
      : null
    : advanced
      ? parsed.state === "valid"
        ? parsed.expression
        : null
      : chipsExpression;
  const fallbackName = (
    isDescribed ? description : advanced ? query : allWords.join(", ")
  )
    .trim()
    .slice(0, 80);

  const addWords = () => {
    const next = cleanWords([...words, ...splitList(wordInput)]);
    setWords(next);
    setWordInput("");
    changed();
  };

  async function submit() {
    if (busy) return;
    if (!expression) {
      setError(
        isDescribed
          ? `Décrivez le sujet en au moins ${DESCRIPTION.min} caractères.`
          : advanced
            ? parsed.state === "invalid"
              ? parsed.message
              : "Écrivez la requête à surveiller."
            : "Ajoutez au moins un mot à surveiller.",
      );
      return;
    }
    setBusy(true);
    setError("");
    try {
      const saved = editing
        ? await editAlert(
            editing.alert_id,
            expression,
            name.trim() || editing.name,
          )
        : await createAlert(
            name.trim() || fallbackName,
            expression,
            idempotency.current,
          );
      onSaved(saved, !editing);
      reset(null);
    } catch (e) {
      if (e instanceof APIError && e.status === 401) onUnauthorized();
      setError(alertMessage(e, expression.kind));
    } finally {
      setBusy(false);
    }
  }

  const segment = <T extends string>(
    label: string,
    options: [T, string][],
    value: T,
    onChange: (value: T) => void,
    disabled = false,
  ) => (
    <div className="segmented" role="group" aria-label={label}>
      {options.map(([v, text]) => (
        <button
          key={v}
          type="button"
          aria-pressed={value === v}
          disabled={disabled && value !== v}
          onClick={() => {
            onChange(v);
            changed();
          }}
        >
          {text}
        </button>
      ))}
    </div>
  );

  return (
    <form
      className="alert-form"
      aria-labelledby={`${id}-title`}
      noValidate
      onSubmit={(event) => {
        event.preventDefault();
        void submit();
      }}
    >
      <div className="form-intro">
        <h2 id={`${id}-title`}>
          {editing ? "Modifier l’alerte" : "Nouvelle alerte"}
        </h2>
        <p>
          Quivr vous prévient dès qu’un nouvel article correspond, et le marque
          dans le fil.
        </p>
      </div>
      {described ? (
        segment(
          "Type d’alerte",
          [
            ["keywords", "Des mots précis"],
            ["described", "Un sujet décrit"],
          ],
          kind,
          setKind,
          !!editing,
        )
      ) : (
        <p className="form-note alert-kind-off">
          Les alertes sur un sujet décrit en une phrase ne sont pas activées sur
          ce déploiement : elles demandent un classifieur externe.
        </p>
      )}
      {isDescribed ? (
        <div className="form-field">
          <label htmlFor={`${id}-description`}>Décrivez le sujet en une phrase</label>
          <textarea
            id={`${id}-description`}
            value={description}
            maxLength={DESCRIPTION.max}
            rows={3}
            placeholder="Des grèves dans les ports"
            aria-describedby="alert-described-note"
            onChange={(event) => {
              setDescription(event.target.value);
              changed();
            }}
          />
          <p className="form-help">
            Quivr comprend le sens : il attrapera aussi les articles qui en
            parlent avec d’autres mots.
          </p>
          <DescribedNote id="alert-described-note" />
        </div>
      ) : advanced ? (
        <div className="form-field">
          <label htmlFor="alert-query">Requête avancée</label>
          <input
            id="alert-query"
            className="form-input"
            value={query}
            autoComplete="off"
            spellCheck={false}
            placeholder="orage AND (grêle OR vent) NOT football"
            aria-describedby="alert-query-preview"
            onChange={(event) => {
              setQuery(event.target.value);
              changed();
            }}
          />
          <QueryPreview id="alert-query-preview" parsed={parsed} />
          <p className="form-help">
            AND, OR et NOT en majuscules, des parenthèses, une expression entre
            guillemets, et <code>source:nom</code> pour une source.
          </p>
          {(parsed.state === "empty" ||
            (parsed.state === "valid" && decompose(parsed.expression.match))) && (
            <button
              type="button"
              className="link-button"
              onClick={() => {
                const form =
                  parsed.state === "valid"
                    ? decompose(parsed.expression.match)
                    : null;
                setWords(form?.words || []);
                setMode(form?.mode || "any");
                setExclude(form?.exclude.join(", ") || "");
                setWatched(form?.sources || []);
                setAdvanced(false);
                changed();
              }}
            >
              Revenir aux mots simples
            </button>
          )}
        </div>
      ) : (
        <>
          <div className="form-field">
            <label htmlFor={`${id}-words`}>Mots à surveiller</label>
            <div
              className="word-box"
              onClick={() => wordField.current?.focus()}
            >
              {words.length > 0 && (
                <ul className="word-chips" aria-label="Mots surveillés">
                  {words.map((word) => (
                    <li key={word}>
                      {word}
                      <button
                        type="button"
                        aria-label={`Retirer « ${word} »`}
                        onClick={() => {
                          setWords(words.filter((w) => w !== word));
                          changed();
                        }}
                      >
                        ×
                      </button>
                    </li>
                  ))}
                </ul>
              )}
              <input
                ref={wordField}
                id={`${id}-words`}
                value={wordInput}
                autoComplete="off"
                placeholder={words.length ? "Autre mot…" : "orage, grêle, vent…"}
                aria-describedby={`${id}-words-help`}
                onChange={(event) => {
                  setWordInput(event.target.value);
                  changed();
                }}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === ",") {
                    event.preventDefault();
                    if (wordInput.trim()) addWords();
                    else if (event.key === "Enter") void submit();
                  } else if (
                    event.key === "Backspace" &&
                    !wordInput &&
                    words.length
                  ) {
                    setWords(words.slice(0, -1));
                    changed();
                  }
                }}
                onBlur={() => wordInput.trim() && addWords()}
              />
            </div>
            <p id={`${id}-words-help`} className="form-help">
              Un mot ou une expression, puis Entrée. Les majuscules et les
              accents ne comptent pas.
            </p>
          </div>
          {allWords.length > 1 &&
            segment(
              "Combinaison des mots",
              [
                ["any", "Au moins un de ces mots"],
                ["all", "Tous ces mots"],
              ],
              mode,
              setMode,
            )}
          <div className="form-field">
            <label htmlFor={`${id}-exclude`}>
              Ignorer les articles qui parlent de…{" "}
              <span className="form-optional">(facultatif)</span>
            </label>
            <input
              id={`${id}-exclude`}
              className="form-input"
              value={exclude}
              placeholder="football, publicité"
              onChange={(event) => {
                setExclude(event.target.value);
                changed();
              }}
            />
          </div>
        </>
      )}
      <div className="form-field">
        <label htmlFor={`${id}-name`}>
          Nom de l’alerte{" "}
          {!editing && <span className="form-optional">(facultatif)</span>}
        </label>
        <input
          id={`${id}-name`}
          className="form-input"
          value={name}
          maxLength={120}
          placeholder={fallbackName || "Par défaut : les mots surveillés"}
          onChange={(event) => {
            setName(event.target.value);
            changed();
          }}
        />
      </div>
      {(isDescribed || !advanced) && (
        <div className="form-field">
          <span className="form-label" id={`${id}-sources`}>
            Sources surveillées
          </span>
          <div
            className="source-chips"
            role="group"
            aria-labelledby={`${id}-sources`}
          >
            <button
              type="button"
              aria-pressed={watched.length === 0}
              onClick={() => {
                setWatched([]);
                changed();
              }}
            >
              Toutes
            </button>
            {[...new Set([...sources, ...watched])].map((ns) => (
              <button
                key={ns}
                type="button"
                aria-pressed={watched.includes(ns)}
                onClick={() => {
                  setWatched(
                    watched.includes(ns)
                      ? watched.filter((w) => w !== ns)
                      : [...watched, ns],
                  );
                  changed();
                }}
              >
                {sourceName(ns)}
              </button>
            ))}
          </div>
        </div>
      )}
      {!isDescribed && !advanced && (
        <div className="form-preview" aria-live="polite">
          {chipsExpression ? (
            <>
              <p className="form-preview-title">Quivr attrapera les articles avec</p>
              <p className="query-preview" data-state="valid">
                <Interpretation node={chipsExpression.match} />
              </p>
              <AlertPreview
                expression={chipsExpression}
                onDemand={false}
                onUnauthorized={onUnauthorized}
              />
            </>
          ) : (
            <p className="form-preview-title" data-empty="true">
              Ajoutez au moins un mot pour voir comment Quivr lira l’alerte.
            </p>
          )}
        </div>
      )}
      {(isDescribed || advanced) && expression && (
        <div className="form-preview">
          <AlertPreview
            expression={expression}
            onDemand={isDescribed}
            onUnauthorized={onUnauthorized}
          />
        </div>
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="form-actions">
        <button
          className="button primary"
          data-ready={!!expression || undefined}
          disabled={busy}
        >
          {busy
            ? editing
              ? "Enregistrement…"
              : "Création…"
            : editing
              ? "Enregistrer"
              : "Créer l’alerte"}
        </button>
        {editing && (
          <button type="button" className="button" onClick={onCancel}>
            Annuler
          </button>
        )}
        {!isDescribed && !advanced && (
          <button
            type="button"
            className="link-button form-advanced"
            onClick={() => {
              setQuery(
                chipsExpression ? (print(chipsExpression.match) ?? "") : "",
              );
              setAdvanced(true);
              changed();
            }}
          >
            Écrire une requête avancée
          </button>
        )}
      </div>
    </form>
  );
}
