import type { Connector, ConnectorKind } from "../../lib/connectors";

/**
 * A short human identification of the source: the first text setting in the
 * kind's schema order (a feed URL, a mailbox, a list ID...). Derived from the
 * schema so no kind is named here.
 */
export function sourceSummary(connector: Connector, kind?: ConnectorKind) {
  const order = [
    ...Object.keys(kind?.config_schema.properties || {}),
    ...Object.keys(connector.config),
  ];
  for (const name of order) {
    const value = connector.config[name];
    if (typeof value === "string" && value) return value;
  }
  return "";
}
