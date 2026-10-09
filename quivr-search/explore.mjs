// The Explorer (THE-1171): browse the documents of the corpora the demo
// reads, newest first, narrowed by metadata, and open one with all it holds.
//
// Fields come from two places. The nine common fields of the reserved
// `quivr.metadata` extension apply to every corpus (`metadata.language`…);
// a corpus adds its own typed mappings declared with the `filter` role,
// read through their `source_pointer`. The listing filters with the engine's
// metadata predicates and relays which corpora a field excluded.
//
// Facet values go through one seam, `facets()`, counted by the engine
// (`POST /v0/facets`, THE-1184) under the same corpora and predicates as the
// list. A date field gives a histogram whose step suits the period picked;
// the publication date is the page's timeline (THE-1204), counted outside the
// range it picks, within the span it shows. The browser asks one field at a
// time; a fast count (THE-1387) may come from the engine's stored counts,
// dated, or from a sample, approximate, and says so.
//
// A text query searches the same corpora under the same predicates
// (`POST /v0/search`), its documents shown as the list's rows.
//
// The engine lists no Record's Versions: the history shows those the demo
// read since it started (the feeds reread each new Version), bounded.
import { describe } from "./feed.mjs";

export const COMMON_FIELDS = [
  ["language", "string"],
  ["published_at", "datetime"],
  ["source_type", "string"],
  ["source", "string"],
  ["author", "string_array"],
  ["subjects", "string_array"],
  ["tags", "string_array"],
  ["country", "string_array"],
  ["place", "string_array"],
].map(([name, type]) => ({
  name: `metadata.${name}`,
  type,
  source_pointer: `/extensions/quivr.metadata/data/${name}`,
}));

const PAGE = 25;
// A search shows its best documents, this many hits at most.
const SEARCH_LIMIT = 50;
const QUERY_CHARS = 500;
// The field the timeline counts.
export const TIMELINE_FIELD = "metadata.published_at";
// The engine returns at most this many values per field, the most frequent.
const FACET_VALUES = 100;
// Counting may take the engine up to 25 s, and it admits eight counts at
// once per instance: one Explorer view sends at most three.
const FACET_TIMEOUT_MS = 30000;
const FACET_CONCURRENCY = 3;
// The engine counts at most this many fields per request.
const FACET_FIELDS = 16;
// Unpicked dates: months, or days when they span at most this many months,
// or years when they span more than this many.
const DAYS_UP_TO_MONTHS = 2;
const YEARS_FROM_MONTHS = 36;
const MAX_VERSIONS = 5000;
const CORPUS_TTL_MS = 60000;
const MAX_BLOBS = 5;
const HYDRATE_CONCURRENCY = 6;
const MAX_HISTORY_RECORDS = 20000;
const MAX_HISTORY_VERSIONS = 50;
const FIELD = /^(metadata\.)?[a-z][a-z0-9_]{0,63}$/;
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$/;

const failure = (status, message, code) =>
  Object.assign(new Error(message), { status, ...(code ? { code } : {}) });

/** The value at a JSON Pointer (RFC 6901) in a document, or undefined. */
export function pointerValue(doc, pointer) {
  if (typeof pointer !== "string" || !pointer.startsWith("/")) return undefined;
  let at = doc;
  for (const raw of pointer.slice(1).split("/")) {
    const key = raw.replace(/~1/g, "/").replace(/~0/g, "~");
    if (at === null || typeof at !== "object" || !Object.hasOwn(at, key)) return undefined;
    at = at[key];
  }
  return at;
}

/** A field's values in a Version, as a list of scalars (an array field gives each element). */
export function fieldValues(version, field) {
  const value = pointerValue(version, field.source_pointer);
  const list = Array.isArray(value) ? value : value === undefined || value === null ? [] : [value];
  return list.filter((v) => ["string", "number", "boolean"].includes(typeof v));
}

/** Every field of a corpus a filter can use: the common ones, then its own. */
export function fieldsOf(corpus) {
  const own = (corpus?.effective_retrieval?.fields || [])
    .filter((f) => Array.isArray(f.roles) && f.roles.includes("filter") && FIELD.test(f.name || ""))
    .map(({ name, type, source_pointer }) => ({ name, type, source_pointer }));
  return { common: COMMON_FIELDS, own };
}

/**
 * The metadata predicates a browser sends (?metadata=<JSON array>), checked
 * for shape only: the engine judges types and values.
 */
export function predicatesOf(raw) {
  if (!raw) return [];
  let list;
  try {
    list = JSON.parse(raw);
  } catch {
    throw failure(422, "Ce filtre n’est pas valide.");
  }
  const scalar = (v) =>
    (typeof v === "string" && v.length <= 200) || typeof v === "number" || typeof v === "boolean";
  if (
    !Array.isArray(list) ||
    list.length > 16 ||
    new Set(list.map((p) => p?.field)).size !== list.length ||
    !list.every(
      (p) =>
        p &&
        typeof p === "object" &&
        FIELD.test(p.field || "") &&
        Object.keys(p).every((k) => ["field", "any_of", "gte", "lte"].includes(k)) &&
        (p.any_of === undefined ||
          (Array.isArray(p.any_of) && p.any_of.length >= 1 && p.any_of.length <= 50 && p.any_of.every(scalar))) &&
        (p.gte === undefined || RFC3339.test(p.gte)) &&
        (p.lte === undefined || RFC3339.test(p.lte)) &&
        (p.any_of || p.gte || p.lte),
    )
  )
    throw failure(422, "Ce filtre n’est pas valide.");
  return list;
}

/**
 * The span the timeline shows (?window=<from>,<to>, RFC 3339), checked for
 * shape and order; undefined when absent.
 */
export function windowOf(raw) {
  if (!raw) return undefined;
  const [gte, lte, ...rest] = raw.split(",");
  if (rest.length || !RFC3339.test(gte || "") || !RFC3339.test(lte || "") || !(Date.parse(gte) < Date.parse(lte)))
    throw failure(422, "Cette période n’est pas valide.");
  return { gte, lte };
}

const PERIOD = { year: 4, month: 7, day: 10 };

// An instant in UTC; Date.UTC would read the years 0–99 as 1900–1999.
const utc = (year, month, day) => new Date(0).setUTCFullYear(year, month, day);

/** The UTC instants that bound a period: "2026", "2026-10" or "2026-10-05". */
export function periodBounds(period) {
  const [year, month = 1, day = 1] = period.split("-").map(Number);
  const start = utc(year, month - 1, day);
  const next =
    period.length === PERIOD.year
      ? utc(year + 1, 0, 1)
      : period.length === PERIOD.month
        ? utc(year, month, 1)
        : utc(year, month - 1, day + 1);
  return { gte: new Date(start).toISOString(), lte: new Date(next - 1).toISOString() };
}

/** The period a date predicate picks, if its bounds are exactly a year, a month or a day. */
function periodOf(predicate) {
  if (!predicate?.gte || !predicate?.lte || predicate.any_of) return undefined;
  const start = Date.parse(predicate.gte);
  if (Number.isNaN(start)) return undefined;
  const iso = new Date(start).toISOString();
  return Object.values(PERIOD)
    .map((length) => iso.slice(0, length))
    .find((period) => {
      const bounds = periodBounds(period);
      return Date.parse(bounds.gte) === start && Date.parse(bounds.lte) === Date.parse(predicate.lte);
    });
}

/**
 * How a date field is counted: its step, and the predicate its own count
 * keeps. A year picked shows its months, a month its days, a day the days of
 * its month so the others stay offered; nothing picked starts with months.
 */
function histogramOf(predicate) {
  if (!predicate) return { interval: "month", scope: undefined, adapt: true };
  const period = periodOf(predicate);
  if (period?.length === PERIOD.year) return { interval: "month", scope: predicate };
  if (period?.length === PERIOD.month) return { interval: "day", scope: predicate };
  if (period) return { interval: "day", scope: { field: predicate.field, ...periodBounds(period.slice(0, PERIOD.month)) } };
  // Bounds of another span: the step that keeps it readable.
  return { interval: stepOf(predicate), scope: predicate };
}

/** The step that keeps a span readable: days within two months, months within three years. */
function stepOf({ gte, lte }) {
  const days = (Date.parse(lte || gte) - Date.parse(gte || lte)) / 864e5;
  return days <= 62 ? "day" : days <= 3 * 366 ? "month" : "year";
}

/**
 * How the timeline is counted: outside the range it picks, so the documents
 * around it stay drawn, and within the span it shows, at the step that span
 * asks; unbounded, it starts with months as an unpicked date does.
 */
function timelineOf(field, window) {
  if (!window) return { interval: "month", scope: undefined, adapt: true };
  return { interval: stepOf(window), scope: { field, ...window } };
}

const monthIndex = (value) => Number(value.slice(0, 4)) * 12 + Number(value.slice(5, 7));

/**
 * Each field's values counted by the engine, most frequent first; a date
 * field's are periods in time order ("2026", "2026-10", "2026-10-05"). A
 * field's own predicate is left out of its count, so its other values stay
 * offered; a date keeps the period it zooms in. The timeline's field
 * (`timeline.field`) leaves its whole range out and keeps the span shown
 * (`timeline.window`), and `total` then counts the dated documents every
 * predicate keeps. `count` posts one `POST /v0/facets` body and returns the
 * engine's answer. The corpora excluded are those of the common fields'
 * count under every predicate, as the list's: a corpus's own fields, counted
 * apart, 16 at a time, exclude nothing the page would announce.
 */
export async function countFacets({ count, ids, fields, predicates, timeline, accuracy }) {
  const own = new Map(predicates.map((p) => [p.field, p]));
  const histograms = new Map(
    fields
      .filter((f) => f.type === "datetime")
      .map((f) => [
        f.name,
        f.name === timeline?.field ? timelineOf(f.name, timeline.window) : histogramOf(own.get(f.name)),
      ]),
  );
  const ask = (names, kept, intervals = {}) =>
    count({
      corpus_ids: ids,
      fields: names.map((field) => {
        const interval = intervals[field] || histograms.get(field)?.interval;
        return { field, limit: FACET_VALUES, ...(interval ? { interval } : {}) };
      }),
      ...(kept.length ? { filter: { metadata: kept } } : {}),
      ...(accuracy === "fast" ? { accuracy } : {}),
    }).then((answer) => {
      counted.push(answer);
      return answer;
    });
  // Every answer, for how its counts were made.
  const counted = [];
  // Fields counted under every predicate, and each other one under its own.
  const shared = [];
  const alone = [];
  const keptBy = new Map();
  for (const field of fields) {
    const p = own.get(field.name);
    const histogram = histograms.get(field.name);
    if ((!p && !histogram?.scope) || (histogram && histogram.scope === p)) {
      shared.push(field.name);
      keptBy.set(field.name, predicates);
    } else {
      const kept = [...predicates.filter((x) => x !== p), ...(histogram?.scope ? [histogram.scope] : [])];
      alone.push({ field: field.name, kept });
      keptBy.set(field.name, kept);
    }
  }
  // The common fields' count under every predicate also names the corpora
  // the predicates exclude.
  const common = shared.filter((name) => name.startsWith("metadata."));
  const ownShared = shared.filter((name) => !name.startsWith("metadata."));
  // The timeline counted apart: its total is its years under every predicate.
  const totalApart = timeline && !shared.includes(timeline.field);
  // Without a common field shared, as when one field is asked alone, the
  // list names the corpora the predicates exclude: no extra count.
  const tasks = [
    ...(common.length ? [() => ask(common, predicates)] : []),
    ...Array.from({ length: Math.ceil(ownShared.length / FACET_FIELDS) }, (_, i) => () =>
      ask(ownShared.slice(i * FACET_FIELDS, (i + 1) * FACET_FIELDS), predicates),
    ),
    ...alone.map(({ field, kept }) => () => ask([field], kept)),
    ...(totalApart ? [() => ask([timeline.field], predicates, { [timeline.field]: "year" })] : []),
  ];
  // A failed count stops the others from starting; those running end first.
  const answers = [];
  let next = 0;
  let failed = false;
  const workers = await Promise.allSettled(
    Array.from({ length: Math.min(FACET_CONCURRENCY, tasks.length) }, async () => {
      for (let i; !failed && (i = next++) < tasks.length; )
        answers[i] = await tasks[i]().catch((error) => {
          failed = true;
          throw error;
        });
    }),
  );
  const rejected = workers.find((w) => w.status === "rejected");
  if (rejected) throw rejected.reason;
  const all = common.length ? answers[0] : undefined;
  const years = totalApart ? answers.pop() : undefined;
  const others = answers.slice(common.length ? 1 : 0);
  const buckets = new Map();
  for (const item of [...(all?.items || []), ...others.flatMap((o) => o.items)])
    buckets.set(item.field, item.buckets);
  const intervals = new Map([...histograms].map(([name, h]) => [name, h.interval]));

  // Unbounded dates over a short span are counted again by day; over a long
  // one, by year: summed from the months, or asked again when the months
  // were cut at the engine's bound. Each keeps the predicates it was counted under.
  const again = [];
  for (const [name, histogram] of histograms) {
    if (!histogram.adapt) continue;
    const months = buckets.get(name) || [];
    if (!months.length) continue;
    const span = monthIndex(months.at(-1).value) - monthIndex(months[0].value) + 1;
    if (span <= DAYS_UP_TO_MONTHS) again.push([name, "day"]);
    else if (months.length >= FACET_VALUES) again.push([name, "year"]);
    else if (span > YEARS_FROM_MONTHS) {
      const years = new Map();
      for (const b of months) years.set(b.value.slice(0, 4), (years.get(b.value.slice(0, 4)) || 0) + b.count);
      buckets.set(name, [...years].map(([value, n]) => ({ value: `${value}-01-01T00:00:00Z`, count: n })));
      intervals.set(name, "year");
    }
  }
  for (const [name, interval] of again) {
    const data = await ask([name], keptBy.get(name), { [name]: interval });
    for (const item of data.items) {
      buckets.set(item.field, item.buckets);
      intervals.set(item.field, interval);
    }
  }

  const sum = (list = []) => list.reduce((n, b) => n + b.count, 0);
  // The oldest stored counts date them all; one estimate makes them all approximate.
  const asOf = counted
    .map((a) => a.as_of)
    .filter((t) => t && !Number.isNaN(Date.parse(t)))
    .sort((a, b) => Date.parse(a) - Date.parse(b))[0];
  return {
    fields: fields.map((field) => {
      const interval = intervals.get(field.name);
      const values = (buckets.get(field.name) || []).map(({ value, count: n }) => ({
        value: interval ? String(value).slice(0, PERIOD[interval]) : value,
        count: n,
      }));
      return { field: field.name, type: field.type, ...(interval ? { interval } : {}), values };
    }),
    ...(all?.excluded_corpora?.length ? { excluded_corpora: all.excluded_corpora } : {}),
    ...(timeline
      ? { total: years ? sum(years.items[0]?.buckets) : sum(buckets.get(timeline.field)) }
      : {}),
    ...(asOf ? { as_of: asOf } : {}),
    ...(counted.some((a) => a.approximate) ? { approximate: true } : {}),
  };
}

/** The Versions the demo read of each Record, newest last, bounded. */
export function createHistory() {
  const records = new Map();
  return {
    note(record, version, acceptedAt) {
      if (!record || !version) return;
      const list = records.get(record) || [];
      if (!list.some((v) => v.version_id === version)) {
        list.push({ version_id: version, ...(acceptedAt ? { accepted_at: acceptedAt } : {}) });
        list.sort((a, b) => (a.accepted_at || "").localeCompare(b.accepted_at || ""));
        if (list.length > MAX_HISTORY_VERSIONS) list.shift();
      }
      records.delete(record);
      records.set(record, list);
      if (records.size > MAX_HISTORY_RECORDS) records.delete(records.keys().next().value);
    },
    of: (record) => [...(records.get(record) || [])],
  };
}

export function createExplorer({ upstream, readable, picked, demo, history }) {
  // Corpora as the engine describes them, read again after a minute.
  const corpora = new Map();
  async function corpus(id) {
    const known = corpora.get(id);
    if (known && Date.now() - known.at < CORPUS_TTL_MS) return known.value;
    const response = await upstream(`/v0/corpora/${encodeURIComponent(id)}`);
    if (response.status >= 500) throw failure(503, "Le moteur est momentanément indisponible. Réessayez.");
    // A corpus the key cannot read, or that is gone, is not offered; any
    // other answer is passing, and the corpus is described again later.
    if (response.status === 403 || response.status === 404) throw failure(404, "Corpus introuvable.");
    if (response.status !== 200) throw failure(503, "Le moteur est momentanément indisponible. Réessayez.");
    const data = response.data;
    const value = {
      corpus_id: id,
      name: data.name || (id === demo() ? "Espace démo" : id),
      demo: id === demo(),
      ...fieldsOf(data),
    };
    corpora.set(id, { at: Date.now(), value });
    return value;
  }

  // How many documents a corpus holds, for the corpus switcher only, read
  // again after a minute; a count the engine cannot give now is left out.
  const counts = new Map();
  async function documents(id) {
    const known = counts.get(id);
    if (known && Date.now() - known.at < CORPUS_TTL_MS) return known.value;
    const response = await upstream(`/v0/records/count?corpus_id=${encodeURIComponent(id)}`).catch(() => null);
    const value = response?.status === 200 && Number.isInteger(response.data?.count) ? response.data.count : undefined;
    if (value !== undefined) counts.set(id, { at: Date.now(), value });
    return value;
  }

  // Versions are immutable: each is read once and kept, the latest MAX_VERSIONS.
  const versions = new Map();
  async function version(record, id) {
    if (versions.has(id)) return versions.get(id);
    const response = await upstream(
      `/v0/records/${encodeURIComponent(record)}/versions/${encodeURIComponent(id)}`,
    );
    if (response.status === 404) return null;
    if (response.status !== 200) throw failure(503, "Ces documents sont momentanément indisponibles. Réessayez.");
    versions.set(id, response.data);
    if (versions.size > MAX_VERSIONS) versions.delete(versions.keys().next().value);
    history.note(record, id, response.data.accepted_at);
    return response.data;
  }

  // One listing page of the corpora under the predicates, newest first.
  async function list(ids, predicates, limit, cursor) {
    const query = new URLSearchParams({
      corpus_ids: ids.join(","),
      order: "accepted_at_desc",
      limit: String(limit),
    });
    if (predicates.length) query.set("metadata", JSON.stringify(predicates));
    if (cursor) query.set("page_cursor", cursor);
    const response = await upstream(`/v0/records?${query}`);
    if (response.status === 422 && response.data?.code === "metadata_filter_unavailable")
      throw failure(
        422,
        "Un corpus choisi ne peut pas encore être filtré par métadonnées : il doit être reconstruit.",
        "metadata_filter_unavailable",
      );
    if (response.status === 409) throw failure(409, "La liste a changé. Rechargez-la.");
    if (response.status === 422) throw failure(422, "Ce filtre n’est pas valide.");
    if (response.status !== 200) throw failure(503, "Ces documents sont momentanément indisponibles. Réessayez.");
    return response.data;
  }

  // The best documents for a text under the predicates, as listing rows
  // would carry them: one per Record, in rank order.
  async function search(ids, predicates, query) {
    const response = await upstream("/v0/search", "POST", {
      query,
      corpus_ids: ids,
      limit: SEARCH_LIMIT,
      ...(predicates.length ? { filter: { metadata: predicates } } : {}),
    });
    if (response.status === 422 && response.data?.code === "metadata_filter_unavailable")
      throw failure(
        422,
        "Un corpus choisi ne peut pas encore être filtré par métadonnées : il doit être reconstruit.",
        "metadata_filter_unavailable",
      );
    if (response.status === 422 && response.data?.code === "query_too_long")
      throw failure(422, "Cette recherche est trop longue.");
    if (response.status === 422 && response.data?.code === "unsupported_search")
      throw failure(422, "La recherche par sens est indisponible. Réessayez par mots-clés.", "unsupported_search");
    if (response.status === 422) throw failure(422, "Cette recherche n’est pas valide.");
    if (response.status !== 200) throw failure(503, "La recherche est momentanément indisponible. Réessayez.");
    const seen = new Set();
    const hits = [...(response.data.items || [])]
      .sort((a, b) => a.rank - b.rank)
      .filter((hit) => !seen.has(hit.record_id) && seen.add(hit.record_id));
    const records = [];
    const queue = [...hits.entries()];
    await Promise.all(
      Array.from({ length: HYDRATE_CONCURRENCY }, async () => {
        for (let next; (next = queue.shift()); ) {
          const [index, hit] = next;
          const source = await sourceOf(hit.record_id);
          if (source) records[index] = { record_id: hit.record_id, source, withdrawn: false, current_version_id: hit.version_id };
        }
      }),
    );
    return {
      items: records.filter(Boolean),
      excluded_corpora: response.data.excluded_corpora,
      retrieval_profile: response.data.retrieval_profile,
      // As many passages as asked: more documents may match.
      bounded: (response.data.items || []).length >= SEARCH_LIMIT,
    };
  }

  // A Record's source identity, which never changes: read once, the latest kept.
  const sources = new Map();
  async function sourceOf(record) {
    if (sources.has(record)) return sources.get(record);
    const response = await upstream(`/v0/records/${encodeURIComponent(record)}`);
    if (response.status === 404 || response.status === 403) return null;
    if (response.status !== 200) throw failure(503, "Ces documents sont momentanément indisponibles. Réessayez.");
    sources.set(record, response.data.source);
    if (sources.size > MAX_VERSIONS) sources.delete(sources.keys().next().value);
    return response.data.source;
  }

  // One count of field values, as the engine answers it.
  async function count(body, signal) {
    const response = await upstream("/v0/facets", "POST", body, FACET_TIMEOUT_MS, signal);
    if (response.status === 422 && response.data?.code === "metadata_filter_unavailable")
      throw failure(
        422,
        "Un corpus choisi doit être reconstruit avant que ses valeurs soient comptées.",
        "metadata_filter_unavailable",
      );
    if (response.status === 422) throw failure(422, "Ce filtre n’est pas valide.");
    if (response.status !== 200) throw failure(503, "Les nombres sont momentanément indisponibles. Réessayez.");
    return response.data;
  }

  // The current Version of each listed Record, in order; withdrawn ones left out.
  async function hydrate(records) {
    const out = [];
    const queue = records
      .filter((r) => !r.withdrawn && r.current_version_id)
      .map((record, index) => ({ record, index }));
    await Promise.all(
      Array.from({ length: HYDRATE_CONCURRENCY }, async () => {
        for (let next; (next = queue.shift()); ) {
          const data = await version(next.record.record_id, next.record.current_version_id);
          if (data) out[next.index] = { record: next.record, version: data };
        }
      }),
    );
    return out.filter(Boolean);
  }

  // A document as the list shows it: its title, text start, date and every field's values.
  function row({ record, version: data }, info) {
    const item = describe(record, data, data.accepted_at);
    const metadata = {};
    for (const field of [...info.common, ...info.own]) {
      const values = fieldValues(data, field);
      if (values.length) metadata[field.name] = values;
    }
    // Its place among the Versions the demo read of it: 1 for the first.
    const known = history.of(record.record_id);
    const at = known.findIndex((v) => v.version_id === data.version_id);
    return { ...item, metadata, version: at >= 0 ? at + 1 : Math.max(1, known.length) };
  }

  return {
    /** GET /demo/corpora: the corpora the demo reads and their fields. */
    async corpora() {
      // The configured names are looked up again: a corpus recreated under
      // its name shows after a reload. A corpus the engine cannot describe
      // now is listed by its id with the common fields; it is read again on
      // the next call.
      const fallback = (id) => ({
        corpus_id: id,
        name: id === demo() ? "Espace démo" : id,
        demo: id === demo(),
        common: COMMON_FIELDS,
        own: [],
      });
      return {
        items: (
          await Promise.all(
            (await readable({ fresh: true })).map(async (id) => {
              // One gone since it was looked up is left out.
              const described = await corpus(id).catch((error) => (error.status === 404 ? null : fallback(id)));
              if (!described) return null;
              const count = await documents(id);
              return count === undefined ? described : { ...described, documents: count };
            }),
          )
        ).filter(Boolean),
      };
    },
    /** GET /demo/explore: a page of documents, newest first, or a search's best (?q=). */
    async page(params) {
      const ids = await picked(params);
      const predicates = predicatesOf(params.get("metadata"));
      const query = (params.get("q") || "").trim();
      if (query.length > QUERY_CHARS) throw failure(422, "Cette recherche est trop longue.");
      const data = query ? await search(ids, predicates, query) : await list(ids, predicates, PAGE, params.get("cursor"));
      const infos = new Map((await Promise.all(ids.map(corpus))).map((c) => [c.corpus_id, c]));
      const rows = await hydrate(data.items || []);
      const page = {
        items: rows.map((r) => row(r, infos.get(r.record.source?.corpus_id) || { common: COMMON_FIELDS, own: [] })),
      };
      if (data.next_page_cursor) page.next_cursor = data.next_page_cursor;
      if (data.excluded_corpora?.length) page.excluded_corpora = data.excluded_corpora;
      if (data.retrieval_profile) page.retrieval_profile = data.retrieval_profile;
      if (data.bounded) page.bounded = true;
      return page;
    },
    /**
     * GET /demo/explore/facets: each field's values and how many documents
     * have them, or one field's (?field=). ?accuracy=fast lets the engine
     * answer from its stored counts or a sample.
     */
    async facets(params, signal) {
      const ids = await picked(params);
      const predicates = predicatesOf(params.get("metadata"));
      const accuracy = params.get("accuracy") || "exact";
      if (!["exact", "fast"].includes(accuracy)) throw failure(422, "Cette précision de comptage n’est pas valide.");
      const infos = await Promise.all(ids.map(corpus));
      let fields = [...COMMON_FIELDS, ...(ids.length === 1 ? infos[0].own : [])];
      const field = params.get("field");
      if (field) {
        fields = fields.filter((f) => f.name === field);
        if (!fields.length) throw failure(422, "Ce filtre n’est pas valide.");
      }
      return countFacets({
        count: (body) => count(body, signal),
        ids,
        fields,
        predicates,
        accuracy,
        ...(!field || field === TIMELINE_FIELD
          ? { timeline: { field: TIMELINE_FIELD, window: windowOf(params.get("window")) } }
          : {}),
      });
    },
    /**
     * GET /demo/explore/records/{id}: the Record, its current Version, the
     * Versions the demo read, its corpus's fields and its source files'
     * descriptions (the engine serves no file's bytes).
     */
    async record(id) {
      const response = await upstream(`/v0/records/${encodeURIComponent(id)}`);
      if (response.status >= 500) throw failure(503, "Ce document est momentanément indisponible. Réessayez.");
      const record = response.data;
      if (response.status !== 200 || !(await readable()).includes(record.source?.corpus_id))
        throw failure(404, "Document introuvable.");
      const current = record.current_version_id ? await version(id, record.current_version_id) : null;
      // The current Version heads the history, even one read long ago.
      if (current) history.note(id, current.version_id, current.accepted_at);
      const blobs = [];
      for (const blob of (current?.provenance?.source_blob_ids || []).slice(0, MAX_BLOBS)) {
        const read = await upstream(`/v0/blobs/${encodeURIComponent(blob)}`).catch(() => null);
        if (read?.status === 200) blobs.push(read.data);
      }
      return {
        record,
        version: current,
        versions: history.of(id).reverse(),
        corpus: await corpus(record.source.corpus_id),
        blobs,
      };
    },
  };
}
