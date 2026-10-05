import { useRef, useState } from "react";
import { Key } from "@phosphor-icons/react";
import { Dialog } from "../Dialog";
import { APIError } from "../../lib/search";
import {
  changeSchedule,
  connectorMessage,
  formatAbsolute,
  formatInterval,
  formatRelative,
  rotateCredential,
  type Connector,
  type ConnectorKind,
  type KindCatalog,
} from "../../lib/connectors";
import { HealthBadge, displayState } from "./HealthBadge";
import { SchemaFields, clearFields, collect } from "./SchemaForm";
import { ErrorSummary, labelFor } from "./ErrorSummary";
import { useSubmit } from "./useSubmit";
import { ExpiryField, readExpiry } from "./fields";
import { sourceSummary } from "./summary";
import { intervals } from "./AddSource";
import { FAILURES, WeekSpark, nameOf, resumable, weekRates, type SourceStats } from "./SourceList";
import { SourceLogo } from "../feed/SourceLogo";

function When({ value, empty = "Jamais" }: { value?: string; empty?: string }) {
  if (!value) return <>{empty}</>;
  return (
    <time dateTime={value} title={formatAbsolute(value)}>
      {formatRelative(value)}
    </time>
  );
}

/**
 * A source's settings, in two columns: what it is and how it does on the
 * left, what can be changed on the right (its name, how often it is read,
 * its credential), saved together. The configuration itself is read-only:
 * another one is another source.
 */
export function ConnectorDetail({
  connector,
  kind,
  catalog,
  stats,
  now,
  onClose,
  onChanged,
  onRename,
  onRemove,
}: {
  connector: Connector;
  kind?: ConnectorKind;
  catalog: KindCatalog;
  /** Null until the facade counted them. */
  stats: SourceStats | null;
  now: number;
  onClose: () => void;
  onChanged: (connector: Connector) => void;
  onRename: (c: Connector, name: string) => Promise<void>;
  onRemove: (c: Connector) => Promise<void>;
}) {
  const h = connector.health;
  const name = nameOf(connector);
  const properties = kind?.config_schema.properties || {};
  const address = connector.kind === "rss" ? sourceSummary(connector, kind) : "";
  const { week, perDay, most } = weekRates(connector, stats, now);
  const current = connector.schedule.interval_seconds;
  const options = [...new Set([...intervals(catalog), current])].sort((a, b) => a - b);
  const [draft, setDraft] = useState(name);
  const [seconds, setSeconds] = useState(current);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [confirming, setConfirming] = useState(false);
  const renamed = draft.trim().replace(/\s+/g, " ") !== name;
  const rescheduled = connector.enabled && seconds !== current;

  const save = async () => {
    if (busy) return;
    if (!renamed && !rescheduled) return onClose();
    setBusy(true);
    setError("");
    let scheduled = false;
    try {
      if (rescheduled) {
        onChanged(await changeSchedule(connector.connector_id, seconds));
        scheduled = true;
      }
      if (renamed) await onRename(connector, draft);
      onClose();
    } catch (e) {
      const message = e instanceof Error && !(e instanceof APIError) ? e.message : connectorMessage(e, catalog.min_interval_seconds);
      setError(scheduled ? `Intervalle enregistré, mais pas le nom : ${message}` : message);
      setBusy(false);
    }
  };
  const remove = async () => {
    setBusy(true);
    setError("");
    try {
      await onRemove(connector);
    } catch (e) {
      setError(e instanceof Error ? e.message : "La source n’a pas pu être retirée.");
      setBusy(false);
    }
  };

  return (
    <Dialog title="Réglages" label={`Réglages de ${name}`} closeLabel="Fermer les réglages" onClose={onClose} wide>
      <div className="settings">
        <aside className="settings-summary" aria-label="Résumé">
          <div className="settings-id">
            <SourceLogo
              namespace={connector.source_namespace}
              connectorId={connector.kind === "rss" ? connector.connector_id : undefined}
            />
            <div className="settings-name">
              <h1>{name}</h1>
              <HealthBadge state={displayState(connector)} />
            </div>
          </div>
          <dl className="sc-stats settings-stats">
            <div>
              <dt>par jour</dt>
              <dd>{stats ? perDay : "—"}</dd>
            </div>
            <div>
              <dt>repérés</dt>
              <dd>{stats ? stats.caught : "—"}</dd>
            </div>
          </dl>
          <WeekSpark week={week} most={most} now={now} />
          <dl className="settings-facts">
            <dt>Dernier article</dt>
            <dd>
              <When value={h.last_item_at} empty="Aucun pour l’instant" />
            </dd>
            {h.last_error && (
              <>
                <dt>Dernière erreur</dt>
                <dd data-tone="error">
                  {FAILURES[h.last_error.code] || "erreur"} (<code>{h.last_error.code}</code>),{" "}
                  <When value={h.last_error.at} />
                </dd>
              </>
            )}
            <dt>Ajoutée</dt>
            <dd>
              <When value={connector.created_at} />
            </dd>
          </dl>
        </aside>
        <div className="settings-form">
          <form
            id="settings-form"
            className="settings-fields"
            noValidate
            onSubmit={(event) => {
              event.preventDefault();
              void save();
            }}
          >
            <label className="form-field">
              <span className="form-label">Nom</span>
              <input
                className="form-input"
                value={draft}
                maxLength={80}
                placeholder={connector.source_namespace}
                title="Vide, la source reprend son nom d’origine"
                onChange={(event) => setDraft(event.target.value)}
              />
            </label>
            <div className="form-field">
              <span className="form-label" id="settings-interval">
                Vérifier les nouveautés toutes les…
              </span>
              {connector.enabled ? (
                <div className="segmented" role="group" aria-labelledby="settings-interval">
                  {options.map((s) => (
                    <button key={s} type="button" aria-pressed={seconds === s} onClick={() => setSeconds(s)}>
                      {formatInterval(s)}
                    </button>
                  ))}
                </div>
              ) : (
                <p className="settings-note">
                  {resumable(connector, kind)
                    ? "En pause : la collecte reprend depuis la carte de la source."
                    : "En pause pour de bon : une source à identifiant ne reprend pas. Ajoutez-la de nouveau pour relancer la collecte."}
                </p>
              )}
            </div>
            {address ? (
              <div className="form-field">
                <span className="form-label">Adresse du flux</span>
                <a className="settings-address" href={address} target="_blank" rel="noreferrer" title={address}>
                  {address}
                </a>
              </div>
            ) : (
              Object.keys(connector.config).length > 0 && (
                <div className="form-field">
                  <span className="form-label">Configuration</span>
                  <dl className="settings-config" title="Une autre configuration est une autre source.">
                    {Object.entries(connector.config).map(([key, value]) => (
                      <div key={key}>
                        <dt>{properties[key]?.title || key}</dt>
                        <dd>
                          <code>{typeof value === "string" ? value : JSON.stringify(value)}</code>
                        </dd>
                      </div>
                    ))}
                  </dl>
                </div>
              )
            )}
          </form>
          <CredentialSection connector={connector} kind={kind} catalog={catalog} onChanged={onChanged} />
          {error && (
            <p className="error-text" role="alert">
              {error}
            </p>
          )}
          {confirming ? (
            <div className="settings-actions" role="group" aria-label={`Retirer ${name}`}>
              <span className="row-confirm">Retirer cette source ? Les articles déjà reçus restent.</span>
              <button type="button" className="button danger" disabled={busy} onClick={remove} autoFocus>
                {busy ? "Retrait…" : "Oui, retirer"}
              </button>
              <button type="button" className="button" onClick={() => setConfirming(false)}>
                Non
              </button>
            </div>
          ) : (
            <div className="settings-actions">
              <button type="button" className="button ghost-danger" onClick={() => setConfirming(true)}>
                Retirer
              </button>
              <span className="settings-gap" />
              <button type="button" className="button" onClick={onClose}>
                Annuler
              </button>
              <button className="button primary" form="settings-form" disabled={busy}>
                {busy ? "Enregistrement…" : "Enregistrer"}
              </button>
            </div>
          )}
        </div>
      </div>
    </Dialog>
  );
}

function CredentialSection({
  connector,
  kind,
  catalog,
  onChanged,
}: {
  connector: Connector;
  kind?: ConnectorKind;
  catalog: KindCatalog;
  onChanged: (c: Connector) => void;
}) {
  const [open, setOpen] = useState(false);
  const [done, setDone] = useState(false);
  const key = useRef(crypto.randomUUID());
  const { busy, failure, summary, run } = useSubmit(
    catalog.min_interval_seconds,
  );
  const c = connector.credential;
  // A source that may take one but has none (a public feed) skips the
  // section, unless the site now refuses access: an identifier may fix that.
  const refused = ["access_error", "credential_expiring"].includes(displayState(connector));
  if (!c && (!kind?.credential_schema || (kind.credential !== "required" && !refused))) return null;
  const canDeposit =
    connector.enabled &&
    !!kind?.credential_schema &&
    catalog.credential_deposits === "available";
  return (
    <section aria-labelledby="detail-credential">
      <h2 id="detail-credential">Identifiant</h2>
      <p>
        {c ? (
          <>
            <Key size={15} aria-hidden="true" /> Présent · version {c.version} ·
            déposé <When value={c.deposited_at} />
            {c.expires_at ? (
              <> · expire le {formatAbsolute(c.expires_at)}</>
            ) : (
              " · sans expiration"
            )}
          </>
        ) : (
          "Aucun identifiant déposé."
        )}
      </p>
      {done && (
        <p role="status" className="success-text">
          Identifiant remplacé. Il s’applique dès la prochaine collecte.
        </p>
      )}
      {!canDeposit && connector.enabled && kind?.credential_schema && (
        <p className="inline-note">
          <Key size={16} aria-hidden="true" /> Le dépôt d’identifiants est
          désactivé sur ce déploiement.
        </p>
      )}
      {canDeposit && !open && (
        <button
          type="button"
          className="button"
          onClick={() => {
            key.current = crypto.randomUUID();
            setDone(false);
            setOpen(true);
          }}
        >
          {c ? "Remplacer l’identifiant" : "Déposer un identifiant"}
        </button>
      )}
      {canDeposit && open && (
        <form
          noValidate
          className="inline-form credential-block"
          onSubmit={(event) =>
            run(
              event,
              (form, local) => {
                const secret = collect(
                  form,
                  kind!.credential_schema!,
                  "secret",
                  local,
                );
                const expires_at = readExpiry(form, "expires_at");
                return async () => {
                  const next = await rotateCredential(connector.connector_id, {
                    idempotency_key: key.current,
                    secret,
                    ...(expires_at ? { expires_at } : {}),
                  }).catch((error) => {
                    if (
                      error instanceof APIError &&
                      error.code === "idempotency_conflict"
                    )
                      key.current = crypto.randomUUID();
                    throw error;
                  });
                  onChanged(next);
                  setOpen(false);
                  setDone(true);
                };
              },
              {
                after: (form) => {
                  clearFields(form, "secret");
                  clearFields(form, "expires_at");
                },
              },
            )
          }
        >
          <ErrorSummary failure={failure} summary={summary} labels={labelFor} />
          <SchemaFields
            schema={kind!.credential_schema!}
            name="secret"
            errors={failure?.fields || {}}
            secret
            legend="Nouvel identifiant"
          />
          <ExpiryField name="expires_at" error={failure?.fields.expires_at} />
          <div className="form-row">
            <button className="button primary" disabled={busy}>
              {busy ? "Dépôt…" : "Déposer le nouvel identifiant"}
            </button>
            <button
              type="button"
              className="button"
              onClick={() => setOpen(false)}
            >
              Annuler
            </button>
          </div>
        </form>
      )}
    </section>
  );
}
