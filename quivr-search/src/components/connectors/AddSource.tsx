import { useEffect, useId, useRef, useState } from "react";
import { Check, CircleNotch, Plus } from "@phosphor-icons/react";
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
const DISCOVER_DELAY_MS = 700;
// The intervals offered, bounded below by what the deployment allows.
const INTERVALS = [300, 900, 1800, 3600];

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

// Looks enough like an address to look it up while the reader types.
const looksLikeAddress = (value: string) =>
  /^[a-z][a-z\d+.-]*:\/\/\S+/i.test(value.trim()) ||
  /^[^\s/]+\.[^\s/]{2,}/.test(value.trim());

export function intervals(catalog: KindCatalog) {
  const floor = catalog.min_interval_seconds;
  return [
    ...new Set([...(floor < INTERVALS[0] ? [floor] : []), ...INTERVALS]),
  ].filter((s) => s >= floor);
}

function sourceError(error: unknown, minInterval: number) {
  if (error instanceof APIError && error.code === "source_namespace_in_use")
    return "Une source active porte déjà ce nom. Choisissez-en un autre.";
  if (error instanceof APIError && error.code === "invalid_config")
    return "Cette adresse n’est pas acceptée comme fil : elle doit commencer par http:// ou https://.";
  return connectorMessage(error, minInterval);
}

type Found = { feeds: FeedChoice[]; key: string };

/**
 * Paste a site's address: Quivr looks for its feed as soon as the reader
 * stops typing (the facade refuses private addresses), then starts
 * collecting it at the chosen interval. Suggestions add in one click.
 */
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
  const lookup = useRef(0);
  const auto = useRef<ReturnType<typeof setTimeout>>(undefined);
  const [address, setAddress] = useState("");
  const [checking, setChecking] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<{
    text: string;
    at: number;
    focus: boolean;
  } | null>(null);
  const [found, setFound] = useState<Found | null>(null);
  const [choice, setChoice] = useState(0);
  const [name, setName] = useState("");
  const [renamed, setRenamed] = useState(false);
  const rss = catalog.items.find((k) => k.kind === "rss");
  const options = intervals(catalog);
  const preferred = rss?.default_interval_seconds || 900;
  const [interval, setSeconds] = useState(
    options.includes(preferred)
      ? preferred
      : options.find((s) => s >= preferred) || options[0],
  );

  useEffect(() => {
    if (error?.focus) errorRef.current?.focus();
  }, [error]);

  const fail = (e: unknown, focus: boolean) =>
    setError({
      text: sourceError(e, catalog.min_interval_seconds),
      at: Date.now(),
      focus,
    });
  const collected = new Set(
    existing.filter((c) => c.enabled).map((c) => String(c.config.url || "")),
  );

  async function discover(value: string, byHand: boolean) {
    clearTimeout(auto.current);
    const attempt = ++lookup.current;
    setChecking(true);
    setError(null);
    setFound(null);
    try {
      const { feeds } = await discoverFeeds(normalise(value));
      // The address changed meanwhile: this answer is for another one.
      if (attempt !== lookup.current) return;
      setFound({ feeds, key: crypto.randomUUID() });
      setChoice(0);
      setName(feeds[0].title.slice(0, MAX_NAME));
      setRenamed(false);
    } catch (e) {
      if (attempt === lookup.current) fail(e, byHand);
    } finally {
      if (attempt === lookup.current) setChecking(false);
    }
  }

  // Look the address up once the reader pauses.
  useEffect(() => {
    if (!looksLikeAddress(address)) return;
    auto.current = setTimeout(
      () => void discover(address, false),
      DISCOVER_DELAY_MS,
    );
    return () => clearTimeout(auto.current);
    // discover reads the latest state through its own lookup counter.
  }, [address]);

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
        `« ${connector.source_namespace} » ajoutée. Ses premiers articles arrivent dans un instant.`,
      );
      return true;
    } catch (e) {
      fail(e, true);
      return false;
    } finally {
      setBusy(null);
    }
  }

  if (!rss) return null;
  const feed = found?.feeds[choice];
  const ready = !!feed && !!name.trim() && busy === null;

  // Entrée looks the address up, or starts collecting the feed found.
  async function submit() {
    if (!address.trim() || busy) return;
    if (!feed) return void discover(address, true);
    if (!ready) return;
    if (await create(feed, name, interval, found!.key, "create")) {
      setFound(null);
      setAddress("");
      input.current?.focus();
    }
  }

  return (
    <section className="add-source" aria-labelledby={`${id}-title`}>
      <div className="form-intro">
        <h2 id={`${id}-title`}>Ajouter une source</h2>
        <p>
          Collez l’adresse d’un site ou d’un journal. Quivr trouve tout seul
          son fil d’actualités et le relève régulièrement.
        </p>
      </div>
      <form
        className="source-form"
        aria-label="Ajouter une source"
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <div className="form-field">
          <label htmlFor={`${id}-url`}>Adresse du site</label>
          <input
            ref={input}
            id={`${id}-url`}
            className="form-input"
            data-state={error && !found ? "error" : found ? "found" : undefined}
            type="text"
            inputMode="url"
            autoComplete="url"
            spellCheck={false}
            placeholder="www.example.org"
            value={address}
            onChange={(event) => {
              // A result belongs to the address it was found for.
              setAddress(event.target.value);
              lookup.current++;
              setChecking(false);
              setFound(null);
              setError(null);
            }}
            aria-describedby={`${id}-status`}
            aria-invalid={error && !found ? true : undefined}
            onKeyDown={(event) => {
              // The submit button stays disabled until a feed is found, which
              // would block Entrée's implicit submission.
              if (event.key === "Enter") {
                event.preventDefault();
                void submit();
              }
            }}
          />
          <div id={`${id}-status`} aria-live="polite">
            {checking && (
              <p className="form-help">
                <CircleNotch size={14} className="spin" aria-hidden="true" />{" "}
                Recherche du fil d’actualités…
              </p>
            )}
            {found && feed && found.feeds.length === 1 && (
              <div className="found-card">
                <p className="found-label">Fil trouvé</p>
                <p className="found-title">{feed.title}</p>
                <p className="found-url">{feed.url}</p>
              </div>
            )}
          </div>
          {error && (
            <p
              ref={errorRef}
              tabIndex={-1}
              key={error.at}
              className="form-error"
              role="alert"
            >
              {error.text}
            </p>
          )}
        </div>
        {found && found.feeds.length > 1 && (
          <fieldset className="feed-choices">
            <legend>
              {found.feeds.length} fils trouvés. Lequel suivre ?
            </legend>
            {found.feeds.map((f, index) => (
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
        {found && (
          <div className="form-field">
            <label htmlFor={`${id}-name`}>Nom de la source</label>
            <input
              id={`${id}-name`}
              className="form-input"
              value={name}
              maxLength={MAX_NAME}
              required
              onChange={(event) => {
                setName(event.target.value);
                setRenamed(true);
              }}
            />
          </div>
        )}
        <div className="form-field">
          <span className="form-label" id={`${id}-interval`}>
            Vérifier les nouveautés toutes les…
          </span>
          <div
            className="segmented"
            role="group"
            aria-labelledby={`${id}-interval`}
          >
            {options.map((s) => (
              <button
                key={s}
                type="button"
                aria-pressed={interval === s}
                onClick={() => setSeconds(s)}
              >
                {formatInterval(s)}
              </button>
            ))}
          </div>
        </div>
        <button
          className="button primary"
          data-ready={ready || undefined}
          disabled={!ready}
          aria-busy={busy === "create"}
        >
          {busy === "create" ? "Ajout…" : "Commencer la collecte"}
        </button>
      </form>
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
                      added ? `${s.title} (déjà suivie)` : `Ajouter ${s.title}`
                    }
                    title={feedHost(s.url)}
                    onClick={() =>
                      void create(
                        s,
                        s.title,
                        interval,
                        crypto.randomUUID(),
                        marker,
                      )
                    }
                  >
                    {added ? (
                      <Check size={14} weight="bold" aria-hidden="true" />
                    ) : busy === marker ? (
                      <CircleNotch size={14} className="spin" aria-hidden="true" />
                    ) : (
                      <Plus size={14} weight="bold" aria-hidden="true" />
                    )}
                    {s.title}
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
