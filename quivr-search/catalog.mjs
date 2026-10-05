// An index of every article of the demo corpus, so the demo's numbers cover
// all of them and not only the latest the feed keeps (THE-1038). It holds
// one small entry per Record: its source, current Version and acceptance
// time. The engine's listing does not return acceptance times, so the index
// lists the catalog one hour at a time (THE-1031's date listing) and dates
// each Record by its hour; the Records the feed reads (its latest articles
// and every live arrival) carry their exact time.
//
// The build is lazy: the first request for a number starts it, once the
// feed has read its latest articles, newest day first, with a few calls at a
// time, and numbers are served from what is indexed meanwhile (`building`). The feed's change stream keeps it current;
// it is rebuilt every few hours, and after the feed had to resynchronize, to
// catch what the stream cannot say.
//
// Topics need titles, which only Versions hold: each is read once, kept by
// Version id (Versions are immutable), and only for the periods asked.
import { describe, instant } from "./feed.mjs";
import { feedStats, filterOf, sourceStats } from "./stats.mjs";
import { topics } from "./topics.mjs";

const HOUR = 3600000;
const DAY = 24 * HOUR;
const CONCURRENCY = 4; // calls to the core at a time, as the day counts
const CHUNK_DAYS = 14;
const MAX_DAYS = 366;
const MAX_RECORDS = 200000;
const REBUILD_MS = 6 * HOUR;
const RETRY_MS = 30000;
const MAX_TITLES = 30000;
const TOPICS = 10;
const TOPICS_FRESH_MS = 15000;
const TOPICS_TTL_MS = 60000;
// While titles are still being read, topics are counted again this often.
const TOPICS_PARTIAL_MS = 2000;
const MAX_CACHED = 100;

async function pool(items, limit, fn) {
  const queue = [...items];
  await Promise.all(
    Array.from({ length: Math.min(limit, queue.length) }, async () => {
      for (let item; (item = queue.shift()) !== undefined; ) await fn(item);
    }),
  );
}

const utcDay = (t) => t - (t % DAY);

// What a browser may ask for. Times are RFC 3339 instants, as for day counts.
const invalid = () =>
  Object.assign(new Error("Cette période n’est pas valide."), { status: 422 });
const time = (value) => {
  const t = instant(value);
  if (!t) throw invalid();
  return Date.parse(t);
};
const strings = (value, max) => {
  if (value === undefined) return [];
  if (
    !Array.isArray(value) ||
    value.length > max ||
    value.some((s) => typeof s !== "string" || !s || s.length > 256)
  )
    throw invalid();
  return value;
};
const MAX_BOUNDS = 26;
const MAX_FACETS = 100;
const MAX_READ = 2000;

/** Instants newest first, as "t0,t1,…" or a list: at most 26, decreasing. */
export function boundsOf(value) {
  if (value === undefined || value === null || value === "") return [];
  const list = typeof value === "string" ? value.split(",") : value;
  if (!Array.isArray(list) || list.length > MAX_BOUNDS) throw invalid();
  const times = list.map(time);
  if (times.some((t, i) => i && t >= times[i - 1])) throw invalid();
  return times;
}
function period(after, before) {
  const out = {};
  if (after !== undefined && after !== null) out.after = time(after);
  if (before !== undefined && before !== null) out.before = time(before);
  if (out.after !== undefined && out.before !== undefined && out.after >= out.before)
    throw invalid();
  return out;
}

/** The body of POST /demo/feed/stats. */
export function statsQuery(body) {
  if (!body || typeof body !== "object") throw invalid();
  if (body.read !== undefined && body.read !== "all" && body.read !== "unread")
    throw invalid();
  return {
    ...period(body.after, body.before),
    buckets: boundsOf(body.buckets),
    sources: strings(body.sources, MAX_FACETS),
    muted: strings(body.muted, MAX_FACETS),
    alerts: strings(body.alerts, MAX_FACETS),
    read: body.read || "all",
    since: body.since === undefined ? undefined : time(body.since),
    readIds: strings(body.read_ids, MAX_READ),
  };
}

/** The query of GET /demo/feed/topics: ?after&before, sources, muted, alerts. */
export function topicsQuery(params) {
  if (!params.get("after") || !params.get("before")) throw invalid();
  const list = (name) => strings(params.getAll(name), MAX_FACETS).sort();
  return {
    ...period(params.get("after"), params.get("before")),
    sources: list("source"),
    muted: list("muted"),
    alerts: list("alert"),
  };
}

export function createCatalog({ upstream, corpus, caught, ready = async () => {} }) {
  // record id → { record_id, version_id, namespace, at, exact, seen }
  const entries = new Map();
  const titles = new Map();
  const wanted = new Map();
  let generation = 0;
  let build = null;
  let builtAt = 0;
  let failedAt = 0;
  let stale = false;
  // The index is whole for every article accepted at or after this time.
  let completeAfter = Infinity;
  let pumping = false;

  const query = (extra) =>
    new URLSearchParams({ corpus_id: corpus, ...extra }).toString();
  const iso = (t) => new Date(t).toISOString();

  async function count(after, before) {
    const extra = {};
    if (after !== undefined) extra.accepted_after = iso(after);
    if (before !== undefined) extra.accepted_before = iso(before);
    const response = await upstream(`/v0/records/count?${query(extra)}`);
    if (response.status !== 200) throw new Error(`count: HTTP ${response.status}`);
    return response.data.count;
  }

  // Every Record accepted in one hour, dated by that hour unless the index
  // already knows its exact time for the same Version.
  async function listHour(start, round) {
    let cursor;
    do {
      const extra = {
        order: "accepted_at_desc",
        accepted_after: iso(start),
        accepted_before: iso(start + HOUR),
        limit: "100",
      };
      if (cursor) extra.page_cursor = cursor;
      const response = await upstream(`/v0/records?${query(extra)}`);
      if (response.status !== 200) throw new Error(`list: HTTP ${response.status}`);
      for (const record of response.data.items) {
        if (record.source?.corpus_id !== corpus) continue;
        if (record.withdrawn || !record.current_version_id) {
          if (entries.delete(record.record_id)) generation += 1;
          continue;
        }
        const known = entries.get(record.record_id);
        if (known?.version_id === record.current_version_id) {
          known.seen = round;
          continue;
        }
        if (!known && entries.size >= MAX_RECORDS) continue;
        entries.set(record.record_id, {
          record_id: record.record_id,
          version_id: record.current_version_id,
          namespace: record.source?.namespace || "",
          at: start,
          exact: false,
          seen: round,
        });
        generation += 1;
      }
      cursor = response.data.next_page_cursor;
    } while (cursor);
  }

  // Newest day first: a day's hours are listed once Quivr counts articles
  // in it, and the index is whole down to that day once it is done.
  async function run() {
    // The feed's first catalog scan goes first: the page shows sooner, and
    // the index starts with the latest articles' exact times and titles.
    await ready();
    const round = Date.now();
    const first = !builtAt;
    let top = utcDay(round) + DAY;
    for (let done = 0; done < MAX_DAYS; done += CHUNK_DAYS) {
      const days = Array.from({ length: CHUNK_DAYS }, (_, i) => top - (i + 1) * DAY);
      const counts = new Map();
      let older = 0;
      await pool([...days, "older"], CONCURRENCY, async (day) => {
        if (day === "older") older = await count(undefined, days.at(-1));
        else counts.set(day, await count(day, day + DAY));
      });
      for (const day of days) {
        if (counts.get(day)) {
          const hours = Array.from({ length: 24 }, (_, i) => day + (23 - i) * HOUR).filter(
            (hour) => hour <= round,
          );
          await pool(hours, CONCURRENCY, (hour) => listHour(hour, round));
        }
        if (first) completeAfter = day;
      }
      top = days.at(-1);
      if (!older || entries.size >= MAX_RECORDS) break;
    }
    // What the listing no longer holds, and the feed did not note since,
    // was withdrawn or left the corpus.
    for (const [id, entry] of entries)
      if (entry.seen !== round && !(entry.exact && entry.noted >= round)) {
        entries.delete(id);
        generation += 1;
      }
    completeAfter = -Infinity;
    builtAt = Date.now();
    stale = false;
  }

  /** Starts the build when none ran, or rebuilds an old or stale index. */
  function ensure() {
    const now = Date.now();
    if (build) return;
    if (builtAt && !stale && now - builtAt < REBUILD_MS) return;
    if (failedAt && now - failedAt < RETRY_MS) return;
    build = run()
      .catch((error) => {
        failedAt = Date.now();
        console.warn(`Index: build interrupted (${error.message})`);
      })
      .finally(() => {
        build = null;
      });
  }

  const status = () => ({
    building: !!build || !builtAt,
    complete_after: Number.isFinite(completeAfter) ? iso(Math.max(completeAfter, 0)) : null,
  });

  // Titles of the Versions asked for and not known yet, read in the
  // background a few at a time.
  async function pump() {
    if (pumping) return;
    pumping = true;
    try {
      while (wanted.size) {
        const batch = [...wanted.values()].slice(0, CONCURRENCY * 4);
        await pool(batch, CONCURRENCY, async (entry) => {
          wanted.delete(entry.version_id);
          const response = await upstream(
            `/v0/records/${encodeURIComponent(entry.record_id)}/versions/${encodeURIComponent(entry.version_id)}`,
          ).catch(() => ({ status: 503 }));
          if (response.status !== 200) return;
          remember(entry.version_id, describe({ record_id: entry.record_id }, response.data).title);
          const exact = Date.parse(response.data.accepted_at);
          const known = entries.get(entry.record_id);
          if (known?.version_id === entry.version_id && !Number.isNaN(exact)) {
            known.at = exact;
            known.exact = true;
          }
        });
        generation += 1;
      }
    } finally {
      pumping = false;
    }
  }
  function remember(version, title) {
    titles.delete(version);
    titles.set(version, title);
    if (titles.size > MAX_TITLES) titles.delete(titles.keys().next().value);
  }

  // Matches of the demo's alerts, by record id.
  async function matchedMap() {
    try {
      const map = await caught();
      return map instanceof Map ? map : new Map(Object.entries(map || {}));
    } catch {
      return new Map();
    }
  }

  const topicsCache = new Map();
  async function topicsOf(q, key) {
    const now = Date.now();
    const hit = topicsCache.get(key);
    const age = hit ? now - hit.at : Infinity;
    if (
      hit &&
      (hit.value.building
        ? age < TOPICS_PARTIAL_MS
        : age < TOPICS_FRESH_MS || (age < TOPICS_TTL_MS && hit.generation === generation))
    )
      return hit.value;
    const matched = q.alerts.length ? await matchedMap() : new Map();
    const keep = filterOf({ ...q, read: "all" }, matched);
    const known = [];
    let missing = 0;
    for (const entry of entries.values()) {
      if (!keep(entry)) continue;
      const title = titles.get(entry.version_id);
      if (title !== undefined) known.push(title);
      else {
        missing += 1;
        if (wanted.size < MAX_TITLES) wanted.set(entry.version_id, entry);
      }
    }
    if (missing) void pump();
    const value = {
      items: topics(known, TOPICS),
      ...status(),
      building: status().building || missing > 0,
      as_of: iso(now),
    };
    topicsCache.delete(key);
    topicsCache.set(key, { at: now, generation, value });
    if (topicsCache.size > MAX_CACHED) topicsCache.delete(topicsCache.keys().next().value);
    return value;
  }

  return {
    /** An article the feed read: its exact time, title and current Version. */
    note(item) {
      const at = Date.parse(item.received_at || "");
      if (Number.isNaN(at)) return;
      const known = entries.get(item.record_id);
      if (!known && entries.size >= MAX_RECORDS) return;
      entries.set(item.record_id, {
        record_id: item.record_id,
        version_id: item.version_id,
        namespace: item.namespace,
        at,
        exact: true,
        noted: Date.now(),
        seen: known?.seen,
      });
      remember(item.version_id, item.title);
      generation += 1;
    },
    /** A Record withdrawn, or gone from the corpus. */
    drop(id) {
      if (entries.delete(id)) generation += 1;
    },
    /** The feed resynchronized: what the stream missed shows at the next rebuild. */
    invalidate() {
      stale = true;
    },
    /** POST /demo/feed/stats: the Fil's counts for a filter. */
    async feed(q) {
      ensure();
      const matched = await matchedMap();
      return { ...feedStats(entries.values(), q, matched), ...status() };
    },
    /** GET /demo/feed/topics: the topics of a period. */
    async topics(q) {
      ensure();
      return topicsOf(q, JSON.stringify(q));
    },
    /** GET /demo/sources/stats: each source's numbers. */
    async sources(bounds) {
      ensure();
      const matched = await matchedMap();
      return { sources: sourceStats(entries.values(), bounds, matched), ...status() };
    },
    /**
     * The time and source of each article the alerts caught, and how many
     * articles arrived since each alert started (or since the oldest indexed).
     */
    alerts(list) {
      ensure();
      const namespaces = [];
      const index = new Map();
      const records = {};
      for (const id of Object.keys(list.matched || {})) {
        const entry = entries.get(id);
        if (!entry) continue;
        if (!index.has(entry.namespace)) {
          index.set(entry.namespace, namespaces.length);
          namespaces.push(entry.namespace);
        }
        records[id] = [entry.version_id, index.get(entry.namespace), Math.round(entry.at / 1000)];
      }
      let oldest = Infinity;
      for (const entry of entries.values()) if (entry.at < oldest) oldest = entry.at;
      const starts = list.items.map((alert) => {
        const created = Date.parse(alert.created_at || "");
        return Number.isNaN(created) ? oldest : created;
      });
      const arrived = starts.map(() => 0);
      for (const entry of entries.values())
        starts.forEach((start, i) => {
          if (entry.at >= start) arrived[i] += 1;
        });
      return {
        ...list,
        items: list.items.map((alert, i) => ({ ...alert, arrived: arrived[i] })),
        dated: {
          namespaces,
          records,
          oldest: Number.isFinite(oldest) ? iso(oldest) : null,
          ...status(),
        },
      };
    },
  };
}
