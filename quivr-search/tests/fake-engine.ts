// A fake engine behind the demo's facade routes, for the dashboard specs: the
// browser talks to the real bundle, and every /demo and /v0 call it makes is
// answered here from an in-memory workspace. The feed's live stream comes from
// a small local SSE server so a test decides when an article arrives.
// Synthetic, neutral content only.
import http from "node:http";
import type { AddressInfo } from "node:net";
import type { Page, Route } from "@playwright/test";

const minutes = (n: number) => new Date(Date.now() - n * 60000).toISOString();

export interface Article {
  record_id: string;
  version_id: string;
  namespace: string;
  title: string;
  body: string;
  /** Words a meaning-based search relates to this article. */
  topics: string[];
  received_at?: string;
  published_at?: string;
  link?: string;
  updated_at?: string;
  /** The Version a correction replaced. */
  previous?: { version_id: string; title: string; body: string };
}

const article = (
  id: string,
  namespace: string,
  age: number,
  title: string,
  body: string,
  topics: string[],
  extra: Partial<Article> = {},
): Article => ({
  record_id: `rec_${id}`,
  version_id: `ver_${id}`,
  namespace,
  title,
  body,
  topics,
  received_at: minutes(age),
  link: namespace === "web-demo" ? undefined : `https://news.example.org/${id}`,
  ...extra,
});

export function workspace() {
  const articles: Article[] = [
    article("storm", "Dépêches exemple", 2, "Orages : la grêle frappe les vergers de la vallée", "Un violent épisode orageux a traversé la vallée en fin d’après-midi. Des grêlons ont été signalés autour du village, où plusieurs vergers ont perdu leur récolte.\n\nLa préfecture a activé sa cellule de crise.", ["meteo", "intemperies", "agriculture"]),
    article("cell", "Météo locale", 5, "Cellule orageuse active au nord de la ville, grêle observée", "Cellule orageuse très active au nord de la ville, grêle observée depuis 20 h 40. Rafales estimées à 90 km/h.", ["meteo", "intemperies"]),
    article("note", "web-demo", 9, "Compte rendu de la réunion du matin", "Compte rendu de la réunion du matin\nPriorités du jour : suivi des intempéries, point sur les transports.", ["redaction"], { link: undefined }),
    article("tram", "Revue technique", 14, "Les conducteurs de tramway cessent le travail", "Les conducteurs du réseau de tramway ont cessé le travail ce matin. Les lignes sont interrompues jusqu’à nouvel ordre.", ["transports", "social"], { updated_at: minutes(4), previous: { version_id: "ver_tram_0", title: "Les conducteurs de tramway menacent de cesser le travail", body: "Les conducteurs du réseau de tramway menacent de cesser le travail demain. Les lignes restent ouvertes." } }),
    article("match", "Dépêches exemple", 25, "Football : le match du soir reporté en raison des orages", "La rencontre prévue ce soir a été reportée après la chute de grêle sur la pelouse.", ["sport", "meteo"]),
    article("flood", "Dépêches exemple", 70, "Inondations : la mairie ouvre un gymnase", "Après les pluies de la nuit, la mairie a ouvert un gymnase pour accueillir les personnes évacuées.", ["meteo", "intemperies"]),
    article("battery", "Revue technique", 130, "Batteries : une usine pilote annoncée", "Une jeune entreprise annonce une ligne pilote de batteries sodium-ion, moins dépendantes du lithium.", ["industrie", "energie"]),
    article("water", "Dépêches exemple", 200, "Sécheresse : des restrictions d’eau dans plusieurs départements", "Malgré les orages récents, plusieurs départements restent soumis à des restrictions d’eau.", ["meteo", "environnement"]),
  ];
  const incoming: Article[] = [
    article("hail", "Météo locale", 0, "Grêlons de quatre centimètres signalés sur la plaine", "Des grêlons de quatre centimètres ont été signalés sur la plaine après le passage de l’orage.", ["meteo", "intemperies"]),
    article("bus", "Revue technique", 0, "Les bus remplacent le tramway sur deux lignes", "Des bus remplacent le tramway sur deux lignes pendant l’arrêt de travail des conducteurs.", ["transports", "social"]),
  ];
  const alerts = [
    {
      alert_id: "alert_storm",
      name: "Orages et grêle",
      enabled: true,
      kind: "keywords",
      expression: {
        kind: "keywords",
        match: {
          all: [
            { any: [{ term: "orage" }, { term: "grêle" }] },
            { not: { term: "football" } },
          ],
        },
      },
      caught: { rec_storm: ["orage", "grêle"], rec_cell: ["grêle"] } as Record<string, string[]>,
      scores: {} as Record<string, number>,
    },
    {
      alert_id: "alert_transport",
      name: "Grèves dans les transports",
      enabled: true,
      kind: "described",
      expression: {
        kind: "described",
        description: "Les grèves dans les transports publics",
      },
      caught: { rec_tram: [] } as Record<string, string[]>,
      scores: { rec_tram: 0.91 } as Record<string, number>,
    },
    {
      alert_id: "alert_port",
      name: "Ports et quais",
      enabled: false,
      kind: "keywords",
      expression: {
        kind: "keywords",
        match: {
          all: [
            { any: [{ term: "port" }, { term: "quai" }] },
            { term: "grève" },
            { not: { any: [{ term: "football" }, { term: "rugby" }] } },
          ],
        },
      },
      caught: {} as Record<string, string[]>,
      scores: {} as Record<string, number>,
    },
  ];
  const connector = (
    id: string,
    namespace: string,
    url: string,
    health: Record<string, unknown>,
    extra: Record<string, unknown> = {},
  ) => ({
    connector_id: id,
    corpus_id: "demo",
    source_namespace: namespace,
    kind: "rss",
    config: { url },
    schedule: { interval_seconds: 900 },
    health_policy: { silent_after_seconds: 86400, credential_warning_seconds: 604800 },
    enabled: true,
    created_at: minutes(600),
    health: { evaluated_at: minutes(1), ...health },
    ...extra,
  });
  const connectors = [
    connector("con_wire", "Dépêches exemple", "https://news.example.org/feed.xml", { state: "active", last_success_at: minutes(1), last_item_at: minutes(2) }),
    connector("con_tech", "Revue technique", "https://tech.example.com/rss", { state: "active", last_success_at: minutes(3), last_item_at: minutes(14) }),
    connector("con_weather", "Météo locale", "https://weather.example.net/feed", { state: "active", last_success_at: minutes(40), last_item_at: minutes(5), last_error: { code: "timeout", at: minutes(10) } }),
  ];
  return { articles, incoming, alerts, connectors };
}

export type Workspace = ReturnType<typeof workspace>;

const feedItem = (a: Article) => {
  const item: Record<string, unknown> = {
    record_id: a.record_id,
    version_id: a.version_id,
    namespace: a.namespace,
    title: a.title,
    excerpt: a.body.slice(0, 200),
  };
  for (const key of ["received_at", "published_at", "link", "updated_at"] as const)
    if (a[key]) item[key] = a[key];
  if (a.previous) item.previous_version_id = a.previous.version_id;
  return item;
};

const fold = (text: string) =>
  text.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();
const words = (text: string) =>
  fold(text).split(/[^\p{L}\p{N}]+/u).filter((w) => w.length > 2);
// Crude synonyms for a meaning-based search.
const MEANINGS: Record<string, string[]> = {
  tempete: ["meteo", "intemperies"],
  orage: ["meteo", "intemperies"],
  intemperies: ["meteo", "intemperies"],
  greve: ["social", "transports"],
};

// A stand-in for the alerts plugin in previews: a keyword tree over the
// title and body (folded substrings), a source filter, and a described alert
// that fits the articles sharing a topic with its words (MEANINGS).
function caughtBy(expression: any, a: Article): boolean {
  if (expression.kind === "described") {
    const meaning = new Set(words(expression.description).flatMap((w) => MEANINGS[w] || []));
    return a.topics.some((t) => meaning.has(t));
  }
  const text = fold(`${a.title} ${a.body}`);
  const test = (node: any): boolean =>
    "term" in node ? text.includes(fold(node.term))
    : "field" in node ? a.namespace === node.equals
    : "all" in node ? node.all.every(test)
    : "any" in node ? node.any.some(test)
    : !test(node.not);
  return test(expression.match);
}

export interface Engine {
  ws: Workspace;
  /** POST bodies the app sent, by path. */
  sent: { path: string; body: any }[];
  /** Search requests the app sent; sources is their source filter, if any. */
  searches: { query: string; mode: string; limit: number; sources?: string[] }[];
  /** sourceFilter false answers like a corpus built before source filtering. */
  options: { sourceFilter: boolean };
  /** Sends the next incoming article on the live stream. */
  arrive: () => Article;
  close: () => Promise<void>;
}

export async function fakeEngine(page: Page, ws = workspace()): Promise<Engine> {
  const sent: Engine["sent"] = [];
  const searches: Engine["searches"] = [];
  const options: Engine["options"] = { sourceFilter: true };
  const streams = new Set<http.ServerResponse>();
  const server = http.createServer((req, res) => {
    res.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-store",
      "Access-Control-Allow-Origin": "*",
    });
    res.write(`retry: 500\nevent: status\ndata: {"live":true}\n\n`);
    streams.add(res);
    req.on("close", () => streams.delete(res));
  });
  server.listen(0, "127.0.0.1");
  await new Promise((resolve) => server.once("listening", resolve));
  const streamURL = `http://127.0.0.1:${(server.address() as AddressInfo).port}/stream`;

  const json = (route: Route, data: unknown, status = 200) =>
    route.fulfill({ status, contentType: "application/json", json: data });
  const alertView = (a: Workspace["alerts"][number]) => ({
    alert_id: a.alert_id,
    name: a.name,
    enabled: a.enabled,
    kind: a.kind,
    expression: a.expression,
    match_count: Object.keys(a.caught).length,
    capped: false,
  });
  const find = (id: string) => ws.articles.find((a) => a.record_id === id);

  await page.route("**/demo/feed/stream", (route) =>
    route.continue({ url: streamURL }),
  );
  await page.route(/\/(demo|v0)\//, async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    const method = request.method();
    const body = method === "GET" ? undefined : request.postDataJSON();
    if (body !== undefined) sent.push({ path, body });
    if (path === "/demo/feed/stream") return route.continue({ url: streamURL });
    if (path === "/demo/session")
      return json(route, { corpus_id: "demo", name: "Espace démo" });
    if (path === "/demo/feed")
      return json(route, { items: ws.articles.map(feedItem), live: true });
    if (path === "/v0/changes")
      return json(route, { items: [], next_cursor: "c0", has_more: false });
    if (path === "/v0/search") {
      const sources: string[] | undefined = body.filter?.source_namespaces;
      searches.push({ query: body.query, mode: body.mode, limit: body.limit, sources });
      if (sources && !options.sourceFilter)
        return json(route, { code: "source_filter_unavailable", message: "source filter unavailable", retryable: false }, 422);
      const typed = words(body.query);
      const meaning = new Set(typed.flatMap((w) => MEANINGS[w] || []));
      const hits = [];
      for (const a of ws.articles) {
        if (sources && !sources.includes(a.namespace)) continue;
        const text = words(`${a.title} ${a.body}`);
        const literal = typed.some((w) => text.some((t) => t.startsWith(w)));
        const near =
          body.mode === "hybrid" && a.topics.some((t) => meaning.has(t));
        if (!literal && !near) continue;
        const passage = literal
          ? a.body.split(/(?<=[.!?])\s+/).find((s) => typed.some((w) => fold(s).includes(w))) || a.body
          : a.body.split(/(?<=[.!?])\s+/)[0];
        hits.push({
          record_id: a.record_id,
          version_id: a.version_id,
          part_key: "body",
          segment_id: `seg_${a.record_id}`,
          segmentation_id: "s",
          rank: hits.length + 1,
          excerpt: { text: passage, start: 0, end: passage.length, coordinate_system: "unicode_code_point" },
        });
      }
      if (body.mode === "semantic") {
        // The reader's neighbours: articles sharing a topic with the seed's article.
        const seed = ws.articles.find((a) => body.query.startsWith(a.title));
        const related = ws.articles.filter(
          (a) => seed && a !== seed && a.topics.some((t) => seed.topics.includes(t)),
        );
        return json(route, {
          items: [seed, ...related].filter(Boolean).map((a, i) => ({
            record_id: a!.record_id,
            version_id: a!.version_id,
            part_key: "body",
            segment_id: `seg_${a!.record_id}`,
            segmentation_id: "s",
            rank: i + 1,
            excerpt: { text: a!.body.slice(0, 120), start: 0, end: 120, coordinate_system: "unicode_code_point" },
          })),
          retrieval_profile: { name: "balanced", version: "1" },
        });
      }
      return json(route, { items: hits, retrieval_profile: { name: "balanced", version: "1" } });
    }
    const version = path.match(/^\/v0\/records\/([\w-]+)\/versions\/([\w-]+)$/);
    if (version) {
      const a = find(version[1]);
      if (!a) return json(route, { message: "Document introuvable." }, 404);
      const shown = a.previous?.version_id === version[2] ? a.previous : a;
      const parts =
        a.namespace === "web-demo"
          ? [{ key: "text", role: "body", content: { kind: "text", text: shown.body } }]
          : [
              { key: "title", role: "title", content: { kind: "text", text: shown.title } },
              { key: "body", role: "body", content: { kind: "text", text: shown.body } },
            ];
      return json(route, {
        record_id: a.record_id,
        version_id: shown.version_id,
        manifest: { parts },
        availability: { state: "retrieval_ready", searchable: true, is_current: true },
      });
    }
    if (path === "/demo/alerts" && method === "GET") {
      const matched: Record<string, string[]> = {};
      for (const a of ws.alerts)
        for (const record of Object.keys(a.caught))
          (matched[record] ||= []).push(a.alert_id);
      return json(route, {
        available: true,
        described: true,
        items: ws.alerts.map(alertView),
        matched,
      });
    }
    if (path === "/demo/alerts" && method === "POST") {
      const a = {
        alert_id: `alert_${ws.alerts.length + 1}`,
        name: body.name,
        enabled: true,
        kind: body.expression.kind,
        expression: body.expression,
        caught: {},
        scores: {},
      };
      ws.alerts.unshift(a);
      return json(route, alertView(a), 201);
    }
    if (path === "/demo/alerts/preview") {
      const caught = ws.articles.filter((a) => caughtBy(body.expression, a));
      return json(route, {
        evaluated: ws.articles.length,
        matched: caught.length,
        complete: true,
        items: caught.slice(0, 3).map((a) => ({
          record_id: a.record_id,
          version_id: a.version_id,
          available: true,
          title: a.title,
          excerpt: "",
          source: a.namespace,
          explanation: "",
          terms: [],
          fields: [],
          score: null,
          threshold: null,
        })),
      });
    }
    const action = path.match(/^\/demo\/alerts\/([\w-]+)(?:\/(\w+))?$/);
    if (action) {
      const a = ws.alerts.find((x) => x.alert_id === action[1]);
      if (!a) return json(route, { message: "Alerte introuvable." }, 404);
      if (action[2] === "pause") a.enabled = false;
      if (action[2] === "resume") a.enabled = true;
      if (action[2] === "edit") a.expression = body.expression;
      if (action[2] === "delete") {
        ws.alerts.splice(ws.alerts.indexOf(a), 1);
        return json(route, { deleted: true });
      }
      if (action[2]) return json(route, alertView(a));
      return json(route, {
        ...alertView(a),
        matches: Object.entries(a.caught).map(([record, terms]) => {
          const x = find(record)!;
          const score = a.scores[record];
          return {
            match_id: `m_${a.alert_id}_${record}`,
            record_id: record,
            version_id: x.version_id,
            available: true,
            title: x.title,
            excerpt: x.body.slice(0, 160),
            source: x.namespace,
            explanation: "",
            terms: terms.map((term) => ({ term, parts: ["body"] })),
            fields: [],
            score: score ?? null,
            threshold: score === undefined ? null : 0.5,
          };
        }),
      });
    }
    if (path === "/v0/connector-kinds")
      return json(route, {
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
    if (path === "/v0/connectors" && method === "GET")
      return json(route, { items: ws.connectors });
    if (path === "/v0/connectors" && method === "POST") {
      const c = {
        ...ws.connectors[0],
        connector_id: `con_${ws.connectors.length + 1}`,
        source_namespace: body.source_namespace,
        config: body.config,
        schedule: body.schedule || { interval_seconds: 900 },
        enabled: true,
        created_at: new Date().toISOString(),
        health: { state: "active", evaluated_at: new Date().toISOString() },
      };
      ws.connectors.push(c);
      return json(route, c, 201);
    }
    // A requested check that finds the source answering again.
    const runs = path.match(/^\/v0\/connectors\/([\w-]+)\/runs$/);
    if (runs) {
      const c = ws.connectors.find((x) => x.connector_id === runs[1])!;
      const now = new Date().toISOString();
      Object.assign(c.health, { state: "active", evaluated_at: now, last_success_at: now });
      return json(route, { connector_id: c.connector_id, run_at: now }, 202);
    }
    const disable = path.match(/^\/v0\/connectors\/([\w-]+)\/disable$/);
    if (disable) {
      const c = ws.connectors.find((x) => x.connector_id === disable[1])!;
      Object.assign(c, { enabled: false, disabled_at: new Date().toISOString() });
      return json(route, c);
    }
    const one = path.match(/^\/v0\/connectors\/([\w-]+)$/);
    if (one)
      return json(route, ws.connectors.find((x) => x.connector_id === one[1]));
    if (path === "/demo/sources/remove") {
      // Like the facade: every instance of the source's namespace goes.
      const c = ws.connectors.find((x) => x.connector_id === body.connector_id)!;
      const gone = ws.connectors.filter((x) => x.source_namespace === c.source_namespace);
      ws.connectors = ws.connectors.filter((x) => !gone.includes(x));
      return json(route, { removed: gone.map((x) => x.connector_id) });
    }
    if (path === "/demo/feeds/suggestions")
      return json(route, {
        items: [
          { title: "Fil exemple — International", url: "https://world.example.org/rss.xml" },
          { title: "Journal exemple — Sciences", url: "https://science.example.com/feed" },
        ],
      });
    if (path === "/demo/feeds/discover") {
      if (/\/\/(10\.|127\.|192\.168\.)/.test(body.url))
        return json(route, {
          code: "demo_request_failed",
          message: "Cette adresse pointe vers un réseau privé ou local : elle ne peut pas être collectée.",
          retryable: false,
        }, 422);
      const host = new URL(body.url).hostname;
      return json(route, {
        feeds: [{ url: `https://${host}/rss.xml`, title: `${host} — À la une` }],
      });
    }
    return json(route, { message: `Route non simulée : ${method} ${path}` }, 404);
  });

  return {
    ws,
    sent,
    searches,
    options,
    arrive() {
      const next = ws.incoming.shift()!;
      next.received_at = new Date().toISOString();
      ws.articles.unshift(next);
      const frame = `event: item\ndata: ${JSON.stringify(feedItem(next))}\n\n`;
      for (const res of streams) res.write(frame);
      return next;
    },
    async close() {
      for (const res of streams) res.end();
      server.closeAllConnections();
      await new Promise((resolve) => server.close(resolve));
    },
  };
}
