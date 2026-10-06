// A fake engine behind the demo's facade routes, for the dashboard specs: the
// browser talks to the real bundle, and every /demo and /v0 call it makes is
// answered here from an in-memory workspace. The feed's live stream comes from
// a small local SSE server so a test decides when an article arrives.
// Synthetic, neutral content only.
import http from "node:http";
import type { AddressInfo } from "node:net";
import type { Page, Route } from "@playwright/test";
// The facade's own counting, over this workspace's articles.
import { feedStats, filterOf, sourceStats } from "../stats.mjs";
import { topics } from "../topics.mjs";
// The facade's reading of fields, and its facet counts (THE-1171, THE-1184).
import { COMMON_FIELDS, countFacets, fieldValues } from "../explore.mjs";

const minutes = (n: number) => new Date(Date.now() - n * 60000).toISOString();
/** A local time `n` days ago, at that hour. */
export const daysAgo = (n: number, hour: number) => {
  const at = new Date();
  at.setDate(at.getDate() - n);
  at.setHours(hour, 0, 0, 0);
  return at.toISOString();
};

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
  /** Its corpus, when not the demo corpus. */
  corpus_id?: string;
  /** Its common metadata (quivr.metadata) and its corpus's own extension. */
  metadata?: Record<string, unknown>;
  own?: Record<string, unknown>;
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
      created_at: minutes(20 * 24 * 60),
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
  // Older than the feed's snapshot: reached by picking their day.
  const archive: Article[] = [
    article("port", "Dépêches exemple", 0, "Le port rouvre après trois jours de fermeture", "Le trafic reprend au port après trois jours de fermeture.", ["transports"], { received_at: daysAgo(3, 18) }),
    article("bridge", "Revue technique", 0, "Un pont suspendu inspecté par drone", "Des drones ont inspecté le pont suspendu pendant la nuit.", ["industrie"], { received_at: daysAgo(3, 12) }),
    article("market", "Dépêches exemple", 0, "Le marché couvert rouvre ses portes", "Après travaux, le marché couvert rouvre ce matin.", ["commerce"], { received_at: daysAgo(3, 8) }),
    article("archive", "Dépêches exemple", 0, "Les archives municipales numérisées", "Les archives municipales sont désormais consultables en ligne.", ["culture"], { received_at: daysAgo(5, 10) }),
  ];
  // A second corpus the demo reads, with a field of its own (desk), for the
  // Explorer and the feed's corpus picker.
  const wire = (id: string, age: number, title: string, body: string, metadata: Record<string, unknown>, desk: string, extra: Partial<Article> = {}) =>
    article(id, "Agence exemple", age, title, body, [], { corpus_id: "wires", metadata, own: { desk }, link: undefined, ...extra });
  const wires: Article[] = [
    wire("wport", 30, "Port reopens after three-day closure", "Traffic resumed at the port on Monday after a three-day closure.", { language: "en", subjects: ["transport"], published_at: daysAgo(0, 6) }, "economy", { previous: { version_id: "ver_wport_0", title: "Port remains closed for a third day", body: "Traffic is still halted at the port for a third day." } }),
    wire("wgrain", 90, "Récolte de blé : les prix reculent", "Les prix du blé reculent après une récolte abondante.", { language: "fr", subjects: ["agriculture"], published_at: daysAgo(1, 9) }, "economy"),
    wire("wcup", 150, "Cup final moved to Sunday", "The cup final has been moved to Sunday because of the storms.", { language: "en", subjects: ["sport"], published_at: daysAgo(1, 7) }, "sport"),
    wire("wvote", 260, "Le conseil vote le budget", "Le conseil a voté le budget de l’année prochaine.", { language: "fr", subjects: ["politique"], published_at: daysAgo(2, 11) }, "politics"),
  ];
  const wireIncoming: Article[] = [
    wire("wflash", 0, "Flash : le tunnel rouvre à la circulation", "Le tunnel rouvre à la circulation ce soir.", { language: "fr", subjects: ["transport"] }, "economy"),
  ];
  const corpora = [
    { corpus_id: "demo", name: "Espace démo", demo: true, common: COMMON_FIELDS, own: [] },
    {
      corpus_id: "wires",
      name: "Dépêches d’agence",
      demo: false,
      common: COMMON_FIELDS,
      own: [{ name: "desk", type: "string", source_pointer: "/extensions/wire.item/data/desk" }],
    },
  ];
  return { articles, incoming, alerts, connectors, archive, wires, wireIncoming, corpora };
}

export type Workspace = ReturnType<typeof workspace>;

const feedItem = (a: Article) => {
  const item: Record<string, unknown> = {
    record_id: a.record_id,
    version_id: a.version_id,
    corpus_id: a.corpus_id || "demo",
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
  /**
   * Search requests the app sent; sources is their source filter, if any, and
   * profile is set when it is not "default".
   */
  searches: { query: string; mode: string; limit: number; sources?: string[]; profile?: string }[];
  /**
   * sourceFilter false answers like a corpus built before source filtering.
   * profiles is the list GET /v0/search/profiles answers (absent: 404, like an
   * older engine). deep picks how a `deep` search answers: re-ranked by Jev,
   * in hybrid order because the re-ranker was unavailable, or past its deadline.
   */
  options: {
    sourceFilter: boolean;
    profiles?: { name: string; provider: { kind: string; plugin_id?: string } }[];
    deep: "jev" | "fallback" | "deadline";
  };
  /** Sends the next incoming article on the live stream. */
  arrive: () => Article;
  /** Sends the next incoming article of the second corpus on the live stream. */
  arriveWire: () => Article;
  close: () => Promise<void>;
}

export async function fakeEngine(page: Page, ws = workspace()): Promise<Engine> {
  const sent: Engine["sent"] = [];
  const searches: Engine["searches"] = [];
  const options: Engine["options"] = { sourceFilter: true, deep: "jev" };
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
    created_at: "created_at" in a ? a.created_at : undefined,
  });
  // Every stored article of the corpora asked (the demo corpus by default).
  const stored = (corpora = ["demo"]) => [
    ...(corpora.includes("demo") ? [...ws.articles, ...ws.archive] : []),
    ...(corpora.includes("wires") ? ws.wires : []),
  ];
  const find = (id: string) => stored(["demo", "wires"]).find((a) => a.record_id === id);
  // A Version as the engine returns it: its text, metadata and own fields.
  const versionOf = (a: Article, version_id = a.version_id) => {
    const shown = a.previous?.version_id === version_id ? a.previous : a;
    const parts =
      a.namespace === "web-demo"
        ? [{ key: "text", role: "body", content: { kind: "text", text: shown.body } }]
        : [
            { key: "title", role: "title", content: { kind: "text", text: shown.title } },
            { key: "body", role: "body", content: { kind: "text", text: shown.body } },
          ];
    const extensions: Record<string, unknown> = {
      "quivr.metadata": { schema_version: "1", data: { language: "fr", ...a.metadata } },
    };
    if (a.own) extensions["wire.item"] = { schema_version: "1", data: a.own };
    return {
      record_id: a.record_id,
      version_id: shown.version_id,
      accepted_at: shown === a ? a.received_at : minutes(24 * 60),
      manifest: { parts },
      extensions,
      provenance: { source_blob_ids: a.corpus_id ? [`blob_${a.record_id}`] : [], producer: a.corpus_id ? "wire-normalizer" : "rss" },
      availability: { state: "retrieval_ready", searchable: true, is_current: shown === a },
    };
  };
  // The corpora a facade request picks (?corpora=), the demo corpus by default.
  const pickedOf = (url: URL) => (url.searchParams.get("corpora") || "demo").split(",");
  const inCorpora = (corpora: string[]) => [
    ...(corpora.includes("demo") ? ws.articles : []),
    ...(corpora.includes("wires") ? ws.wires : []),
  ];
  type Predicate = { field: string; any_of?: unknown[]; gte?: string; lte?: string };
  const fieldsOf = (id: string) => {
    const c = ws.corpora.find((x) => x.corpus_id === id)!;
    return [...c.common, ...c.own];
  };
  // Like the engine: a predicate, or a counted field, on a field a corpus
  // lacks excludes it.
  const matching = (corpora: string[], predicates: Predicate[], counted: string[] = []) => {
    const excluded = corpora
      .map((id) => ({
        corpus_id: id,
        fields: [...new Set([...predicates.map((p) => p.field), ...counted])].filter(
          (f) => !fieldsOf(id).some((x) => x.name === f),
        ),
      }))
      .filter((e) => e.fields.length);
    const kept = inCorpora(corpora.filter((id) => !excluded.some((e) => e.corpus_id === id))).filter((a) =>
      predicates.every((p) => {
        const field = fieldsOf(a.corpus_id || "demo").find((f) => f.name === p.field)!;
        const values = fieldValues(versionOf(a), field);
        if (p.any_of) return values.some((v: unknown) => p.any_of!.includes(v));
        return values.some((v: unknown) => (!p.gte || String(v) >= p.gte) && (!p.lte || String(v) <= p.lte));
      }),
    );
    return { kept: kept.sort((a, b) => arrived(b) - arrived(a)), excluded };
  };
  const explore = (url: URL) => {
    const corpora = pickedOf(url);
    const predicates: Predicate[] = JSON.parse(url.searchParams.get("metadata") || "[]");
    return { corpora, predicates, ...matching(corpora, predicates) };
  };
  // Like the engine's POST /v0/facets: documents per value under the
  // predicates, each value once per document, a date by its UTC period.
  const facetCounts = (body: {
    corpus_ids: string[];
    fields: { field: string; limit?: number; interval?: "day" | "month" | "year" }[];
    filter?: { metadata?: Predicate[] };
  }) => {
    const { kept, excluded } = matching(body.corpus_ids, body.filter?.metadata || [], body.fields.map((f) => f.field));
    const start = { year: "-01-01T00:00:00Z", month: "-01T00:00:00Z", day: "T00:00:00Z" };
    const length = { year: 4, month: 7, day: 10 };
    return {
      items: body.fields.map(({ field, limit = 20, interval }) => {
        const counts = new Map<unknown, number>();
        for (const a of kept) {
          const f = fieldsOf(a.corpus_id || "demo").find((x) => x.name === field)!;
          const values = fieldValues(versionOf(a), f).map((v: unknown) =>
            interval ? new Date(String(v)).toISOString().slice(0, length[interval]) + start[interval] : v,
          );
          for (const v of new Set(values)) counts.set(v, (counts.get(v) || 0) + 1);
        }
        const buckets = [...counts]
          .map(([value, count]) => ({ value, count }))
          .sort((a, b) => b.count - a.count || String(a.value).localeCompare(String(b.value)))
          .slice(0, limit);
        if (interval) buckets.sort((a, b) => String(a.value).localeCompare(String(b.value)));
        return { field, buckets };
      }),
      ...(excluded.length ? { excluded_corpora: excluded } : {}),
    };
  };
  const arrived = (a: Article) => Date.parse(a.received_at!);
  // What the facade's index holds: every article, dated, and what alerts caught.
  const rows = (corpora?: string[]) =>
    stored(corpora).map((a) => ({ record_id: a.record_id, version_id: a.version_id, namespace: a.namespace, at: arrived(a) }));
  const catchesOf = () => {
    const out = new Map<string, string[]>();
    for (const a of ws.alerts)
      for (const record of Object.keys(a.caught)) out.set(record, [...(out.get(record) || []), a.alert_id]);
    return out;
  };
  const instant = (value?: string) => (value ? Date.parse(value) : undefined);

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
      return json(route, { items: inCorpora(pickedOf(url)).map(feedItem), live: true });
    if (path === "/demo/corpora") return json(route, { items: ws.corpora });
    if (path === "/demo/explore") {
      const { kept, excluded } = explore(url);
      const start = Number(url.searchParams.get("cursor") || 0);
      return json(route, {
        items: kept.slice(start, start + 3).map((a) => {
          const v = versionOf(a);
          const c = ws.corpora.find((x) => x.corpus_id === (a.corpus_id || "demo"))!;
          const metadata: Record<string, unknown[]> = {};
          for (const f of [...c.common, ...c.own]) {
            const values = fieldValues(v, f);
            if (values.length) metadata[f.name] = values;
          }
          return { ...feedItem(a), metadata };
        }),
        next_cursor: start + 3 < kept.length ? String(start + 3) : undefined,
        ...(excluded.length ? { excluded_corpora: excluded } : {}),
      });
    }
    if (path === "/demo/explore/facets") {
      const { corpora, predicates } = explore(url);
      const own = corpora.length === 1 ? ws.corpora.find((c) => c.corpus_id === corpora[0])!.own : [];
      return json(
        route,
        await countFacets({
          count: async (body: Parameters<typeof facetCounts>[0]) => facetCounts(body),
          ids: corpora,
          fields: [...COMMON_FIELDS, ...own],
          predicates,
        }),
      );
    }
    const explored = path.match(/^\/demo\/explore\/records\/([\w-]+)$/);
    if (explored) {
      const a = find(explored[1]);
      if (!a) return json(route, { message: "Document introuvable." }, 404);
      const corpus = ws.corpora.find((c) => c.corpus_id === (a.corpus_id || "demo"));
      return json(route, {
        record: {
          record_id: a.record_id,
          source: { corpus_id: corpus!.corpus_id, namespace: a.namespace, record_key: `key-${a.record_id}` },
          withdrawn: false,
          current_version_id: a.version_id,
        },
        version: versionOf(a),
        versions: [
          { version_id: a.version_id, accepted_at: a.received_at },
          ...(a.previous ? [{ version_id: a.previous.version_id, accepted_at: minutes(24 * 60) }] : []),
        ],
        corpus,
        blobs: a.corpus_id
          ? [{ blob_id: `blob_${a.record_id}`, size_bytes: 3172, sha256: "c0ffee".repeat(10) + "abcd", media_type: "application/vnd.iptc.g2.newsitem+xml" }]
          : [],
      });
    }
    // Like the facade: Quivr's counts per period, and a day newest first,
    // two articles a page so that paging shows.
    if (path === "/demo/feed/days") {
      const bounds = (url.searchParams.get("bounds") || "").split(",").map(Date.parse);
      if (bounds.some((b, i) => Number.isNaN(b) || (i > 0 && b >= bounds[i - 1])))
        return json(route, { message: "Cette période n’est pas valide." }, 422);
      const all = stored(pickedOf(url));
      return json(route, {
        total: all.length,
        days: bounds.slice(1).map(
          (after, i) => all.filter((a) => arrived(a) >= after && arrived(a) < bounds[i]).length,
        ),
        older: all.filter((a) => arrived(a) < bounds.at(-1)!).length,
        as_of: new Date().toISOString(),
      });
    }
    if (path === "/demo/feed/page") {
      const after = Date.parse(url.searchParams.get("after") || "");
      const before = Date.parse(url.searchParams.get("before") || "");
      if (Number.isNaN(after) || Number.isNaN(before) || after >= before)
        return json(route, { message: "Cette période n’est pas valide." }, 422);
      const start = Number(url.searchParams.get("cursor") || 0);
      const day = stored(pickedOf(url))
        .filter((a) => arrived(a) >= after && arrived(a) < before)
        .sort((a, b) => arrived(b) - arrived(a));
      return json(route, {
        items: day.slice(start, start + 2).map(feedItem),
        next_cursor: start + 2 < day.length ? String(start + 2) : undefined,
      });
    }
    if (path === "/demo/feed/stats" && method === "POST")
      return json(route, {
        ...feedStats(
          rows(pickedOf(url)),
          {
            after: instant(body.after),
            before: instant(body.before),
            buckets: (body.buckets || []).map(Date.parse),
            sources: body.sources,
            muted: body.muted,
            alerts: body.alerts,
            read: body.read,
            since: instant(body.since),
            readIds: body.read_ids,
          },
          catchesOf(),
        ),
        building: false,
      });
    if (path === "/demo/feed/topics") {
      const p = url.searchParams;
      const keep = filterOf(
        {
          after: instant(p.get("after") || undefined),
          before: instant(p.get("before") || undefined),
          sources: p.getAll("source"),
          muted: p.getAll("muted"),
          alerts: p.getAll("alert"),
        },
        catchesOf(),
      );
      const all = rows(pickedOf(url));
      const titles = stored(pickedOf(url))
        .filter((_, i) => keep(all[i]))
        .map((a) => a.title);
      return json(route, { items: topics(titles, 10), building: false });
    }
    if (path === "/demo/sources/stats") {
      const bounds = (url.searchParams.get("bounds") || "").split(",").map(Date.parse);
      return json(route, { sources: sourceStats(rows(), bounds, catchesOf()), building: false });
    }
    if (path === "/v0/changes")
      return json(route, { items: [], next_cursor: "c0", has_more: false });
    if (path === "/v0/search/profiles" && options.profiles)
      return json(route, { items: options.profiles });
    if (path === "/v0/search") {
      const sources: string[] | undefined = body.filter?.source_namespaces;
      const profile = body.profile === "default" ? undefined : body.profile;
      searches.push({ query: body.query, mode: body.mode, limit: body.limit, sources, profile });
      if (profile === "deep" && options.deep === "deadline")
        return json(route, { code: "search_deadline_exceeded", message: "search deadline exceeded", retryable: true }, 504);
      if (sources && !options.sourceFilter)
        return json(route, { code: "source_filter_unavailable", message: "source filter unavailable", retryable: false }, 422);
      const typed = words(body.query);
      const meaning = new Set(typed.flatMap((w) => MEANINGS[w] || []));
      const hits = [];
      for (const a of inCorpora(body.corpus_ids)) {
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
        const seed = inCorpora(body.corpus_ids).find((a) => body.query.startsWith(a.title));
        const related = inCorpora(body.corpus_ids).filter(
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
          retrieval_profile: { name: "default", version: "1" },
        });
      }
      if (profile === "deep") {
        // Like jev.rerank: the passages with a typed word first, each with its
        // probability; or hybrid order when the re-ranker was unavailable.
        if (options.deep === "fallback")
          return json(route, {
            items: hits.map((h) => ({ ...h, explanation: "re-ranker unavailable: deadline" })),
            retrieval_profile: { name: "deep", version: "plugin:jev.rerank@0.1.0/deep" },
            usage: { rounds: 2, elapsed_ms: 2140, paid_calls: 0, cost_cents: 0 },
          });
        const scored = hits
          .map((h) => {
            const literal = typed.some((w) => fold(h.excerpt.text).includes(w));
            return { h, p: literal ? 0.91 - h.rank / 100 : 0.12 };
          })
          .sort((a, b) => b.p - a.p);
        return json(route, {
          items: scored.map(({ h, p }, i) => ({
            ...h,
            rank: i + 1,
            explanation: `Jev jev-1.13.0, rubric answers-query-v1, noul; noul=${p}`,
          })),
          retrieval_profile: { name: "deep", version: "plugin:jev.rerank@0.1.0/deep" },
          usage: { rounds: 2, elapsed_ms: 1420, paid_calls: 1, cost_cents: 0.4 },
        });
      }
      return json(route, { items: hits, retrieval_profile: { name: "default", version: "1" } });
    }
    const version = path.match(/^\/v0\/records\/([\w-]+)\/versions\/([\w-]+)$/);
    if (version) {
      const a = find(version[1]);
      if (!a) return json(route, { message: "Document introuvable." }, 404);
      return json(route, versionOf(a, version[2]));
    }
    if (path === "/demo/alerts" && method === "GET") {
      const matched: Record<string, string[]> = {};
      for (const a of ws.alerts)
        for (const record of Object.keys(a.caught))
          (matched[record] ||= []).push(a.alert_id);
      const all = rows();
      const namespaces = [...new Set(all.map((r) => r.namespace))];
      const oldest = Math.min(...all.map((r) => r.at));
      const records: Record<string, [string, number, number]> = {};
      for (const r of all)
        if (matched[r.record_id])
          records[r.record_id] = [r.version_id, namespaces.indexOf(r.namespace), Math.round(r.at / 1000)];
      return json(route, {
        available: true,
        described: true,
        items: ws.alerts.map((a) => {
          const start = "created_at" in a && a.created_at ? Date.parse(a.created_at) : oldest;
          return { ...alertView(a), arrived: all.filter((r) => r.at >= start).length };
        }),
        matched,
        dated: { namespaces, records, oldest: new Date(oldest).toISOString(), building: false },
      });
    }
    if (path === "/demo/alerts" && method === "POST") {
      const a = {
        alert_id: `alert_${ws.alerts.length + 1}`,
        name: body.name,
        enabled: true,
        // The facade notes when it creates an alert.
        created_at: new Date().toISOString(),
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
      if (action[2] === "edit") {
        a.expression = body.expression;
        if (body.name) a.name = body.name;
      }
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
    const schedule = path.match(/^\/v0\/connectors\/([\w-]+)\/schedule$/);
    if (schedule && method === "PUT") {
      const c = ws.connectors.find((x) => x.connector_id === schedule[1])!;
      c.schedule = { interval_seconds: body.interval_seconds };
      return json(route, c);
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
    if (path === "/demo/sources/rename") {
      // Like the facade: the name of the namespace, on every instance.
      const c = ws.connectors.find((x) => x.connector_id === body.connector_id)!;
      const name = String(body.name).trim();
      for (const x of ws.connectors)
        if (x.source_namespace === c.source_namespace)
          Object.assign(x, { display_name: name && name !== c.source_namespace ? name : undefined });
      return json(route, { source_namespace: c.source_namespace, display_name: name || null });
    }
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
    arriveWire() {
      const next = ws.wireIncoming.shift()!;
      next.received_at = new Date().toISOString();
      ws.wires.unshift(next);
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
