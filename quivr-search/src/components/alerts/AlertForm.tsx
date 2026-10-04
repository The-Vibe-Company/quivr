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
import type { FeedItem } from "../../lib/feed";
import { print } from "../../lib/notation";
import {
  cleanWords,
  compose,
  decompose,
  sourceName,
  splitList,
} from "../../lib/alertForm";
import { FilterMenu, MenuOption } from "../feed/FilterMenu";
import { SourceLogo } from "../feed/SourceLogo";
import { DescribedIcon, KeywordsIcon, PlusIcon, SourcesIcon } from "../RailIcons";
import { QueryPreview, useParsed } from "./QueryPreview";
import { DescribedNote } from "./DescribedNote";
import { AlertPreview } from "./AlertPreview";

type Kind = "keywords" | "described";

const KINDS: [Kind, string, string][] = [
  ["keywords", "Des mots précis", "dès que ces mots apparaissent"],
  ["described", "Un sujet décrit", "Quivr comprend le sens"],
];

/**
 * Words as chips in a box: Enter or a comma adds what is typed, Backspace on
 * an empty field takes back the last one. What is typed and not yet added
 * counts too.
 */
function ChipField({
  id,
  label,
  listLabel,
  chips,
  onChips,
  text,
  onText,
  placeholder,
  tone,
  onEmptyEnter,
}: {
  id: string;
  label: string;
  listLabel: string;
  chips: string[];
  onChips: (chips: string[]) => void;
  text: string;
  onText: (text: string) => void;
  placeholder: string;
  tone?: "not";
  onEmptyEnter?: () => void;
}) {
  const field = useRef<HTMLInputElement>(null);
  const add = () => {
    onChips(cleanWords([...chips, ...splitList(text)]));
    onText("");
  };
  return (
    <div className="word-box" data-tone={tone} onClick={() => field.current?.focus()}>
      <label className="visually-hidden" htmlFor={id}>
        {label}
      </label>
      {chips.length > 0 && (
        <ul className="word-chips" aria-label={listLabel}>
          {chips.map((word) => (
            <li key={word}>
              <span>{word}</span>
              <button
                type="button"
                aria-label={`Retirer « ${word} »`}
                onClick={() => onChips(chips.filter((w) => w !== word))}
              >
                ×
              </button>
            </li>
          ))}
        </ul>
      )}
      <input
        ref={field}
        id={id}
        value={text}
        autoComplete="off"
        placeholder={placeholder}
        onChange={(event) => onText(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter" || event.key === ",") {
            event.preventDefault();
            if (text.trim()) add();
            else if (event.key === "Enter") onEmptyEnter?.();
          } else if (event.key === "Backspace" && !text && chips.length) onChips(chips.slice(0, -1));
        }}
        onBlur={() => text.trim() && add()}
      />
    </div>
  );
}

/**
 * Writes or edits an alert as one sentence: "Prévenez-moi quand un article
 * parle de … sauf s'il parle de … dans …", with the words as chips (any or
 * all of them); or a described subject when the deployment has a classifier;
 * or, for anything else, an advanced query in the plugin's notation. Its name
 * is the title, filled from the words until it is written. Under it, what the
 * alert would have caught among the newest articles.
 */
export function AlertForm({
  described,
  editing,
  sources,
  logoOf,
  feedItems,
  onSaved,
  onCancel,
  onUnauthorized,
}: {
  described: boolean;
  editing: Alert | null;
  /** Source Namespaces an alert can watch. */
  sources: string[];
  logoOf: Map<string, string>;
  /** The feed's articles, to date the preview's articles. */
  feedItems: FeedItem[];
  onSaved: (alert: Alert, created: boolean) => void;
  onCancel: () => void;
  onUnauthorized: () => void;
}) {
  const id = useId();
  const [kind, setKind] = useState<Kind>("keywords");
  const [words, setWords] = useState<string[]>([]);
  const [wordInput, setWordInput] = useState("");
  const [mode, setMode] = useState<"any" | "all">("any");
  const [exclude, setExclude] = useState<string[]>([]);
  const [excludeInput, setExcludeInput] = useState("");
  const [watched, setWatched] = useState<string[]>([]);
  const [advanced, setAdvanced] = useState(false);
  const [query, setQuery] = useState("");
  const [description, setDescription] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // One key per alert being written, so a retried submit is not a second alert.
  const idempotency = useRef(crypto.randomUUID());
  const parsed = useParsed(query);

  const reset = (next: Alert | null) => {
    setError("");
    setWordInput("");
    setExcludeInput("");
    idempotency.current = crypto.randomUUID();
    setName(next?.name || "");
    setDescription(next?.kind === "described" ? next.expression.description : "");
    setKind(next?.kind === "described" ? "described" : "keywords");
    const form = next?.kind === "keywords" ? decompose(next.expression.match) : null;
    setWords(form?.words || []);
    setMode(form?.mode || "any");
    setExclude(form?.exclude || []);
    setWatched(next?.kind === "described" ? next.expression.sources || [] : form?.sources || []);
    const needsAdvanced = next?.kind === "keywords" && !form;
    setAdvanced(needsAdvanced);
    setQuery(needsAdvanced && next?.kind === "keywords" ? (print(next.expression.match) ?? "") : "");
  };
  useEffect(() => reset(editing), [editing]);

  const changed = () => {
    setError("");
    if (!editing) idempotency.current = crypto.randomUUID();
  };
  const allWords = cleanWords([...words, ...splitList(wordInput)]);
  const allExcluded = cleanWords([...exclude, ...splitList(excludeInput)]);
  const chipsExpression = compose({ words: allWords, mode, exclude: allExcluded, sources: watched });
  const isDescribed = kind === "described";
  const expression: AlertExpression | null = isDescribed
    ? description.trim().length >= DESCRIPTION.min
      ? { kind: "described", description: description.trim(), ...(watched.length ? { sources: watched } : {}) }
      : null
    : advanced
      ? parsed.state === "valid"
        ? parsed.expression
        : null
      : chipsExpression;
  const fallbackName = (isDescribed ? description : advanced ? query : allWords.join(", ")).trim().slice(0, 80);

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
        ? await editAlert(editing.alert_id, expression, name.trim() || editing.name)
        : await createAlert(name.trim() || fallbackName, expression, idempotency.current);
      onSaved(saved, !editing);
      reset(null);
    } catch (e) {
      if (e instanceof APIError && e.status === 401) onUnauthorized();
      setError(alertMessage(e, expression.kind));
    } finally {
      setBusy(false);
    }
  }

  const sourcesMenu = (
    <FilterMenu
      title="Sources surveillées"
      icon={<SourcesIcon size={15} />}
      placeholder="Toutes les sources"
      summary={
        watched.length === 0
          ? undefined
          : watched.length === 1
            ? sourceName(watched[0])
            : `${watched.length} sources`
      }
    >
      <MenuOption
        single
        pressed={watched.length === 0}
        onClick={() => {
          setWatched([]);
          changed();
        }}
      >
        Toutes les sources
      </MenuOption>
      {[...new Set([...sources, ...watched])].map((ns) => (
        <MenuOption
          key={ns}
          pressed={watched.includes(ns)}
          lead={<SourceLogo namespace={ns} connectorId={logoOf.get(ns)} size="small" />}
          onClick={() => {
            setWatched(watched.includes(ns) ? watched.filter((w) => w !== ns) : [...watched, ns]);
            changed();
          }}
        >
          {sourceName(ns)}
        </MenuOption>
      ))}
    </FilterMenu>
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
      <div className="af-title">
        <span className="kind-tile" data-kind={kind} aria-hidden="true">
          {isDescribed ? <DescribedIcon size={18} /> : <KeywordsIcon size={18} />}
        </span>
        <div className="af-title-text">
          <h2 id={`${id}-title`} tabIndex={-1}>
            {editing ? "Modifier l’alerte" : "Nouvelle alerte"}
          </h2>
          <label className="visually-hidden" htmlFor={`${id}-name`}>
            Nom de l’alerte
          </label>
          <input
            id={`${id}-name`}
            className="af-name"
            value={name}
            maxLength={120}
            placeholder={fallbackName || "Sans nom"}
            title="Le nom de l’alerte ; par défaut, ce qu’elle surveille"
            onChange={(event) => {
              setName(event.target.value);
              changed();
            }}
          />
        </div>
      </div>
      {described ? (
        <div className="af-kinds" role="group" aria-label="Type d’alerte">
          {KINDS.map(([value, label, hint]) => (
            <button
              key={value}
              type="button"
              className="af-kind"
              aria-pressed={kind === value}
              disabled={!!editing && kind !== value}
              onClick={() => {
                setKind(value);
                changed();
              }}
            >
              <span className="kind-tile" aria-hidden="true">
                {value === "described" ? <DescribedIcon size={16} /> : <KeywordsIcon size={16} />}
              </span>
              <span>
                <b>{label}</b>
                <span className="af-kind-hint">{hint}</span>
              </span>
            </button>
          ))}
        </div>
      ) : (
        <p className="form-note alert-kind-off">
          Les alertes sur un sujet décrit en une phrase ne sont pas activées sur ce déploiement : elles
          demandent un classifieur externe.
        </p>
      )}
      {advanced && !isDescribed ? (
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
            AND, OR et NOT en majuscules, des parenthèses, une expression entre guillemets, et{" "}
            <code>source:nom</code> pour une source.
          </p>
          {(parsed.state === "empty" || (parsed.state === "valid" && decompose(parsed.expression.match))) && (
            <button
              type="button"
              className="link-button"
              onClick={() => {
                const form = parsed.state === "valid" ? decompose(parsed.expression.match) : null;
                setWords(form?.words || []);
                setMode(form?.mode || "any");
                setExclude(form?.exclude || []);
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
        <div className="af-sentence">
          <p className="af-line">
            Prévenez-moi quand un article parle de
            {!isDescribed && allWords.length > 1 && (
              <span className="af-mode" role="group" aria-label="Combinaison des mots">
                {(
                  [
                    ["any", "l’un de ces mots"],
                    ["all", "tous ces mots"],
                  ] as const
                ).map(([value, label]) => (
                  <button
                    key={value}
                    type="button"
                    aria-pressed={mode === value}
                    onClick={() => {
                      setMode(value);
                      changed();
                    }}
                  >
                    {label}
                  </button>
                ))}
              </span>
            )}
          </p>
          {isDescribed ? (
            <>
              <label className="visually-hidden" htmlFor={`${id}-description`}>
                Décrivez le sujet en une phrase
              </label>
              <textarea
                id={`${id}-description`}
                className="af-describe"
                value={description}
                maxLength={DESCRIPTION.max}
                rows={2}
                placeholder="des grèves dans les ports"
                aria-describedby="alert-described-note"
                onChange={(event) => {
                  setDescription(event.target.value);
                  changed();
                }}
              />
              <DescribedNote id="alert-described-note" />
            </>
          ) : (
            <>
              <ChipField
                id={`${id}-words`}
                label="Mots à surveiller"
                listLabel="Mots surveillés"
                chips={words}
                onChips={(next) => {
                  setWords(next);
                  changed();
                }}
                text={wordInput}
                onText={(text) => {
                  setWordInput(text);
                  changed();
                }}
                placeholder={words.length ? "+ un mot, puis Entrée" : "orage, grêle, vent…"}
                onEmptyEnter={() => void submit()}
              />
              <p className="af-line">sauf s’il parle de</p>
              <ChipField
                id={`${id}-exclude`}
                label="Ignorer les articles qui parlent de"
                listLabel="Mots ignorés"
                tone="not"
                chips={exclude}
                onChips={(next) => {
                  setExclude(next);
                  changed();
                }}
                text={excludeInput}
                onText={(text) => {
                  setExcludeInput(text);
                  changed();
                }}
                placeholder={exclude.length ? "+ un mot" : "football, publicité… (facultatif)"}
              />
            </>
          )}
          <div className="af-line af-sources">dans {sourcesMenu}</div>
        </div>
      )}
      {!isDescribed && !advanced && !expression && (
        <p className="alert-preview alert-preview-title" data-empty="true">
          Ajoutez un mot pour voir ce que l’alerte aurait repéré.
        </p>
      )}
      {(isDescribed || !advanced || expression) && (
        <AlertPreview
          expression={expression}
          onDemand={isDescribed}
          terms={isDescribed ? [] : allWords}
          logoOf={logoOf}
          feedItems={feedItems}
          onUnauthorized={onUnauthorized}
        />
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="form-actions af-actions">
        <button className="button primary" data-ready={!!expression || undefined} disabled={busy}>
          {!editing && <PlusIcon size={16} />}
          {busy ? (editing ? "Enregistrement…" : "Création…") : editing ? "Enregistrer" : "Créer l’alerte"}
        </button>
        <button type="button" className="button quiet" onClick={onCancel}>
          Annuler
        </button>
        {!isDescribed && !advanced && (
          <button
            type="button"
            className="link-button form-advanced"
            onClick={() => {
              setQuery(chipsExpression ? (print(chipsExpression.match) ?? "") : "");
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
