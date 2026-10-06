// A synthetic Quivr core behind the routes the facade calls: RSS-like
// Records spread over several days, a few connectors and keyword alerts.
// The facade tests import it (tests/server.test.mjs); run alone, it serves a
// generated corpus to measure the demo locally. Synthetic, neutral content.
//
//   node scripts/fake-core.mjs            # 12,000 Records over 10 days on :7700
//   FAKE_RECORDS=300 FAKE_DAYS=1 FAKE_LATENCY_MS=20 node scripts/fake-core.mjs
//
// Then run the demo against it:
//   QUIVR_API_URL=http://127.0.0.1:7700 QUIVR_API_KEY=local QUIVR_DEMO_CORPUS_ID=demo \
//   QUIVR_DEMO_DESTINATION_ID=demo-alerts-sink node server.mjs
//
// GET /fake/calls answers how many calls each route received, to check that
// the facade does not hammer the core.
import http from "node:http";
import { pathToFileURL } from "node:url";

const DAY = 86400000;
const OWNER = "quivr-web-demo";
export const SOURCES = [
  ["con_wire", "Dépêches exemple", "https://news.example.org/feed.xml"],
  ["con_tech", "Revue technique", "https://tech.example.com/rss"],
  ["con_weather", "Météo locale", "https://weather.example.net/feed"],
  ["con_city", "Gazette de Valmont", "https://city.example.org/rss"],
  ["con_sport", "Sport exemple", "https://sport.example.com/feed"],
];
// Each day has its own leading story, so the topics depend on the period.
const STORIES = [
  ["Orages", "grêle sur la vallée de Valmont"],
  ["Tramway", "grève des conducteurs à Port-Lumière"],
  ["Batteries", "usine pilote de sodium à Bellerive"],
  ["Sécheresse", "restrictions d’eau dans le bassin du Lorn"],
  ["Marché couvert", "réouverture après travaux"],
  ["Festival Lumina", "programmation dévoilée"],
  ["Pont suspendu", "inspection par drone"],
  ["Conseil municipal", "budget voté à Valmont"],
  ["Hôpital Saint-Aubin", "nouveau service d’urgences"],
  ["Rugby", "victoire de l’équipe de Bellerive"],
];
const VERBS = ["relance", "inquiète", "mobilise", "divise", "accélère", "surprend"];

/** `count` Records over `days` days before `now`, more on recent days. */
export function generate({ count = 12000, days = 10, now = Date.now() } = {}) {
  // A small deterministic generator, so every run holds the same corpus.
  let seed = 42;
  const random = () => ((seed = (seed * 1103515245 + 12345) % 2 ** 31) / 2 ** 31);
  const pick = (list) => list[Math.floor(random() * list.length)];
  return Array.from({ length: count }, (_, i) => {
    const age = Math.floor(random() ** 1.4 * days * DAY);
    const day = Math.floor(age / DAY);
    const story = random() < 0.6 ? STORIES[day % STORIES.length] : pick(STORIES);
    return {
      id: String(i).padStart(6, "0"),
      namespace: pick(SOURCES)[1],
      at: now - age - 60000,
      title: `${story[0]} : ${pick(VERBS)} — ${story[1]}`,
    };
  });
}

/**
 * The fake core. `records` are { id, namespace, at, title }; `alerts` are
 * [subscription id, name, words]: an alert catches the titles holding one.
 */
export function fakeCore({ records: given, alerts = [], latency = 0 } = {}) {
  const records = given.map((r) => ({
    record_id: `rec_${r.id}`,
    version_id: `ver_${r.id}`,
    namespace: r.namespace,
    accepted_at: new Date(r.at).toISOString(),
    title: r.title,
    withdrawn: false,
  }));
  const byId = new Map(records.map((r) => [r.record_id, r]));
  // Every Version a Record had, which stay readable.
  const versions = new Map(records.map((r) => [r.version_id, { ...r }]));
  let sorted = false;
  const ordered = () => {
    if (!sorted)
      records.sort(
        (a, b) => b.accepted_at.localeCompare(a.accepted_at) || b.record_id.localeCompare(a.record_id),
      );
    sorted = true;
    return records;
  };
  const matchesOf = (words) =>
    ordered()
      .filter((r) => words.some((w) => r.title.toLowerCase().includes(w.toLowerCase())))
      .reverse()
      .map((r, n) => ({
        match_id: `m_${r.record_id}_${n}`,
        record_id: r.record_id,
        record_version_id: r.version_id,
        evidence: { explanation: "", details: { terms: words.map((term) => ({ term, part_keys: ["title"] })) } },
      }));

  const calls = new Map();
  const streams = new Set();
  let event = 0;
  const json = (res, status, data) => {
    res.writeHead(status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(data));
  };
  const page = (items, offset, limit, key) => {
    const next =
      offset + limit < items.length
        ? Buffer.from(`${key}|${offset + limit}`).toString("base64url")
        : undefined;
    return { items: items.slice(offset, offset + limit), ...(next ? { next_page_cursor: next } : {}) };
  };
  const offsetOf = (cursor, key) => {
    if (!cursor) return 0;
    const text = Buffer.from(cursor, "base64url").toString();
    const cut = text.lastIndexOf("|");
    return text.slice(0, cut) === key ? Number(text.slice(cut + 1)) : -1;
  };
  const inBounds = (url) => {
    const after = url.searchParams.get("accepted_after");
    const before = url.searchParams.get("accepted_before");
    const a = after ? Date.parse(after) : -Infinity;
    const b = before ? Date.parse(before) : Infinity;
    return ordered().filter((r) => {
      const t = Date.parse(r.accepted_at);
      return t >= a && t < b;
    });
  };
  const record = (r) => ({
    record_id: r.record_id,
    source: { corpus_id: "demo", namespace: r.namespace, source_record_id: r.record_id },
    withdrawn: r.withdrawn,
    current_version_id: r.version_id,
  });
  const version = (v) => ({
    record_id: v.record_id,
    version_id: v.version_id,
    accepted_at: v.accepted_at,
    manifest: {
      parts: [{ key: "title", role: "title", content: { kind: "text", text: v.title } }],
    },
  });
  const connector = ([id, namespace, url]) => ({
    connector_id: id,
    corpus_id: "demo",
    source_namespace: namespace,
    kind: "rss",
    config: { url },
    schedule: { interval_seconds: 900 },
    health_policy: { silent_after_seconds: 86400, credential_warning_seconds: 604800 },
    enabled: true,
    created_at: ordered().at(-1)?.accepted_at || new Date().toISOString(),
    health: {
      state: "active",
      evaluated_at: new Date().toISOString(),
      last_success_at: new Date().toISOString(),
      last_item_at: ordered().find((r) => r.namespace === namespace)?.accepted_at,
    },
  });
  const subscription = ([id, name]) => ({
    subscription_id: id,
    name,
    owner: OWNER,
    enabled: true,
    current_version: { saved_query_id: `sq_${id}`, saved_query_version_id: `sqv_${id}` },
  });

  const server = http.createServer(async (req, res) => {
    const url = new URL(req.url, "http://core");
    const path = url.pathname;
    const route = path.replace(/\/(rec|ver|sub|sq|sqv|con)_[\w-]+/g, "/:id");
    calls.set(route, (calls.get(route) || 0) + 1);
    if (path === "/fake/calls") return json(res, 200, Object.fromEntries(calls));
    if (latency) await new Promise((resolve) => setTimeout(resolve, latency));
    if (path === "/v0/changes/stream") {
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write(": open\n\n");
      streams.add(res);
      const keep = setInterval(() => res.write(": keepalive\n\n"), 10000);
      req.on("close", () => {
        clearInterval(keep);
        streams.delete(res);
      });
      return;
    }
    if (path === "/v0/changes") return json(res, 200, { items: [], next_cursor: "c0", has_more: false });
    if (path === "/v0/records/count") return json(res, 200, { count: inBounds(url).length });
    if (path === "/v0/records") {
      const key = url.search.replace(/[?&]page_cursor=[^&]*/, "");
      const offset = offsetOf(url.searchParams.get("page_cursor"), key);
      if (offset < 0) return json(res, 409, { code: "cursor_scope_changed" });
      const limit = Number(url.searchParams.get("limit") || 100);
      const listed = page(inBounds(url), offset, limit, key);
      return json(res, 200, { ...listed, items: listed.items.map(record) });
    }
    let m = path.match(/^\/v0\/records\/([\w-]+)(?:\/versions\/([\w-]+))?$/);
    if (m) {
      const r = byId.get(m[1]);
      const v = m[2] && versions.get(m[2]);
      if (!r || (m[2] && (!v || v.record_id !== r.record_id))) return json(res, 404, { code: "not_found" });
      return json(res, 200, m[2] ? version(v) : record(r));
    }
    if (path === "/v0/connectors") return json(res, 200, { items: SOURCES.map(connector) });
    if (path === "/v0/connector-kinds")
      return json(res, 200, {
        credential_deposits: "available",
        min_interval_seconds: 60,
        items: [
          {
            kind: "rss",
            title: "Flux RSS ou Atom",
            config_schema: { type: "object", properties: { url: { type: "string" } } },
            credential: "none",
            default_interval_seconds: 900,
          },
        ],
      });
    m = path.match(/^\/v0\/connectors\/([\w-]+)$/);
    if (m) {
      const s = SOURCES.find(([id]) => id === m[1]);
      return s ? json(res, 200, connector(s)) : json(res, 404, { code: "not_found" });
    }
    if (path === "/v0/subscriptions") return json(res, 200, { items: alerts.map(subscription) });
    m = path.match(/^\/v0\/subscriptions\/([\w-]+)$/);
    if (m) {
      const a = alerts.find(([id]) => id === m[1]);
      return a ? json(res, 200, subscription(a)) : json(res, 404, { code: "not_found" });
    }
    m = path.match(/^\/v0\/saved-queries\/sq_([\w-]+)\/versions\/[\w-]+$/);
    if (m) {
      const a = alerts.find(([id]) => id === m[1]);
      if (!a) return json(res, 404, { code: "not_found" });
      return json(res, 200, {
        definition: { corpus_ids: ["demo"], expression: { kind: "keywords", match: { any: a[2].map((term) => ({ term })) } } },
      });
    }
    if (path === "/v0/matches") {
      const a = alerts.find(([id]) => id === url.searchParams.get("subscription_id"));
      const key = `m|${a?.[0]}`;
      const offset = Math.max(0, offsetOf(url.searchParams.get("page_cursor"), key));
      return json(res, 200, page(a ? matchesOf(a[2]) : [], offset, 100, key));
    }
    if (path === "/v0/search/profiles" || path === "/v0/search") return json(res, 200, { items: [] });
    json(res, 404, { code: "not_found", message: path });
  });

  const announce = (r) => {
    event += 1;
    const data = JSON.stringify({
      event_id: `e${event}`,
      type: "record.updated",
      resource: { kind: "record", id: r.record_id },
      occurred_at: new Date().toISOString(),
    });
    for (const res of streams) res.write(`id: c${event}\nevent: change\ndata: ${data}\n\n`);
  };
  return {
    server,
    /** A new Version of a Record, accepted now, announced on the change stream. */
    correct(id, title) {
      const r = byId.get(`rec_${id}`);
      r.version_id = `${r.version_id}_2`;
      r.accepted_at = new Date().toISOString();
      r.title = title;
      versions.set(r.version_id, { ...r });
      sorted = false;
      announce(r);
    },
    /** A Record withdrawn, announced on the change stream. */
    withdraw(id) {
      const r = byId.get(`rec_${id}`);
      r.withdrawn = true;
      announce(r);
    },
    calls: () => Object.fromEntries(calls),
  };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const count = Number(process.env.FAKE_RECORDS || 12000);
  const days = Number(process.env.FAKE_DAYS || 10);
  const port = Number(process.env.FAKE_PORT || 7700);
  const core = fakeCore({
    records: generate({ count, days }),
    alerts: [
      ["sub_storm", "Orages et grêle", ["orages", "grêle"]],
      ["sub_tram", "Grèves dans les transports", ["tramway", "grève"]],
      ["sub_health", "Santé", ["hôpital"]],
    ],
    latency: Number(process.env.FAKE_LATENCY_MS || 5),
  });
  core.server.listen(port, "127.0.0.1", () =>
    console.log(`fake core: ${count} Records over ${days} days on http://127.0.0.1:${port}`),
  );
}
