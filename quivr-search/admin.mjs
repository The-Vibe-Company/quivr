// The Admin tab (THE-796): the demo corpus's latest documents moving through
// their steps, their timelines, and the numbers of the last hour and day.
// Read-only. The facade reads the core's admin list with its own key
// (observability:read), keeps the documents of the demo corpus, and relays
// them to browsers as a snapshot and an SSE stream; neither the key nor a
// cursor reaches a browser.
//
// A refresh rereads the latest page of the admin list. It runs when the Fil's
// change stream (feed.mjs) reports a change in the demo corpus, and every few
// seconds while a browser watches, because an alert decision has no Record
// event of its own. A bounded scan of the same list over 24 hours gives each
// step's usual duration, the documents waiting and the errors of the hour.
// The engine's step rollups (`GET /v0/admin/stats/steps`, THE-795) give the
// throughput, the per-hour chart and the received-to-searchable p95; they
// cover the key's whole Organization, which the demo dedicates to its corpus.
// Without them (a core before THE-795) the scan counts those too.
//
// The other sections of the tab read the engine's rollups through
// `/demo/admin/stats/{kind}`, cached a few seconds so browsers polling them
// cost the core one read per window.

const LIVE = 50;
const PAGE = 100;
const DAY_PAGES = 10;
const DAY_MAX = 5000;
const MINUTE = 60000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
const DEBOUNCE_MS = 200;
const SAFETY_MS = 5000;
const SCAN_MS = MINUTE;
const MAX_CLIENTS = 50;
const RATE_WINDOW = 10 * MINUTE;
const WAITING = new Set(["received", "materialized", "building_baseline"]);
const STATS_KINDS = new Set(["plugins", "searches", "steps", "top-queries"]);
const STATS_WINDOWS = new Set(["1h", "24h", "7d"]);
const STATS_MS = 10000;

/**
 * The five steps a document is shown going through. Each is timed from the
 * step that causes it, as the core's timeline does: vectors and alert
 * decisions both follow the document becoming searchable. `after` falls back
 * to an earlier step when the cause has no time. Vectors and alert decisions
 * missing `settle` ms after the document became searchable are shown as not
 * recorded: a Corpus without vectors, or no alert to decide.
 */
export const STEPS = [
  {
    key: "received",
    at: "materialized_at",
    after: ["accepted_at"],
    needs: [],
    floor: 250,
  },
  {
    key: "cut",
    at: "segmented_at",
    after: ["materialized_at", "accepted_at"],
    needs: ["received"],
    floor: 250,
  },
  {
    key: "searchable",
    at: "retrieval_ready_at",
    after: ["segmented_at", "materialized_at", "accepted_at"],
    needs: ["cut"],
    floor: 250,
  },
  {
    key: "vectors",
    at: "enriched_at",
    after: ["retrieval_ready_at"],
    needs: ["searchable"],
    floor: 1000,
    settle: 10 * MINUTE,
  },
  {
    key: "alerts",
    at: "evaluated_at",
    after: ["retrieval_ready_at"],
    needs: ["searchable"],
    floor: 1000,
    settle: 2 * MINUTE,
  },
];
// Below this many durations over the day, a step's p95 is too noisy to judge
// by; three times its median stands in. Below HISTORY_SAMPLES there is no
// "usual" yet, and nothing is called slow.
const P95_SAMPLES = 20;
const HISTORY_SAMPLES = 5;

const time = (value) => {
  const at = typeof value === "string" ? Date.parse(value) : NaN;
  return Number.isNaN(at) ? undefined : at;
};
const accepted = (doc) => time(doc.steps?.accepted_at) ?? 0;
const newestFirst = (a, b) =>
  accepted(b) - accepted(a) || b.version_id.localeCompare(a.version_id);

/** How long a finished step took, from the first known time it follows. */
function took(steps, step) {
  const at = time(steps[step.at]);
  if (at === undefined) return undefined;
  for (const name of step.after) {
    const from = time(steps[name]);
    if (from !== undefined) return from <= at ? at - from : undefined;
  }
  return undefined;
}

/** Nearest-rank percentile of a sorted list. */
const percentile = (sorted, p) =>
  sorted.length ? sorted[Math.ceil(p * sorted.length) - 1] : null;

/**
 * When each step is slow: above its own p95 over the day, never below the
 * step's floor, so a 90 ms cut beside 80 ms ones is not flagged; null while
 * the step has too little history to say.
 */
export function limits(docs) {
  const out = {};
  for (const step of STEPS) {
    const samples = [];
    for (const doc of docs) {
      const ms = took(doc.steps || {}, step);
      if (ms !== undefined) samples.push(ms);
    }
    samples.sort((a, b) => a - b);
    if (samples.length < HISTORY_SAMPLES) {
      out[step.key] = null;
      continue;
    }
    const usual =
      samples.length >= P95_SAMPLES
        ? percentile(samples, 0.95)
        : 3 * samples[(samples.length - 1) >> 1];
    out[step.key] = Math.max(step.floor, usual);
  }
  return out;
}

/**
 * The cells of one document, one per step: done (with its duration), slow,
 * running (with the time it waits from), to do, not recorded, or error for
 * the step a quarantine stopped. Each carries its step's slow limit.
 */
export function flow(doc, limit, now) {
  const steps = doc.steps || {};
  const quarantined = doc.state === "quarantined" || !!steps.quarantined_at;
  const withdrawn = doc.state === "withdrawn" || !!steps.withdrawn_at;
  const lastDone = STEPS.findLastIndex((s) => steps[s.at]);
  const state = {};
  let stopped = false;
  return STEPS.map((step, index) => {
    const cell = { key: step.key, limit: limit[step.key] };
    if (steps[step.at]) {
      const ms = took(steps, step);
      if (ms !== undefined) cell.ms = ms;
      cell.state =
        ms !== undefined && cell.limit !== null && ms > cell.limit
          ? "slow"
          : "done";
    } else if (index < lastDone && !step.settle) {
      // A later step finished without this one's time (a Version from
      // before step times, or a path that skips it).
      cell.state = "none";
    } else if (
      !step.needs.every((n) => ["done", "slow", "none"].includes(state[n]))
    ) {
      cell.state = "todo";
    } else if (quarantined && !stopped) {
      cell.state = "error";
      stopped = true;
    } else if (quarantined || withdrawn) {
      cell.state = "none";
    } else {
      const since = step.after.map((n) => steps[n]).find(time);
      if (step.settle && now - (time(since) ?? now) > step.settle)
        cell.state = "none";
      else {
        cell.state = "run";
        if (since) cell.since = since;
        // A step that may never come (no vectors, no alert to decide) is not
        // late before it settles.
        if (step.settle) cell.limit = Math.max(cell.limit ?? 0, step.settle);
      }
    }
    state[step.key] = cell.state;
    return cell;
  });
}

/**
 * The KPIs and the documents received per hour over 24 hours. `stuck`
 * counts documents whose running step already exceeds its slow limit.
 * `countedFrom` is where a capped scan stopped: hours before it are not
 * counted.
 */
export function summarize(docs, now, limit, countedFrom) {
  const hour = [];
  const day = [];
  let perWindow = 0;
  let waiting = 0;
  let stuck = 0;
  // The queue in front of each step: documents running it, and since when
  // the oldest of them waits.
  const queues = Object.fromEntries(STEPS.map((s) => [s.key, { count: 0 }]));
  let errors = 0;
  const top = Math.floor(now / HOUR) * HOUR;
  const hours = Array.from({ length: 24 }, (_, i) => ({
    start: new Date(top - (23 - i) * HOUR).toISOString(),
    count: 0,
    errors: 0,
  }));
  for (const doc of docs) {
    const steps = doc.steps || {};
    const at = time(steps.accepted_at);
    if (at === undefined || at > now) continue;
    if (now - at <= RATE_WINDOW) perWindow++;
    if (WAITING.has(doc.state)) waiting++;
    const running = flow(doc, limit, now).filter((c) => c.state === "run");
    if (
      running.some(
        (c) => c.limit !== null && now - (time(c.since) ?? now) > c.limit,
      )
    )
      stuck++;
    for (const cell of running) {
      const queue = queues[cell.key];
      queue.count++;
      const since = time(cell.since);
      if (since !== undefined && !(time(queue.oldest_since) <= since))
        queue.oldest_since = new Date(since).toISOString();
    }
    const quarantine = time(steps.quarantined_at);
    if (quarantine !== undefined && now - quarantine <= HOUR) errors++;
    const ready = time(steps.retrieval_ready_at);
    if (ready !== undefined && ready >= at) {
      day.push(ready - at);
      if (now - at <= HOUR) hour.push(ready - at);
    }
    const slot = 23 - Math.floor((top - Math.floor(at / HOUR) * HOUR) / HOUR);
    if (slot >= 0 && slot < 24) {
      hours[slot].count++;
      if (quarantine !== undefined) hours[slot].errors++;
    }
  }
  hour.sort((a, b) => a - b);
  day.sort((a, b) => a - b);
  const stats = {
    per_minute: Math.round((perWindow / (RATE_WINDOW / MINUTE)) * 10) / 10,
    searchable_p95_ms: percentile(hour, 0.95),
    searchable_day_p95_ms: percentile(day, 0.95),
    waiting,
    stuck,
    waiting_by_step: queues,
    errors,
    hours,
  };
  if (countedFrom) stats.counted_from = new Date(countedFrom).toISOString();
  return stats;
}

/**
 * The KPIs the engine's step rollups give (THE-795): searchable documents per
 * minute over the hour, the received-to-searchable p95 over the hour and the
 * day, and documents made searchable per hour over 24 hours, with the step
 * errors (retried or blocked) of each hour. Buckets are sparse.
 */
export function fromRollups(hour, day, now) {
  const series = (list, step) => list.items.find((i) => i.step === step);
  const searchable = series(hour, "accepted_to_searchable");
  const searchableDay = series(day, "accepted_to_searchable");
  const top = Math.floor(now / HOUR) * HOUR;
  const hours = Array.from({ length: 24 }, (_, i) => ({
    start: new Date(top - (23 - i) * HOUR).toISOString(),
    count: 0,
    errors: 0,
  }));
  const slot = (start) =>
    23 - Math.floor((top - Math.floor(Date.parse(start) / HOUR) * HOUR) / HOUR);
  for (const point of searchableDay?.points || []) {
    const i = slot(point.start);
    if (i >= 0 && i < 24) hours[i].count += point.count;
  }
  for (const step of ["baseline", "enrichment"])
    for (const point of series(day, step)?.points || []) {
      const i = slot(point.start);
      if (i >= 0 && i < 24) hours[i].errors += point.errors;
    }
  return {
    per_minute: Math.round(((searchable?.summary.count || 0) / 60) * 10) / 10,
    per_minute_window: "1h",
    searchable_p95_ms: searchable?.summary.p95_ms ?? null,
    searchable_day_p95_ms: searchableDay?.summary.p95_ms ?? null,
    hours,
    hours_of: "searchable",
  };
}

/**
 * The header's numbers: the engine's rollups where it has them, the scan's
 * otherwise. Rollups are flushed and cached for seconds, so a document the
 * scan already knows keeps the header from reading "calme" or "—" meanwhile.
 */
export function withRollups(own, rollups) {
  if (!rollups)
    return { ...own, per_minute_window: "10min", hours_of: "received" };
  const merged = { ...own, ...rollups };
  delete merged.counted_from;
  if (!rollups.per_minute && own.per_minute) {
    merged.per_minute = own.per_minute;
    merged.per_minute_window = "10min";
  }
  for (const key of ["searchable_p95_ms", "searchable_day_p95_ms"])
    if (merged[key] === null) merged[key] = own[key];
  return merged;
}

const failure = (status, message) =>
  Object.assign(new Error(message), { status });
const unavailable = (status) =>
  status === 403
    ? failure(
        403,
        "La clé du moteur de la démo n’a pas le droit observability:read. Sur Railway, activez QUIVR_DEMO_ADMIN=1 sur le service api.",
      )
    : failure(503, "Le suivi est momentanément indisponible. Réessayez.");

export function createAdmin({ upstream, corpus, follow, clock = Date.now }) {
  // The demo corpus's documents of the last 24 hours, by Version.
  const day = new Map();
  // What each browser was last sent for the documents on show, by Version.
  const sent = new Map();
  const clients = new Set();
  let countedFrom;
  let limit = limits([]);
  let started = null;
  let scannedAt = 0;
  let readAt = 0;
  let live = false;
  let running = null;
  let again = false;
  let debounce;
  let tick;
  let unfollow;

  // The engine's rollups, by kind, window and limit, for STATS_MS.
  const rollups = new Map();
  let overlay = null;
  let overlayAt = 0;

  const view = (doc, now) => ({ ...doc, flow: flow(doc, limit, now) });
  const shown = () => [...day.values()].sort(newestFirst).slice(0, LIVE);
  const stats = (now) =>
    withRollups(
      summarize([...day.values()], now, limit, countedFrom),
      // Rollups that could not be reread for a minute stop standing in.
      now - overlayAt > 6 * STATS_MS ? null : overlay,
    );

  async function rollup(kind, window, limit) {
    const path = `/v0/admin/stats/${kind}?window=${window}${limit ? `&limit=${limit}` : ""}`;
    const cached = rollups.get(path);
    if (cached && clock() - cached.at < STATS_MS) return cached.response;
    const pending = upstream(path);
    rollups.set(path, { at: clock(), response: pending });
    try {
      return await pending;
    } catch (error) {
      rollups.delete(path);
      throw error;
    }
  }
  // The step rollups for the KPIs; kept as they were when a read fails.
  async function readRollups(now) {
    try {
      const [hour, day] = await Promise.all([
        rollup("steps", "1h"),
        rollup("steps", "24h"),
      ]);
      if (hour.status === 200 && day.status === 200) {
        overlay = fromRollups(hour.data, day.data, now);
        overlayAt = now;
      } else if (hour.status === 404) overlay = null;
    } catch {
      // The scan's numbers stand in until the next read.
    }
  }

  function broadcast(event, data) {
    const frame = `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
    for (const res of clients) res.write(frame);
  }
  async function page(cursor) {
    const query = new URLSearchParams({ limit: String(PAGE) });
    if (cursor) query.set("page_cursor", cursor);
    const response = await upstream(`/v0/admin/documents?${query}`);
    if (response.status !== 200) throw unavailable(response.status);
    return response.data;
  }
  function keep(items) {
    for (const doc of items)
      if (doc.corpus_id === corpus) day.set(doc.version_id, doc);
  }
  function prune(now) {
    for (const [id, doc] of day) if (now - accepted(doc) > DAY) day.delete(id);
    if (day.size > DAY_MAX)
      for (const doc of [...day.values()].sort(newestFirst).slice(DAY_MAX))
        day.delete(doc.version_id);
  }

  // Pages back 24 hours, at most DAY_PAGES pages.
  async function scanDay() {
    const now = clock();
    let cursor;
    let oldest = now;
    let pages = 0;
    do {
      let data;
      try {
        data = await page(cursor);
      } catch (error) {
        // Only the first page is needed; a later one that fails (a cursor
        // refused after a key rotation, a timeout) ends the scan there.
        if (!pages) throw error;
        break;
      }
      keep(data.items);
      pages++;
      for (const doc of data.items) oldest = Math.min(oldest, accepted(doc));
      cursor = data.next_page_cursor;
    } while (cursor && now - oldest < DAY && pages < DAY_PAGES);
    countedFrom = cursor && now - oldest < DAY ? oldest : undefined;
    scannedAt = readAt = now;
    prune(now);
    limit = limits([...day.values()]);
  }

  // Rereads the latest page and sends browsers what changed.
  async function refresh() {
    if (running) {
      again = true;
      return running;
    }
    running = (async () => {
      do {
        again = false;
        const now = clock();
        if (now - scannedAt > SCAN_MS) await scanDay();
        else keep((await page()).items);
        readAt = now;
        await readRollups(now);
        prune(now);
        limit = limits([...day.values()]);
        publish(now);
      } while (again);
    })().finally(() => {
      running = null;
    });
    return running;
  }
  function publish(now) {
    const items = [];
    const current = shown();
    const ids = new Set(current.map((doc) => doc.version_id));
    for (const doc of current) {
      const row = view(doc, now);
      const text = JSON.stringify(row);
      if (sent.get(doc.version_id) === text) continue;
      sent.set(doc.version_id, text);
      items.push(row);
    }
    for (const id of sent.keys()) if (!ids.has(id)) sent.delete(id);
    if (items.length) broadcast("documents", { items });
    broadcast("stats", stats(now));
  }
  // Only while a browser watches; a snapshot rereads the list itself.
  // At most one refresh per DEBOUNCE_MS, so a burst of changes still
  // refreshes the view instead of postponing it.
  function schedule() {
    if (!clients.size || debounce) return;
    debounce = setTimeout(() => {
      debounce = undefined;
      refresh().catch((error) =>
        console.warn(
          `Admin: refresh failed (${error.status || error.message})`,
        ),
      );
    }, DEBOUNCE_MS);
  }
  function setLive(value) {
    if (live === value) return;
    live = value;
    broadcast("status", { live });
  }

  function start() {
    started ||= (async () => {
      await scanDay();
      unfollow ||= follow?.((event, data) => {
        if (event === "status") setLive(data);
        else if (event === "change" || event === "reset") schedule();
      });
    })().catch((error) => {
      started = null;
      throw error;
    });
    return started;
  }
  function watching() {
    if (clients.size && !tick) {
      tick = setInterval(() => {
        schedule();
        for (const res of clients) res.write(": keepalive\n\n");
      }, SAFETY_MS);
      tick.unref?.();
    } else if (!clients.size && tick) {
      clearInterval(tick);
      tick = undefined;
    }
  }

  return {
    async snapshot() {
      await start();
      if (clock() - readAt > 1000) await refresh();
      else await readRollups(clock());
      const now = clock();
      const documents = shown().map((doc) => view(doc, now));
      return { documents, stats: stats(now), live };
    },
    async subscribe(req, res) {
      await start();
      if (req.socket.destroyed || res.destroyed) return;
      if (clients.size >= MAX_CLIENTS)
        throw failure(503, "Trop de connexions au suivi. Réessayez.");
      res.writeHead(200, {
        "Content-Type": "text/event-stream; charset=utf-8",
        "Cache-Control": "no-store",
        "X-Accel-Buffering": "no",
      });
      res.write(
        `retry: 3000\nevent: status\ndata: ${JSON.stringify({ live })}\n\n`,
      );
      clients.add(res);
      watching();
      req.on("close", () => {
        clients.delete(res);
        watching();
      });
    },
    // The engine's rollups for a section of the tab, read-only.
    async stats(kind, url) {
      if (!STATS_KINDS.has(kind)) throw failure(404, "Page introuvable.");
      const window = url.searchParams.get("window") || "1h";
      if (!STATS_WINDOWS.has(window)) throw failure(422, "Période inconnue.");
      const limit = Number(url.searchParams.get("limit"));
      const response = await rollup(
        kind,
        window,
        kind === "top-queries" &&
          Number.isInteger(limit) &&
          limit >= 1 &&
          limit <= 100
          ? limit
          : undefined,
      );
      if (response.status === 403) throw unavailable(403);
      return response;
    },
    // One document's steps in time order; a Version outside the demo corpus
    // is reported missing.
    async timeline(versionID) {
      const response = await upstream(
        `/v0/admin/documents/${encodeURIComponent(versionID)}/timeline`,
      );
      if (response.status === 403) throw unavailable(403);
      if (response.status >= 500) return response;
      if (
        response.status !== 200 ||
        response.data.document?.corpus_id !== corpus
      )
        throw failure(404, "Document introuvable.");
      return response;
    },
  };
}
