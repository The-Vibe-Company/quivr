// Live feed of the demo Corpus, kept by the facade. It follows the public
// resynchronization procedure once, server-side: capture a start-now change
// cursor, scan the Record catalog newest first, then consume the change
// stream with the server key and reread each Record an event names. Browsers
// read a snapshot and a fan-out SSE stream; the core key and change cursors
// never reach them. Older days are read on demand: one range of the catalog
// by acceptance date, and cached counts per day.
//
// An item's arrival time is when Quivr accepted its current Version, read
// from the Version itself, so Records found by a catalog scan after a restart
// keep it. The change event's occurred_at stands in only for a core that does
// not send it.

const MAX_ITEMS = 1000;
const SNAPSHOT_ITEMS = 300;
const CATALOG_PAGES = 10;
const HYDRATE_CONCURRENCY = 6;
const MAX_CLIENTS = 200;
const KEEPALIVE_MS = 15000;
const IDLE_MS = 45000;
const TITLE_CHARS = 300; // the page cuts titles to one line, the tooltip shows them whole
const EXCERPT_CHARS = 320;
const RSS_EXTENSION = "connector.rss";
const DAY_PAGE = 40;
const MAX_BOUNDS = 15;
const COUNT_CONCURRENCY = 4;
const COUNT_TTL_MS = 60000;
const MAX_COUNTS = 100;
// What the engine accepts as a bound: RFC 3339 with an offset.
const RFC3339 =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$/;
const instant = (value) =>
  typeof value === "string" &&
  RFC3339.test(value) &&
  !Number.isNaN(Date.parse(value))
    ? value
    : undefined;

const collapse = (text) => (text || "").replace(/\s+/g, " ").trim();
function clip(text, size) {
  if (text.length <= size) return text;
  const cut = text.slice(0, size);
  const space = cut.lastIndexOf(" ");
  return (space > size * 0.6 ? cut.slice(0, space) : cut).trimEnd() + "…";
}
// Only a web address may become the reader's "Ouvrir l'original" link.
function webLink(value) {
  if (typeof value !== "string" || value.length > 2048) return undefined;
  try {
    const url = new URL(value.trim());
    return url.protocol === "http:" || url.protocol === "https:"
      ? url.href
      : undefined;
  } catch {
    return undefined;
  }
}
const date = (value) =>
  typeof value === "string" && !Number.isNaN(Date.parse(value))
    ? new Date(value).toISOString()
    : undefined;

/**
 * The feed item of a Record's current Version: source, title, excerpt, and
 * the original article's address when its source gives one.
 */
export function describe(record, version, receivedAt) {
  const texts = (version.manifest?.parts || []).filter(
    (part) =>
      part.content?.kind === "text" &&
      !part.parent_key &&
      part.role !== "source_html" &&
      collapse(part.content.text),
  );
  const titlePart = texts.find((part) => part.role === "title");
  const rest = texts.filter((part) => part !== titlePart);
  const bodyPart =
    rest.find((part) => part.role === "body") ||
    rest.find((part) => part.role === "summary") ||
    rest[0];
  let title = collapse(titlePart?.content.text);
  let excerpt = collapse(bodyPart?.content.text);
  if (!title) {
    // A pasted text has no title Part: its first line stands in for one.
    const raw = (bodyPart?.content.text || "").trim();
    const [first = "", ...others] = raw.split(/\n+/);
    title = collapse(first);
    excerpt = collapse(others.join(" "));
    if (title.length > TITLE_CHARS && !excerpt) excerpt = title;
  }
  const rss = version.extensions?.[RSS_EXTENSION]?.data?.item;
  const item = {
    record_id: record.record_id,
    version_id: version.version_id,
    namespace: record.source?.namespace || "",
    title: clip(title, TITLE_CHARS),
    excerpt: clip(excerpt, EXCERPT_CHARS),
  };
  const published = date(rss?.published) || date(rss?.updated);
  if (published) item.published_at = published;
  const link = webLink(rss?.link) || webLink(rss?.links?.[0]);
  if (link) item.link = link;
  if (receivedAt) item.received_at = receivedAt;
  return item;
}

const sortKey = (item) => item.received_at || item.published_at || "";
export const newestFirst = (a, b) =>
  sortKey(b).localeCompare(sortKey(a)) ||
  a.record_id.localeCompare(b.record_id);

/** Parses a text/event-stream body into {id, event, data} messages. */
export async function* serverEvents(body, onChunk = () => {}) {
  const decoder = new TextDecoder();
  let buffer = "";
  for await (const chunk of body) {
    onChunk();
    buffer += decoder.decode(chunk, { stream: true }).replace(/\r\n?/g, "\n");
    let end;
    while ((end = buffer.indexOf("\n\n")) >= 0) {
      const block = buffer.slice(0, end);
      buffer = buffer.slice(end + 2);
      const message = { id: undefined, event: "message", data: "" };
      const data = [];
      for (const line of block.split("\n")) {
        if (!line || line.startsWith(":")) continue;
        const colon = line.indexOf(":");
        const field = colon < 0 ? line : line.slice(0, colon);
        const value = colon < 0 ? "" : line.slice(colon + 1).replace(/^ /, "");
        if (field === "id") message.id = value;
        else if (field === "event") message.event = value;
        else if (field === "data") data.push(value);
      }
      if (!data.length && message.id === undefined) continue;
      message.data = data.join("\n");
      yield message;
    }
  }
}

const failure = (status, message) =>
  Object.assign(new Error(message), { status });
// What a browser is told when the feed cannot start.
const unavailable = (status) =>
  status === 403
    ? failure(
        403,
        "La veille n’est pas activée : la clé du moteur n’a pas accès aux changements.",
      )
    : failure(503, "La veille est momentanément indisponible. Réessayez.");
// What a browser is told when a day cannot be listed or counted.
const unlisted = (status) =>
  status === 409
    ? failure(409, "La liste de ce jour a changé. Choisissez-le à nouveau.")
    : failure(
        status === 422 ? 422 : 503,
        status === 422
          ? "Cette période n’est pas valide."
          : "Les articles de ce jour sont momentanément indisponibles. Réessayez.",
      );

export function createFeed({ core, key, corpus, upstream }) {
  const items = new Map();
  const clients = new Set();
  // Other views of the demo corpus (the Admin tab) told of each change as it
  // is read, before its Record is reread, and of the stream's liveness.
  const watchers = new Set();
  const notify = (...args) => {
    for (const watcher of watchers) watcher(...args);
  };
  let cursor = null;
  let started = null;
  let live = false;
  const query = (extra = {}) =>
    new URLSearchParams({ corpus_id: corpus, ...extra }).toString();

  function broadcast(event, data) {
    const frame = `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
    for (const res of clients) res.write(frame);
  }
  function setLive(value) {
    if (live === value) return;
    live = value;
    broadcast("status", { live });
    notify("status", live);
  }
  function upsert(item) {
    items.set(item.record_id, item);
    if (items.size > MAX_ITEMS) {
      const oldest = [...items.values()].sort(newestFirst).at(-1);
      items.delete(oldest.record_id);
      if (oldest.record_id === item.record_id) return;
    }
    broadcast("item", item);
  }
  function remove(id) {
    if (items.delete(id)) broadcast("remove", { record_id: id });
  }

  async function hydrate(id, receivedAt) {
    const path = `/v0/records/${encodeURIComponent(id)}`;
    const record = await upstream(path);
    if (record.status === 404) return remove(id);
    if (record.status !== 200) throw failure(record.status, "record read");
    const current = record.data;
    if (current.source?.corpus_id !== corpus || current.withdrawn)
      return remove(id);
    if (!current.current_version_id) return;
    const known = items.get(id);
    if (known?.version_id === current.current_version_id) return;
    const version = await upstream(
      `${path}/versions/${encodeURIComponent(current.current_version_id)}`,
    );
    if (version.status === 404) return;
    if (version.status !== 200) throw failure(version.status, "version read");
    const arrived = date(version.data.accepted_at) || receivedAt;
    const item = describe(current, version.data, arrived || known?.received_at);
    // A new Version of an article already in the feed whose title or text
    // changed is a correction: the reader says so, dated by that Version's
    // arrival. A feed that only re-dates its items (a new Version on every
    // poll) corrects nothing. The Version it replaces stays readable
    // (Versions are immutable), so the reader can show what changed.
    const edited =
      known && (known.title !== item.title || known.excerpt !== item.excerpt);
    if (edited && arrived) {
      item.updated_at = arrived;
      item.previous_version_id = known.version_id;
    } else if (known?.updated_at) {
      item.updated_at = known.updated_at;
      if (known.previous_version_id)
        item.previous_version_id = known.previous_version_id;
    }
    upsert(item);
  }

  async function sync() {
    const start = await upstream(`/v0/changes?${query({ limit: "1" })}`);
    if (start.status !== 200) throw unavailable(start.status);
    const position = start.data.next_cursor;
    const ids = [];
    let page;
    // Newest first, so the items kept are the latest ones.
    for (let n = 0; n < CATALOG_PAGES; n++) {
      const extra = { order: "accepted_at_desc", limit: "100" };
      if (page?.next_page_cursor) extra.page_cursor = page.next_page_cursor;
      const response = await upstream(`/v0/records?${query(extra)}`);
      if (response.status !== 200) throw unavailable(response.status);
      page = response.data;
      ids.push(...page.items.map((record) => record.record_id));
      if (!page.next_page_cursor) break;
    }
    const queue = [...ids];
    await Promise.all(
      Array.from({ length: HYDRATE_CONCURRENCY }, async () => {
        // One unreadable Record must not keep the others out of the feed.
        for (let id; (id = queue.shift());) await hydrate(id).catch(() => {});
      }),
    );
    if (!page?.next_page_cursor) {
      const listed = new Set(ids);
      for (const id of [...items.keys()]) if (!listed.has(id)) remove(id);
    }
    cursor = position;
  }

  async function follow() {
    for (let delay = 1000; ;) {
      const controller = new AbortController();
      let idle;
      const watch = () => {
        clearTimeout(idle);
        idle = setTimeout(() => controller.abort(), IDLE_MS);
      };
      try {
        watch();
        const response = await fetch(
          `${core}/v0/changes/stream?${query({ cursor })}`,
          {
            headers: {
              Authorization: `Bearer ${key}`,
              Accept: "text/event-stream",
            },
            signal: controller.signal,
            redirect: "error",
          },
        );
        // An expired, rescoped or unreadable cursor (after a cursor key
        // rotation, say) cannot resume: start over from the catalog.
        if ([409, 410, 422].includes(response.status)) {
          await response.body?.cancel();
          await resync();
          // Fall through to the backoff: a fresh cursor refused again
          // must not turn into back-to-back catalog scans.
          throw failure(response.status, "cursor");
        }
        if (response.status !== 200) {
          await response.body?.cancel();
          throw failure(response.status, "stream");
        }
        setLive(true);
        delay = 1000;
        let expired = false;
        for await (const message of serverEvents(response.body, watch)) {
          if (message.event === "stream_error") {
            const code = JSON.parse(message.data || "{}").code;
            expired =
              code === "cursor_expired" || code === "cursor_scope_changed";
            break;
          }
          if (message.event === "change") {
            const change = JSON.parse(message.data);
            notify("change", change);
            // A Record that stays unreadable is skipped rather than
            // replayed forever, which would freeze the feed behind it.
            if (change.resource?.kind === "record")
              await retry(() =>
                hydrate(change.resource.id, date(change.occurred_at)),
              ).catch((error) => {
                // Only an answer that will not change is skipped; an outage,
                // a 5xx or throttling reconnects and retries from the cursor.
                const final =
                  error.oversized ||
                  (error.status >= 400 &&
                    error.status < 500 &&
                    ![408, 429].includes(error.status));
                if (!final) throw error;
                console.warn(
                  `Veille: Record ${change.resource.id} skipped (HTTP ${error.status})`,
                );
              });
          }
          // Advance only once the event's Record was reread.
          if (message.id) cursor = message.id;
        }
        controller.abort();
        if (expired) await resync();
      } catch (error) {
        // Reconnect below from the last cursor.
        const reason = error.status ? `HTTP ${error.status}` : error.message;
        console.warn(`Veille: change stream interrupted (${reason})`);
      } finally {
        clearTimeout(idle);
      }
      setLive(false);
      await new Promise((resolve) => setTimeout(resolve, delay));
      delay = Math.min(delay * 2, 30000);
    }
  }
  async function retry(action) {
    for (let attempt = 0; ; attempt++) {
      try {
        return await action();
      } catch (error) {
        if (attempt >= 2) throw error;
        await new Promise((resolve) => setTimeout(resolve, 500 * 2 ** attempt));
      }
    }
  }
  async function resync() {
    await sync();
    broadcast("reset", {});
    notify("reset");
  }

  // The feed item of each listed Record, the one in memory when it shows the
  // same Version. A Version that is gone is left out; any other failure fails
  // the page, so the browser retries it rather than skip the Record.
  async function describeListed(records) {
    const out = [];
    const queue = records
      .filter(
        (record) =>
          !record.withdrawn &&
          record.current_version_id &&
          record.source?.corpus_id === corpus,
      )
      .map((record, index) => ({ record, index }));
    await Promise.all(
      Array.from({ length: HYDRATE_CONCURRENCY }, async () => {
        for (let next; (next = queue.shift()); ) {
          const { record, index } = next;
          const known = items.get(record.record_id);
          if (known?.version_id === record.current_version_id) {
            out[index] = known;
            continue;
          }
          const version = await upstream(
            `/v0/records/${encodeURIComponent(record.record_id)}/versions/${encodeURIComponent(record.current_version_id)}`,
          ).catch(() => ({ status: 503 }));
          if (version.status === 404) continue;
          if (version.status !== 200) throw unlisted(503);
          out[index] = describe(record, version.data, date(version.data.accepted_at));
        }
      }),
    );
    return out.filter(Boolean);
  }

  // Cached day counts, by the bounds asked: browsers of one time zone ask the
  // same ones, so opening the date filter does not count again for each.
  const counts = new Map();
  async function count(extra) {
    const response = await upstream(`/v0/records/count?${query(extra)}`);
    if (response.status !== 200) throw unlisted(response.status);
    return response.data.count;
  }
  async function countRanges(bounds) {
    const as_of = new Date().toISOString();
    const ranges = [
      {},
      ...bounds.slice(1).map((after, i) => ({
        accepted_after: after,
        accepted_before: bounds[i],
      })),
      { accepted_before: bounds.at(-1) },
    ];
    const results = new Array(ranges.length);
    const queue = ranges.map((range, index) => ({ range, index }));
    await Promise.all(
      Array.from({ length: COUNT_CONCURRENCY }, async () => {
        for (let next; (next = queue.shift()); )
          results[next.index] = await count(next.range);
      }),
    );
    return {
      total: results[0],
      days: results.slice(1, -1),
      older: results.at(-1),
      as_of,
    };
  }

  function start() {
    started ||= sync().then(
      () => {
        void follow();
      },
      (error) => {
        started = null;
        throw error;
      },
    );
    return started;
  }
  const keepalive = setInterval(() => {
    for (const res of clients) res.write(": keepalive\n\n");
  }, KEEPALIVE_MS);
  keepalive.unref();

  return {
    /** Follows the change stream: watcher("change", event), ("status", live), ("reset"). */
    watch(watcher) {
      watchers.add(watcher);
      watcher("status", live);
      start().catch(() => {});
      return () => watchers.delete(watcher);
    },
    async snapshot() {
      await start();
      return {
        items: [...items.values()].sort(newestFirst).slice(0, SNAPSHOT_ITEMS),
        live,
      };
    },
    /**
     * One page of a period, newest first: ?after=…&before=… (RFC 3339, the
     * browser's local day) and the cursor of the page before, if any.
     */
    async page(params) {
      const after = instant(params.get("after"));
      const before = instant(params.get("before"));
      if (!after || !before || Date.parse(after) >= Date.parse(before))
        throw unlisted(422);
      const extra = {
        order: "accepted_at_desc",
        accepted_after: after,
        accepted_before: before,
        limit: String(DAY_PAGE),
      };
      const cursor = params.get("cursor");
      if (cursor) extra.page_cursor = cursor;
      const response = await upstream(`/v0/records?${query(extra)}`);
      if (response.status !== 200) throw unlisted(response.status);
      const page = { items: await describeListed(response.data.items) };
      if (response.data.next_page_cursor)
        page.next_cursor = response.data.next_page_cursor;
      return page;
    },
    /**
     * Articles per period, from ?bounds=t0,t1,…,tn (RFC 3339, newest first):
     * the collection's total, one count per [t(i+1), t(i)), and how many are
     * older than tn. as_of says when they were counted.
     */
    async days(params) {
      const bounds = (params.get("bounds") || "").split(",");
      if (
        bounds.length > MAX_BOUNDS ||
        bounds.some((bound) => !instant(bound)) ||
        bounds.some((bound, i) => i && Date.parse(bound) >= Date.parse(bounds[i - 1]))
      )
        throw unlisted(422);
      const key = bounds.join(",");
      const now = Date.now();
      for (const [cached, entry] of counts)
        if (now - entry.at >= COUNT_TTL_MS) counts.delete(cached);
      let entry = counts.get(key);
      if (!entry) {
        entry = { at: now, answer: countRanges(bounds) };
        counts.set(key, entry);
        if (counts.size > MAX_COUNTS) counts.delete(counts.keys().next().value);
        entry.answer.catch(() => {
          if (counts.get(key) === entry) counts.delete(key);
        });
      }
      return entry.answer;
    },
    async subscribe(req, res) {
      await start();
      // The browser may have left during the first catalog scan.
      if (req.socket.destroyed || res.destroyed) return;
      if (clients.size >= MAX_CLIENTS)
        throw failure(503, "Trop de connexions au flux. Réessayez.");
      res.writeHead(200, {
        "Content-Type": "text/event-stream; charset=utf-8",
        "Cache-Control": "no-store",
        "X-Accel-Buffering": "no",
      });
      res.write(
        `retry: 3000\nevent: status\ndata: ${JSON.stringify({ live })}\n\n`,
      );
      clients.add(res);
      req.on("close", () => clients.delete(res));
    },
  };
}
