import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import {
  ArrowRight,
  Check,
  CircleNotch,
  LinkSimple,
  Plus,
  Rss,
} from "@phosphor-icons/react";
import { APIError } from "../../lib/search";
import {
  connectorMessage,
  createConnector,
  discoverFeeds,
  formatInterval,
  type Connector,
  type FeedChoice,
  type KindCatalog,
} from "../../lib/connectors";

const MAX_NAME = 120;

/** Hostname of a feed address, for compact display. */
export function feedHost(url: string) {
  try {
    return new URL(url).hostname.replace(/^www\./, "");
  } catch {
    return url;
  }
}

// A bare "example.org/news" is read as https; anything else goes as typed and
// the facade explains what it refuses.
function normalise(value: string) {
  const text = value.trim();
  return /^[a-z][a-z\d+.-]*:\/\//i.test(text) ? text : `https://${text}`;
}

function intervals(catalog: KindCatalog, fallback: number) {
  const rss = catalog.items.find((k) => k.kind === "rss");
  const base = rss?.default_interval_seconds || fallback;
  const floor = catalog.min_interval_seconds;
  return [
    ...new Set([floor, base, 900, 3600, 21600].filter((s) => s >= floor)),
  ].sort((a, b) => a - b);
}

function sourceError(error: unknown, minInterval: number) {
  if (error instanceof APIError && error.code === "source_namespace_in_use")
    return "Une source active porte déjà ce nom. Choisissez-en un autre.";
  if (error instanceof APIError && error.code === "invalid_config")
    return "Cette adresse n’est pas acceptée comme flux : elle doit commencer par http:// ou https://.";
  return connectorMessage(error, minInterval);
}

type Pending = { feeds: FeedChoice[]; key: string };

export function AddSource({
  catalog,
  corpus,
  suggestions,
  existing,
  onCreated,
}: {
  catalog: KindCatalog;
  corpus: string;
  suggestions: FeedChoice[];
  existing: Connector[];
  onCreated: (connector: Connector, message: string) => void;
}) {
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  const errorRef = useRef<HTMLParagraphElement>(null);
  const nameRef = useRef<HTMLInputElement>(null);
  const lookup = useRef(0);
  const [address, setAddress] = useState("");
  const [busy, setBusy] = useState<"discover" | "create" | string | null>(
    null,
  );
  const [error, setError] = useState<{ text: string; at: number } | null>(
    null,
  );
  const [pending, setPending] = useState<Pending | null>(null);
  const [choice, setChoice] = useState(0);
  const [name, setName] = useState("");
  const [renamed, setRenamed] = useState(false);
  const rss = catalog.items.find((k) => k.kind === "rss");
  const options = intervals(catalog, 300);
  const [interval, setSeconds] = useState(
    rss?.default_interval_seconds || 300,
  );

  useEffect(() => {
    if (error) errorRef.current?.focus();
  }, [error]);
  useEffect(() => {
    if (pending) nameRef.current?.focus();
  }, [pending]);

  const fail = (e: unknown) =>
    setError({
      text: sourceError(e, catalog.min_interval_seconds),
      at: Date.now(),
    });
  const collected = new Set(
    existing.filter((c) => c.enabled).map((c) => String(c.config.url || "")),
  );

  async function discover(event: FormEvent) {
    event.preventDefault();
    if (busy || !address.trim()) return;
    const attempt = ++lookup.current;
    setBusy("discover");
    setError(null);
    setPending(null);
    try {
      const { feeds } = await discoverFeeds(normalise(address));
      // The address changed meanwhile: this answer is for another one.
      if (attempt !== lookup.current) return;
      setPending({ feeds, key: crypto.randomUUID() });
      setChoice(0);
      setName(feeds[0].title.slice(0, MAX_NAME));
      setRenamed(false);
    } catch (e) {
      if (attempt === lookup.current) fail(e);
    } finally {
      setBusy(null);
    }
  }

  async function create(
    feed: FeedChoice,
    title: string,
    seconds: number,
    key: string,
    marker: string,
  ) {
    setBusy(marker);
    setError(null);
    try {
      const connector = await createConnector({
        idempotency_key: key,
        corpus_id: corpus,
        source_namespace: title.trim().slice(0, MAX_NAME),
        kind: "rss",
        config: { url: feed.url },
        schedule: { interval_seconds: seconds },
      });
      onCreated(
        connector,
        `« ${connector.source_namespace} » est ajoutée. Les premiers articles arrivent dans un instant.`,
      );
      return true;
    } catch (e) {
      fail(e);
      return false;
    } finally {
      setBusy(null);
    }
  }

  if (!rss) return null;
  const feed = pending?.feeds[choice];

  return (
    <section className="add-source" aria-labelledby={`${id}-title`}>
      <h2 id={`${id}-title`} className="visually-hidden">
        Ajouter une source
      </h2>
      <form className="source-field" onSubmit={discover} noValidate>
        <label htmlFor={`${id}-url`} className="visually-hidden">
          Adresse d’un site ou d’un flux RSS
        </label>
        <LinkSimple size={20} className="source-field-icon" aria-hidden="true" />
        <input
          ref={input}
          id={`${id}-url`}
          className="source-input"
          type="text"
          inputMode="url"
          autoComplete="url"
          spellCheck={false}
          placeholder="Collez l’adresse d’un site ou d’un flux RSS"
          value={address}
          onChange={(event) => {
            // A confirmation belongs to the address it was found for.
            setAddress(event.target.value);
            lookup.current++;
            setPending(null);
          }}
          aria-describedby={`${id}-hint`}
          aria-invalid={error && !pending ? true : undefined}
        />
        <button
          className="button primary"
          disabled={!address.trim() || busy !== null}
          aria-busy={busy === "discover"}
        >
          {busy === "discover" ? (
            <>
              <CircleNotch size={17} className="spin" aria-hidden="true" />
              Recherche…
            </>
          ) : (
            <>
              Ajouter
              <ArrowRight size={17} aria-hidden="true" />
            </>
          )}
        </button>
      </form>
      <p id={`${id}-hint`} className="source-hint muted">
        Un site d’actualité ou l’adresse de son flux : Quivr trouve le flux et
        collecte chaque nouvel article.
      </p>
      {error && (
        <p
          ref={errorRef}
          tabIndex={-1}
          key={error.at}
          className="source-error"
          role="alert"
        >
          {error.text}
        </p>
      )}
      {pending && feed && (
        <form
          className="feed-confirm"
          aria-label="Confirmer la source"
          onSubmit={async (event) => {
            event.preventDefault();
            if (busy || !name.trim()) return;
            if (
              await create(feed, name, interval, pending.key, "create")
            ) {
              setPending(null);
              setAddress("");
              input.current?.focus();
            }
          }}
        >
          {pending.feeds.length > 1 && (
            <fieldset className="feed-choices">
              <legend>
                {pending.feeds.length} flux trouvés. Lequel suivre ?
              </legend>
              {pending.feeds.map((f, index) => (
                <label key={f.url} className="feed-choice">
                  <input
                    type="radio"
                    name={`${id}-feed`}
                    checked={index === choice}
                    onChange={() => {
                      setChoice(index);
                      if (!renamed) setName(f.title.slice(0, MAX_NAME));
                    }}
                  />
                  <span>
                    <span className="feed-choice-title">{f.title}</span>
                    <span className="feed-choice-url">{f.url}</span>
                  </span>
                </label>
              ))}
            </fieldset>
          )}
          {pending.feeds.length === 1 && (
            <p className="feed-found">
              <Rss size={18} weight="bold" aria-hidden="true" />
              <span>
                Flux trouvé
                <span className="feed-choice-url">{feed.url}</span>
              </span>
            </p>
          )}
          <div className="feed-settings">
            <div>
              <label className="field-label" htmlFor={`${id}-name`}>
                Nom de la source
              </label>
              <input
                ref={nameRef}
                id={`${id}-name`}
                value={name}
                maxLength={MAX_NAME}
                required
                onChange={(event) => {
                  setName(event.target.value);
                  setRenamed(true);
                }}
              />
            </div>
            <div>
              <label className="field-label" htmlFor={`${id}-interval`}>
                Relevé
              </label>
              <select
                id={`${id}-interval`}
                value={interval}
                onChange={(event) => setSeconds(Number(event.target.value))}
              >
                {options.map((s) => (
                  <option key={s} value={s}>
                    Toutes les {formatInterval(s)}
                  </option>
                ))}
              </select>
            </div>
          </div>
          <div className="feed-actions">
            <button
              type="button"
              className="button"
              onClick={() => {
                setPending(null);
                input.current?.focus();
              }}
            >
              Annuler
            </button>
            <button
              className="button primary"
              disabled={!name.trim() || busy !== null}
              aria-busy={busy === "create"}
            >
              {busy === "create" ? "Ajout…" : "Commencer la collecte"}
            </button>
          </div>
        </form>
      )}
      {suggestions.length > 0 && (
        <div className="suggestions">
          <h3 id={`${id}-suggestions`}>Suggestions</h3>
          <ul aria-labelledby={`${id}-suggestions`}>
            {suggestions.map((s) => {
              const added = collected.has(s.url);
              const marker = `suggestion:${s.url}`;
              return (
                <li key={s.url}>
                  <button
                    type="button"
                    className="suggestion"
                    disabled={added || busy !== null}
                    aria-busy={busy === marker}
                    aria-label={
                      added
                        ? `${s.title} (déjà suivie)`
                        : `Ajouter ${s.title}`
                    }
                    onClick={() =>
                      void create(
                        s,
                        s.title,
                        rss.default_interval_seconds,
                        crypto.randomUUID(),
                        marker,
                      )
                    }
                  >
                    {added ? (
                      <Check size={15} weight="bold" aria-hidden="true" />
                    ) : busy === marker ? (
                      <CircleNotch
                        size={15}
                        className="spin"
                        aria-hidden="true"
                      />
                    ) : (
                      <Plus size={15} weight="bold" aria-hidden="true" />
                    )}
                    <span className="suggestion-title">{s.title}</span>
                    <span className="suggestion-host">{feedHost(s.url)}</span>
                  </button>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </section>
  );
}
