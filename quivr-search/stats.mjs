// The demo's numbers, counted over every indexed article of the corpus
// (catalog.mjs) rather than over the browser's latest ones. Pure functions:
// the facade and the browser tests' fake engine count the same way.
//
// A row is one article: { record_id, version_id, namespace, at } where `at`
// is when Quivr accepted its current Version, in milliseconds. `matched`
// maps a record id to the ids of the alerts that caught it.

/**
 * The articles a filter keeps, the way the Fil applies it: the period, the
 * read state, alerts and sources, and muted sources out unless picked. `skip`
 * leaves one facet out, so a facet's counts read what picking it would show.
 */
export function filterOf(query, matched, skip) {
  const { after = -Infinity, before = Infinity } = query;
  const sources = new Set(query.sources || []);
  const muted = new Set(query.muted || []);
  const alerts = query.alerts || [];
  const readIds = new Set(query.readIds || []);
  const since = query.since ?? Infinity;
  return (row, period = true) => {
    if (muted.has(row.namespace) && !sources.has(row.namespace)) return false;
    if (period && (row.at < after || row.at >= before)) return false;
    if (skip !== "read" && query.read === "unread" && !unread(row, since, readIds))
      return false;
    if (skip !== "alerts" && alerts.length) {
      const caught = matched.get(row.record_id);
      if (!caught || !alerts.some((id) => caught.includes(id))) return false;
    }
    if (skip !== "sources" && sources.size && !sources.has(row.namespace))
      return false;
    return true;
  };
}

/** Unread in a browser: arrived after its first visit, not opened since. */
export const unread = (row, since, readIds) =>
  row.at > since && !readIds.has(`${row.record_id}:${row.version_id}`);

/**
 * The Fil's numbers for a filter. `total` passes every facet, `all` and
 * `unread` every facet but the read state (Tout, Non lus), `sources` and
 * `alerts` every facet but their own, and `buckets` counts what passes every
 * facet between each two of `query.buckets` (instants, newest first).
 */
export function feedStats(rows, query, matched) {
  const every = filterOf(query, matched);
  const butRead = filterOf(query, matched, "read");
  const butSources = filterOf(query, matched, "sources");
  const butAlerts = filterOf(query, matched, "alerts");
  const since = query.since ?? Infinity;
  const readIds = new Set(query.readIds || []);
  const bounds = query.buckets || [];
  const out = {
    total: 0,
    all: 0,
    unread: 0,
    sources: {},
    alerts: {},
    any_alert: 0,
    buckets: bounds.slice(1).map(() => 0),
  };
  for (const row of rows) {
    if (every(row)) out.total += 1;
    if (butRead(row)) {
      out.all += 1;
      if (unread(row, since, readIds)) out.unread += 1;
    }
    if (butSources(row))
      out.sources[row.namespace] = (out.sources[row.namespace] || 0) + 1;
    if (butAlerts(row)) {
      const caught = matched.get(row.record_id);
      if (caught?.length) {
        out.any_alert += 1;
        for (const id of new Set(caught)) out.alerts[id] = (out.alerts[id] || 0) + 1;
      }
    }
    if (bounds.length > 1 && every(row, false)) {
      const i = bucketOf(bounds, row.at);
      if (i >= 0) out.buckets[i] += 1;
    }
  }
  return out;
}

/** Which [bounds[i+1], bounds[i]) holds a time, or -1. Bounds newest first. */
function bucketOf(bounds, at) {
  if (at >= bounds[0] || at < bounds.at(-1)) return -1;
  let low = 0;
  let high = bounds.length - 1;
  // bounds[low] > at >= bounds[high]: narrow down to adjacent bounds.
  while (high - low > 1) {
    const mid = (low + high) >> 1;
    if (at >= bounds[mid]) high = mid;
    else low = mid;
  }
  return low;
}

/**
 * Each source's numbers on the Sources page: its articles in all, those an
 * alert caught, its articles between each two of `bounds` (newest first),
 * and when its oldest one arrived (`first`, in milliseconds).
 */
export function sourceStats(rows, bounds, matched) {
  const out = {};
  for (const row of rows) {
    const s = (out[row.namespace] ||= {
      all: 0,
      caught: 0,
      days: bounds.slice(1).map(() => 0),
      first: row.at,
    });
    s.all += 1;
    if (row.at < s.first) s.first = row.at;
    if (matched.get(row.record_id)?.length) s.caught += 1;
    const i = bounds.length > 1 ? bucketOf(bounds, row.at) : -1;
    if (i >= 0) s.days[i] += 1;
  }
  return out;
}
