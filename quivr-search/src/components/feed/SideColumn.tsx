import type { AlertList, Alert } from "../../lib/alerts";
import { HAND_NAMESPACE, type FeedItem } from "../../lib/feed";
import { keywordRule, sourceName } from "../../lib/alertForm";
import { plural, shortTime, sourceState } from "../../lib/format";
import type { Connector } from "../../lib/connectors";
import { groupSources } from "../connectors/SourceList";
import { displayState, type DisplayState } from "../connectors/HealthBadge";
import type { Filter } from "./FeedPage";

/** How an alert reads in a list: its words, or its description. */
export function alertRule(alert: Alert, withState = true) {
  const rule =
    alert.kind === "described"
      ? `Décrite : « ${alert.expression.description} »`
      : alert.kind === "keywords"
        ? keywordRule(alert.expression.match)
        : "Alerte d’un autre type";
  return alert.enabled || !withState ? rule : `En pause · ${rule}`;
}

/**
 * Beside the feed: each alert with its count and latest catch, and each
 * source with its health and counts. Each one filters the feed.
 */
export function SideColumn({
  alerts,
  connectors,
  items,
  filter,
  now,
  onFilter,
  onOpen,
  onAlerts,
  onSources,
}: {
  alerts: AlertList | null;
  connectors: Connector[];
  items: FeedItem[];
  filter: Filter;
  now: number;
  onFilter: (filter: Filter) => void;
  onOpen: (item: FeedItem) => void;
  onAlerts: () => void;
  onSources: (connectorId?: string) => void;
}) {
  const matched = alerts?.matched || {};
  const counts = new Map<string, { all: number; caught: number }>();
  for (const item of items) {
    const c = counts.get(item.namespace) || { all: 0, caught: 0 };
    c.all += 1;
    if (matched[item.record_id]?.length) c.caught += 1;
    counts.set(item.namespace, c);
  }
  const rows: { namespace: string; connector?: Connector; state?: DisplayState }[] = [
    ...groupSources(connectors).map((c) => ({
      namespace: c.source_namespace,
      connector: c,
      state: displayState(c),
    })),
  ];
  if (counts.has(HAND_NAMESPACE)) rows.push({ namespace: HAND_NAMESPACE });

  return (
    <aside className="side" aria-label="Alertes et sources">
      <section className="panel side-card" aria-labelledby="side-alerts">
        <div className="side-head">
          <h2 id="side-alerts">Alertes</h2>
          <button type="button" className="link-button" onClick={onAlerts}>
            Gérer <span className="visually-hidden">les alertes</span>
          </button>
        </div>
        {!alerts ? null : !alerts.available ? (
          <p className="side-empty">
            Les alertes ne sont pas activées sur ce déploiement.
          </p>
        ) : alerts.items.length === 0 ? (
          <p className="side-empty">
            Aucune alerte.{" "}
            <button type="button" className="link-button" onClick={onAlerts}>
              Créer la première
            </button>
          </p>
        ) : (
          <ul className="alert-cards">
            {alerts.items.map((alert) => {
              const active =
                filter.kind === "alert" && filter.id === alert.alert_id;
              const latest = items.find((i) =>
                matched[i.record_id]?.includes(alert.alert_id),
              );
              const at = latest?.received_at || latest?.published_at;
              return (
                <li
                  key={alert.alert_id}
                  className="alert-card"
                  data-active={active || undefined}
                  data-enabled={alert.enabled}
                >
                  <button
                    type="button"
                    className="alert-card-main"
                    aria-pressed={active}
                    onClick={() =>
                      onFilter(
                        active
                          ? { kind: "all" }
                          : { kind: "alert", id: alert.alert_id },
                      )
                    }
                  >
                    <span className="alert-card-name">{alert.name}</span>
                    <span
                      className="alert-card-count"
                      data-zero={alert.match_count === 0 || undefined}
                    >
                      {alert.match_count}
                      {alert.capped ? "+" : ""}
                      <span className="visually-hidden">
                        {" "}
                        article{alert.match_count > 1 ? "s" : ""} attrapé
                        {alert.match_count > 1 ? "s" : ""}
                      </span>
                    </span>
                  </button>
                  <p className="alert-card-rule">{alertRule(alert)}</p>
                  {latest && (
                    <button
                      type="button"
                      className="alert-card-latest"
                      onClick={() => onOpen(latest)}
                    >
                      {at && <span>{shortTime(at, now)}</span>}
                      <span className="alert-card-title">{latest.title}</span>
                    </button>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </section>
      <section className="panel side-card" aria-labelledby="side-sources">
        <div className="side-head">
          <h2 id="side-sources">Sources</h2>
          <button
            type="button"
            className="link-button"
            onClick={() => onSources()}
          >
            Gérer <span className="visually-hidden">les sources</span>
          </button>
        </div>
        {rows.length === 0 ? (
          <p className="side-empty">
            Aucune source.{" "}
            <button
              type="button"
              className="link-button"
              onClick={() => onSources()}
            >
              Ajouter un site
            </button>
          </p>
        ) : (
          <ul className="source-rows">
            {rows.map(({ namespace, connector, state }) => {
              const active =
                filter.kind === "source" && filter.namespace === namespace;
              const c = counts.get(namespace) || { all: 0, caught: 0 };
              const status = state
                ? sourceState(state)
                : { label: "Vos textes", tone: "quiet" as const };
              const renew =
                connector &&
                (state === "access_error" || state === "credential_expiring");
              return (
                <li
                  key={namespace}
                  className="source-mini"
                  data-active={active || undefined}
                >
                  <button
                    type="button"
                    className="source-mini-main"
                    aria-pressed={active}
                    onClick={() =>
                      onFilter(
                        active ? { kind: "all" } : { kind: "source", namespace },
                      )
                    }
                  >
                    <span
                      className="state-dot"
                      data-tone={status.tone}
                      aria-hidden="true"
                    />
                    <span className="source-mini-text">
                      <span className="source-mini-name">
                        {sourceName(namespace)}
                      </span>
                      <span className="source-mini-state" data-tone={status.tone}>
                        {status.label}
                      </span>
                    </span>
                    <span className="source-mini-counts">
                      {c.caught > 0 && (
                        <span
                          className="source-mini-caught"
                          title="Articles attrapés par vos alertes"
                        >
                          <span className="diamond" aria-hidden="true" />
                          {c.caught}
                          <span className="visually-hidden">
                            {" "}
                            attrapé{c.caught > 1 ? "s" : ""} par une alerte,
                          </span>
                        </span>
                      )}
                      <span>
                        {c.all}
                        <span className="visually-hidden">
                          {" "}
                          {plural(c.all, "article").replace(/^\d+ /, "")} dans le
                          fil
                        </span>
                      </span>
                    </span>
                  </button>
                  {renew && (
                    <button
                      type="button"
                      className="button small"
                      onClick={() => onSources(connector.connector_id)}
                    >
                      Renouveler
                      <span className="visually-hidden">
                        {" "}
                        la connexion de {namespace}
                      </span>
                    </button>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </aside>
  );
}
