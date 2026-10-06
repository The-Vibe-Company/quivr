// The feed's views over several corpora (THE-1171). Each corpus the demo
// reads has its own feed (feed.mjs) and index (catalog.mjs); a browser picks
// the corpora it follows, and these merge what it reads: the latest items,
// the live stream, a day's pages and every number. One corpus is served by
// its own feed and index as they are. Alerts and sources stay on the demo
// corpus, where the demo writes.
import { newestFirst, instant, unlisted } from "./feed.mjs";
import { topics } from "./topics.mjs";
import { TOPICS, TOPIC_TITLES } from "./catalog.mjs";

const SNAPSHOT_ITEMS = 300;
const DAY_PAGE = 40;
const MAX_CLIENTS = 200;
const KEEPALIVE_MS = 15000;

/** Adds b's counts into a: numbers, maps of numbers and lists of numbers. */
function add(a, b) {
  for (const [key, value] of Object.entries(b)) {
    if (typeof value === "number") a[key] = (a[key] || 0) + value;
    else if (Array.isArray(value)) a[key] = value.map((n, i) => (a[key]?.[i] || 0) + n);
    else if (value && typeof value === "object") add((a[key] ||= Object.create(null)), value);
  }
  return a;
}

/**
 * The index status of several corpora: numbers are short while any is
 * building or partial, and whole only after the latest of their bounds.
 */
function progress(list) {
  const after = list.map((s) => s.complete_after).filter(Boolean).sort();
  return {
    building: list.some((s) => s.building),
    partial: list.some((s) => s.partial),
    complete_after: after.at(-1) || null,
  };
}
const statusOf = ({ building, partial, complete_after }) => ({ building, partial, complete_after });

/**
 * The corpora a request picks (?corpora=a,b), each one the demo reads, or
 * the demo corpus when it picks none. Any other corpus is refused.
 */
export function corporaPicker({ readable, demo }) {
  return async (params) => {
    const asked = (params.get("corpora") || "").split(",").filter(Boolean);
    if (!asked.length) return [demo()];
    const allowed = new Set(await readable());
    if (asked.length > 16 || new Set(asked).size !== asked.length || asked.some((id) => !allowed.has(id)))
      throw Object.assign(new Error("Corpus non autorisé."), { status: 403 });
    return asked;
  };
}

export function createViews({ feedFor, indexFor, upstream }) {
  const clients = new Set();
  const keepalive = setInterval(() => {
    // A stream still waiting for its first scans has sent no headers yet.
    for (const res of clients) if (res.headersSent) res.write(": keepalive\n\n");
  }, KEEPALIVE_MS);
  keepalive.unref();

  return {
    async snapshot(corpora) {
      if (corpora.length === 1) return feedFor(corpora[0]).snapshot();
      const snaps = await Promise.all(corpora.map((id) => feedFor(id).snapshot()));
      return {
        items: snaps.flatMap((s) => s.items).sort(newestFirst).slice(0, SNAPSHOT_ITEMS),
        live: snaps.every((s) => s.live),
      };
    },
    /** One SSE stream over the corpora: their items, removals and resets, live when all are. */
    async subscribe(req, res, corpora) {
      if (clients.size >= MAX_CLIENTS)
        throw Object.assign(new Error("Trop de connexions au flux. Réessayez."), { status: 503 });
      const feeds = corpora.map((id) => feedFor(id));
      const live = () => feeds.every((feed) => feed.live());
      // Followed from now on: what arrives during the first catalog scans
      // waits here, and is written once the stream is open.
      let waiting = [];
      const frame = (event, data) => {
        const text = `event: ${event}\ndata: ${JSON.stringify(event === "status" ? { live: live() } : data)}\n\n`;
        if (waiting) waiting.push(text);
        else res.write(text);
      };
      const stops = feeds.map((feed) => feed.listen(frame));
      const stop = () => {
        clients.delete(res);
        for (const end of stops) end();
      };
      clients.add(res);
      req.on("close", stop);
      try {
        // A feed that cannot start refuses the stream, so the browser retries.
        await Promise.all(feeds.map((feed) => feed.opened()));
      } catch (error) {
        stop();
        throw error;
      }
      // The browser may have left during the first catalog scans.
      if (req.socket.destroyed || res.destroyed) return stop();
      res.writeHead(200, {
        "Content-Type": "text/event-stream; charset=utf-8",
        "Cache-Control": "no-store",
        "X-Accel-Buffering": "no",
      });
      res.write(`retry: 3000\nevent: status\ndata: ${JSON.stringify({ live: live() })}\n\n`);
      for (const text of waiting) res.write(text);
      waiting = null;
    },
    /** A period's page, newest first, from one listing over the corpora. */
    async page(corpora, params) {
      if (corpora.length === 1) return feedFor(corpora[0]).page(params);
      const after = instant(params.get("after"));
      const before = instant(params.get("before"));
      if (!after || !before || Date.parse(after) >= Date.parse(before)) throw unlisted(422);
      const query = new URLSearchParams({
        corpus_ids: corpora.join(","),
        order: "accepted_at_desc",
        accepted_after: after,
        accepted_before: before,
        limit: String(DAY_PAGE),
      });
      const cursor = params.get("cursor");
      if (cursor) query.set("page_cursor", cursor);
      const response = await upstream(`/v0/records?${query}`);
      if (response.status !== 200) throw unlisted(response.status);
      const records = response.data.items || [];
      // Each corpus's feed describes its own Records; the listing's order holds.
      const described = new Map();
      // One corpus after the other, so the page reads Versions a few at a time.
      for (const id of corpora) {
        const own = records.filter((record) => record.source?.corpus_id === id);
        if (!own.length) continue;
        for (const item of await feedFor(id).describeRecords(own)) described.set(item.record_id, item);
      }
      const page = { items: records.map((r) => described.get(r.record_id)).filter(Boolean) };
      if (response.data.next_page_cursor) page.next_cursor = response.data.next_page_cursor;
      return page;
    },
    async days(corpora, params) {
      const answers = await Promise.all(corpora.map((id) => feedFor(id).days(params)));
      if (answers.length === 1) return answers[0];
      const { as_of, ...counts } = answers.reduce((sum, answer) => add(sum, { ...answer, as_of: 0 }), {});
      return { ...counts, as_of: answers.map((a) => a.as_of).sort()[0] };
    },
    async stats(corpora, q) {
      const answers = await Promise.all(corpora.map((id) => indexFor(id).feed(q)));
      if (answers.length === 1) return answers[0];
      const sum = Object.create(null);
      for (const answer of answers) {
        const { building, partial, complete_after, ...counts } = answer;
        add(sum, counts);
      }
      return { ...sum, ...progress(answers.map(statusOf)) };
    },
    async topics(corpora, q) {
      if (corpora.length === 1) return indexFor(corpora[0]).topics(q);
      const answers = await Promise.all(corpora.map((id) => indexFor(id).titles(q)));
      // The newest TOPIC_TITLES over every corpus, as for one.
      const known = answers.flatMap((a) => a.known);
      const kept = known.length > TOPIC_TITLES ? known.sort((a, b) => b.at - a.at).slice(0, TOPIC_TITLES) : known;
      const status = progress(answers.map(statusOf));
      return {
        items: topics(kept.map((k) => k.title), TOPICS),
        ...status,
        partial: status.partial || kept.length < known.length,
        as_of: new Date().toISOString(),
      };
    },
  };
}
