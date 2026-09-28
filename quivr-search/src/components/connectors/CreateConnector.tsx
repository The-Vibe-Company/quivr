import { useRef, useState } from "react";
import { ArrowLeft, Key, Plugs } from "@phosphor-icons/react";
import { Dialog } from "../Dialog";
import { APIError } from "../../lib/search";
import {
  createConnector,
  type Connector,
  type ConnectorKind,
  type KindCatalog,
} from "../../lib/connectors";
import { SchemaFields, clearFields, collect, fieldId } from "./SchemaForm";
import { ErrorSummary, labelFor } from "./ErrorSummary";
import { useSubmit } from "./useSubmit";
import { ExpiryField, IntervalField, readExpiry } from "./fields";
import { formatInterval } from "../../lib/connectors";

export function CreateConnector({
  catalog,
  corpus,
  onClose,
  onCreated,
}: {
  catalog: KindCatalog;
  corpus: string;
  onClose: () => void;
  onCreated: (connector: Connector) => void;
}) {
  const [kind, setKind] = useState<ConnectorKind | null>(
    catalog.items.length === 1 && usable(catalog.items[0], catalog)
      ? catalog.items[0]
      : null,
  );
  return (
    <Dialog
      title="Ajouter un connecteur"
      closeLabel="Fermer l’ajout de connecteur"
      onClose={onClose}
    >
      {kind ? (
        <ConnectorForm
          key={kind.kind}
          kind={kind}
          catalog={catalog}
          corpus={corpus}
          onBack={catalog.items.length > 1 ? () => setKind(null) : undefined}
          onCreated={onCreated}
        />
      ) : (
        <KindPicker catalog={catalog} onPick={setKind} />
      )}
    </Dialog>
  );
}

const usable = (k: ConnectorKind, c: KindCatalog) =>
  k.credential !== "required" || c.credential_deposits === "available";

function KindPicker({
  catalog,
  onPick,
}: {
  catalog: KindCatalog;
  onPick: (kind: ConnectorKind) => void;
}) {
  return (
    <div className="add-content">
      <div className="modal-icon">
        <Plugs size={24} aria-hidden="true" />
      </div>
      <h2>Quelle source voulez-vous suivre ?</h2>
      <p className="muted">
        Le connecteur collecte la source à intervalle régulier dans l’espace
        démo.
      </p>
      <ul className="kind-list">
        {catalog.items.map((k) => {
          const blocked = !usable(k, catalog);
          return (
            <li key={k.kind}>
              <button
                type="button"
                className="kind-option"
                disabled={blocked}
                aria-describedby={`kind-${k.kind}-about`}
                onClick={() => onPick(k)}
              >
                <span className="kind-title">{k.title}</span>
                <span className="kind-about" id={`kind-${k.kind}-about`}>
                  {k.description}
                  {k.credential === "required" &&
                    (blocked
                      ? " Indisponible : ce type exige un identifiant, et le dépôt d’identifiants est désactivé sur ce déploiement."
                      : " Identifiant requis.")}
                </span>
              </button>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function ConnectorForm({
  kind,
  catalog,
  corpus,
  onBack,
  onCreated,
}: {
  kind: ConnectorKind;
  catalog: KindCatalog;
  corpus: string;
  onBack?: () => void;
  onCreated: (connector: Connector) => void;
}) {
  const deposits = catalog.credential_deposits === "available";
  const offersCredential =
    deposits && kind.credential !== "none" && !!kind.credential_schema;
  const [withCredential, setWithCredential] = useState(
    kind.credential === "required",
  );
  const key = useRef(crypto.randomUUID());
  const { busy, failure, summary, run } = useSubmit(
    catalog.min_interval_seconds,
  );
  const errors = failure?.fields || {};
  const credentialShown = offersCredential && withCredential;
  return (
    <form
      className="add-content connector-form"
      noValidate
      onSubmit={(event) =>
        run(
          event,
          (form, local) => {
            const config = collect(form, kind.config_schema, "config", local);
            const namespace = (
              form.elements.namedItem("source_namespace") as HTMLInputElement
            ).value.trim();
            const interval = Number(
              (
                form.elements.namedItem(
                  "schedule/interval_seconds",
                ) as HTMLInputElement
              ).value,
            );
            const body: Parameters<typeof createConnector>[0] = {
              idempotency_key: key.current,
              corpus_id: corpus,
              source_namespace: namespace,
              kind: kind.kind,
              config,
              schedule: { interval_seconds: interval },
            };
            if (credentialShown) {
              body.credential = {
                secret: collect(
                  form,
                  kind.credential_schema!,
                  "credential/secret",
                  local,
                ),
              };
              const expires = readExpiry(form, "credential/expires_at");
              if (expires) body.credential.expires_at = expires;
            }
            return async () => {
              try {
                onCreated(await createConnector(body));
              } catch (error) {
                if (
                  error instanceof APIError &&
                  error.code === "idempotency_conflict"
                )
                  key.current = crypto.randomUUID();
                throw error;
              }
            };
          },
          {
            // Secrets never outlive the submit request, even when it fails.
            after: (form) => clearFields(form, "credential"),
          },
        )
      }
    >
      {onBack && (
        <button
          type="button"
          className="text-button back-button"
          onClick={onBack}
        >
          <ArrowLeft size={16} aria-hidden="true" /> Changer de type
        </button>
      )}
      <h2>{kind.title}</h2>
      {kind.description && <p className="muted">{kind.description}</p>}
      <ErrorSummary failure={failure} summary={summary} labels={labelFor} />
      <div className="schema-field">
        <label className="field-label" htmlFor={fieldId("source_namespace")}>
          Espace de noms
          <span className="required-mark" aria-hidden="true">
            {" "}
            *
          </span>
        </label>
        <input
          id={fieldId("source_namespace")}
          name="source_namespace"
          type="text"
          required
          maxLength={200}
          placeholder="flux-exemple"
          autoComplete="off"
          spellCheck={false}
          aria-invalid={errors.source_namespace ? true : undefined}
          aria-describedby={`${fieldId("source_namespace")}-help`}
        />
        <p className="schema-help" id={`${fieldId("source_namespace")}-help`}>
          Identifie les éléments de cette source. Un seul connecteur actif par
          espace de noms ; réutilisez-le pour remplacer un connecteur désactivé.
        </p>
        {errors.source_namespace && (
          <p className="error-text">{errors.source_namespace}</p>
        )}
      </div>
      <SchemaFields
        schema={kind.config_schema}
        name="config"
        errors={errors}
        legend="Configuration"
        describe={false}
      />
      <IntervalField
        name="schedule/interval_seconds"
        defaultValue={kind.default_interval_seconds}
        hint={`Par défaut pour ce type : toutes les ${formatInterval(kind.default_interval_seconds)}.`}
        min={catalog.min_interval_seconds}
        error={errors["schedule/interval_seconds"] || errors.schedule}
      />
      {kind.credential !== "none" && !deposits && (
        <p className="inline-note">
          <Key size={16} aria-hidden="true" /> Le dépôt d’identifiants est
          désactivé sur ce déploiement : ce connecteur sera créé sans
          identifiant.
        </p>
      )}
      {offersCredential && kind.credential === "optional" && (
        <label className="choice credential-toggle">
          <input
            type="checkbox"
            checked={withCredential}
            onChange={(event) => setWithCredential(event.target.checked)}
          />
          Cette source demande un identifiant
        </label>
      )}
      {credentialShown && (
        <div className="credential-block">
          <SchemaFields
            schema={kind.credential_schema!}
            name="credential/secret"
            errors={errors}
            secret
            legend="Identifiant"
          />
          <ExpiryField
            name="credential/expires_at"
            error={errors["credential/expires_at"]}
          />
          <p className="schema-help">
            <Key size={14} aria-hidden="true" /> Chiffré dès réception, jamais
            réaffiché : seuls sa version et son expiration restent visibles.
          </p>
        </div>
      )}
      <div className="modal-actions">
        <button className="button primary" disabled={busy}>
          {busy ? "Création…" : "Créer le connecteur"}
        </button>
      </div>
    </form>
  );
}
