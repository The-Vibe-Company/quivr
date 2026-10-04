import { useRef, useState } from "react";
import { Key, Power } from "@phosphor-icons/react";
import { Dialog } from "../Dialog";
import { APIError } from "../../lib/search";
import {
  changeSchedule,
  connectorMessage,
  disableConnector,
  formatAbsolute,
  formatInterval,
  formatRelative,
  rotateCredential,
  type Connector,
  type ConnectorKind,
  type KindCatalog,
} from "../../lib/connectors";
import { HealthBadge } from "./HealthBadge";
import { SchemaFields, clearFields, collect } from "./SchemaForm";
import { ErrorSummary, labelFor } from "./ErrorSummary";
import { useSubmit } from "./useSubmit";
import { ExpiryField, IntervalField, readExpiry } from "./fields";
import { sourceSummary } from "./summary";

function When({ value, empty = "Jamais" }: { value?: string; empty?: string }) {
  if (!value) return <>{empty}</>;
  return (
    <time dateTime={value} title={formatAbsolute(value)}>
      {formatRelative(value)}
    </time>
  );
}

export function ConnectorDetail({
  connector,
  kind,
  catalog,
  onClose,
  onChanged,
}: {
  connector: Connector;
  kind?: ConnectorKind;
  catalog: KindCatalog;
  onClose: () => void;
  onChanged: (connector: Connector) => void;
}) {
  const h = connector.health;
  const title = kind?.title || connector.kind;
  const properties = kind?.config_schema.properties || {};
  const source = sourceSummary(connector, kind);
  return (
    <Dialog
      title={`Connecteur ${title}`}
      closeLabel="Fermer le connecteur"
      onClose={onClose}
      wide
    >
      <div className="document-body connector-detail">
        <div className="detail-head">
          <HealthBadge state={h.state} />
          <span className="muted">
            {title}
            {source ? ` · ${connector.source_namespace}` : ""}
          </span>
        </div>
        <h1>{source || connector.source_namespace}</h1>
        <section aria-labelledby="detail-health">
          <h2 id="detail-health">Santé</h2>
          <dl className="facts">
            <dt>Dernière collecte réussie</dt>
            <dd>
              <When value={h.last_success_at} />
            </dd>
            <dt>Dernier élément nouveau</dt>
            <dd>
              <When value={h.last_item_at} empty="Aucun" />
            </dd>
            <dt>Dernière erreur</dt>
            <dd>
              {h.last_error ? (
                <>
                  <code>{h.last_error.code}</code> ·{" "}
                  <When value={h.last_error.at} />
                </>
              ) : (
                "Aucune"
              )}
            </dd>
            {h.usage && (
              <>
                <dt>Lectures du jour (UTC)</dt>
                <dd>
                  {h.usage.items_read} · la veille{" "}
                  {h.usage.previous_day_items_read}
                </dd>
              </>
            )}
            <dt>Santé évaluée</dt>
            <dd>
              <When value={h.evaluated_at} />
            </dd>
            <dt>Créé</dt>
            <dd>
              <When value={connector.created_at} />
            </dd>
            {connector.disabled_at && (
              <>
                <dt>Désactivé</dt>
                <dd>
                  <When value={connector.disabled_at} />
                </dd>
              </>
            )}
          </dl>
        </section>
        <section aria-labelledby="detail-config">
          <h2 id="detail-config">Configuration</h2>
          <dl className="facts">
            {Object.entries(connector.config).map(([name, value]) => (
              <div key={name} className="fact-row">
                <dt>{properties[name]?.title || name}</dt>
                <dd>
                  <code className="config-value">
                    {typeof value === "string" ? value : JSON.stringify(value)}
                  </code>
                </dd>
              </div>
            ))}
          </dl>
          <p className="schema-help">
            La configuration ne se modifie pas : désactivez ce connecteur puis
            créez-en un nouveau sur le même espace de noms.
          </p>
        </section>
        <ScheduleSection
          connector={connector}
          minInterval={catalog.min_interval_seconds}
          onChanged={onChanged}
        />
        <CredentialSection
          connector={connector}
          kind={kind}
          catalog={catalog}
          onChanged={onChanged}
        />
        {connector.enabled && (
          <DisableSection connector={connector} onChanged={onChanged} />
        )}
      </div>
    </Dialog>
  );
}

function ScheduleSection({
  connector,
  minInterval,
  onChanged,
}: {
  connector: Connector;
  minInterval: number;
  onChanged: (c: Connector) => void;
}) {
  const { busy, failure, summary, run } = useSubmit(minInterval);
  const [saved, setSaved] = useState(false);
  const current = connector.schedule.interval_seconds;
  return (
    <section aria-labelledby="detail-schedule">
      <h2 id="detail-schedule">Collecte</h2>
      <p>Toutes les {formatInterval(current)}.</p>
      {connector.enabled && (
        <form
          noValidate
          className="inline-form"
          onSubmit={(event) =>
            run(event, (form) => {
              const seconds = Number(
                (
                  form.elements.namedItem(
                    "interval_seconds",
                  ) as HTMLInputElement
                ).value,
              );
              return async () => {
                setSaved(false);
                onChanged(
                  await changeSchedule(connector.connector_id, seconds),
                );
                setSaved(true);
              };
            })
          }
        >
          <ErrorSummary failure={failure} summary={summary} labels={labelFor} />
          <IntervalField
            key={current}
            name="interval_seconds"
            label="Nouvel intervalle (secondes)"
            defaultValue={current}
            min={minInterval}
            error={failure?.fields.interval_seconds}
          />
          <div className="form-row">
            <button className="button" disabled={busy}>
              {busy ? "Enregistrement…" : "Changer l’intervalle"}
            </button>
            {saved && (
              <span role="status" className="success-text">
                Intervalle enregistré.
              </span>
            )}
          </div>
        </form>
      )}
    </section>
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
  if (!kind?.credential_schema && !c) return null;
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

function DisableSection({
  connector,
  onChanged,
}: {
  connector: Connector;
  onChanged: (c: Connector) => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const key = useRef(crypto.randomUUID());
  return (
    <section aria-labelledby="detail-disable" className="danger-zone">
      <h2 id="detail-disable">Désactiver</h2>
      <p className="muted">
        La collecte s’arrête immédiatement. Les éléments déjà collectés restent
        consultables. Un connecteur désactivé ne peut pas être réactivé.
      </p>
      {error && (
        <p className="error-text" role="alert">
          {error}
        </p>
      )}
      {confirming ? (
        <div className="form-row">
          <button
            type="button"
            className="button danger"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              setError("");
              try {
                onChanged(
                  await disableConnector(connector.connector_id, key.current),
                );
                setConfirming(false);
              } catch (e) {
                setError(connectorMessage(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            <Power size={16} aria-hidden="true" />
            {busy ? "Désactivation…" : "Confirmer la désactivation"}
          </button>
          <button
            type="button"
            className="button"
            onClick={() => setConfirming(false)}
          >
            Annuler
          </button>
        </div>
      ) : (
        <button
          type="button"
          className="button"
          onClick={() => setConfirming(true)}
        >
          <Power size={16} aria-hidden="true" /> Désactiver ce connecteur
        </button>
      )}
    </section>
  );
}
