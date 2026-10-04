// The names people gave their sources. The core names a source by its Source
// Namespace only, which Records and alerts keep using; the facade keeps the
// name shown instead (POST /demo/sources/rename) and returns it with each
// connector. Every label of a source in the app reads it from here.
const names = new Map<string, string>();

/** Takes the names from the connectors the app last read. */
export function rememberSourceNames(connectors: { source_namespace: string; display_name?: string | null }[]) {
  names.clear();
  for (const c of connectors) if (c.display_name) names.set(c.source_namespace, c.display_name);
}

/** The name shown for a Source Namespace. */
export const displayName = (namespace: string) => names.get(namespace) || namespace;
