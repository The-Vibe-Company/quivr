// The Admin tab's facade routes, faked for the Goulots and Plugins specs
// (THE-797): the live snapshot, the engine's rollups by window, the active
// plugins and the demo's connectors. A test stops or restarts a plugin, or
// slows a step, and the next read answers accordingly. Synthetic, neutral
// data only; installed after fakeEngine, so these routes win.
import type { Page, Route } from "@playwright/test";

type Window = "1h" | "24h" | "7d";
const RESOLUTION: Record<Window, number> = { "1h": 60, "24h": 900, "7d": 7200 };
const SPAN: Record<Window, number> = { "1h": 3600, "24h": 86400, "7d": 604800 };
const MINUTE = 60000;

interface Bucket {
  count: number;
  errors?: number;
  p50: number;
  p95: number;
  code?: string;
}
/** A bucket `ago` ms before now, per minute of traffic, or null for none. */
type Shape = (ago: number) => Bucket | null;

function series(window: Window, now: number, shape: Shape) {
  const step = RESOLUTION[window] * 1000;
  const to = now;
  const from = to - SPAN[window] * 1000;
  const points: {
    start: string;
    count: number;
    errors: number;
    p50_ms: number;
    p95_ms: number;
  }[] = [];
  let lastError: { code: string; at: string } | undefined;
  for (let t = Math.floor(from / step) * step; t < to; t += step) {
    const minute = shape(to - t - step / 2);
    if (!minute || minute.count <= 0) continue;
    // Traffic per minute, scaled to the bucket.
    const scale = step / MINUTE;
    const count = Math.max(1, Math.round(minute.count * scale));
    const errors = Math.min(count, Math.round((minute.errors || 0) * scale));
    points.push({
      start: new Date(t).toISOString(),
      count,
      errors,
      p50_ms: minute.p50,
      p95_ms: minute.p95,
    });
    if (errors && minute.code)
      lastError = {
        code: minute.code,
        at: new Date(Math.min(to - 20000, t + step - 1000)).toISOString(),
      };
  }
  const count = points.reduce((n, p) => n + p.count, 0);
  const weighted = (f: "p50_ms" | "p95_ms") =>
    count ? points.reduce((n, p) => n + p.count * p[f], 0) / count : undefined;
  return {
    summary: {
      count,
      errors: points.reduce((n, p) => n + p.errors, 0),
      ...(count
        ? {
            p50_ms: weighted("p50_ms"),
            p95_ms: weighted("p95_ms"),
            mean_ms: weighted("p50_ms"),
          }
        : {}),
      ...(lastError
        ? { last_error_code: lastError.code, last_error_at: lastError.at }
        : {}),
    },
    points,
    from: new Date(from).toISOString(),
    to: new Date(to).toISOString(),
  };
}

export interface AdminFake {
  /** The plugin's calls fail as plugin_unavailable from now on. */
  stop: (plugin: string) => void;
  /** The plugin's calls succeed again. */
  restart: (plugin: string) => void;
  /** Every facade read, path and query. */
  reads: string[];
}

export async function fakeAdmin(page: Page): Promise<AdminFake> {
  const stopped = new Set<string>();
  const restarted = new Set<string>();
  const reads: string[] = [];
  const json = (route: Route, data: unknown, status = 200) =>
    route.fulfill({ status, contentType: "application/json", json: data });

  // A stopped plugin's calls of the last 3 minutes failed; once restarted,
  // its last 2 minutes succeed again after those failures.
  const failing =
    (plugin: string, base: Bucket): Shape =>
    (ago) => {
      const down = stopped.has(plugin)
        ? restarted.has(plugin)
          ? ago >= 2 * MINUTE && ago < 5 * MINUTE
          : ago < 3 * MINUTE
        : false;
      return down
        ? {
            ...base,
            errors: base.count,
            code: "plugin_unavailable",
            p95: 30000,
            p50: 30000,
          }
        : base;
    };
  const steady =
    (b: Bucket): Shape =>
    () =>
      b;
  // Vectors got slower over the last 10 minutes.
  const slowed = (): Shape => (ago) =>
    ago < 8 * MINUTE
      ? { count: 6, p50: 2400, p95: 5200 }
      : { count: 6, p50: 700, p95: 1500 };

  const steps: Record<string, Shape> = {
    materialized: steady({ count: 6, p50: 180, p95: 520 }),
    segmented: steady({ count: 6, p50: 40, p95: 110 }),
    retrieval_ready: steady({ count: 6, p50: 95, p95: 260 }),
    enriched: slowed(),
    accepted_to_searchable: steady({ count: 6, p50: 340, p95: 900 }),
    baseline: steady({ count: 6, p50: 130, p95: 340 }),
    enrichment: steady({ count: 6, p50: 610, p95: 1300 }),
  };
  const plugins: {
    id: string;
    version: string;
    operation: string;
    shape: Shape;
  }[] = [
    {
      id: "core.ingest",
      version: "0.2.0",
      operation: "segment_and_embed",
      shape: failing("core.ingest", { count: 12, p50: 180, p95: 420 }),
    },
    {
      id: "core.retrieve",
      version: "0.1.0",
      operation: "search_round",
      shape: steady({ count: 4, p50: 60, p95: 140 }),
    },
    {
      id: "alerts",
      version: "0.2.0",
      operation: "evaluate_subscription",
      shape: failing("alerts", { count: 6, errors: 0, p50: 380, p95: 900 }),
    },
    {
      id: "connector.rss",
      version: "1.0.0",
      operation: "connector_fetch",
      shape: failing("connector.rss", { count: 1, p50: 420, p95: 800 }),
    },
    {
      id: "pdf-text",
      version: "0.1.0",
      operation: "normalize",
      shape: (ago) =>
        ago > 20 * MINUTE && ago < 40 * MINUTE
          ? { count: 0.2, p50: 900, p95: 1400 }
          : null,
    },
  ];
  const active = [
    { plugin_id: "alerts", version: "0.2.0", roles: ["subscription:alerts"] },
    { plugin_id: "core.ingest", version: "0.2.0", roles: ["ingestion"] },
    { plugin_id: "core.retrieve", version: "0.1.0", roles: ["retrieval"] },
    {
      plugin_id: "pdf-text",
      version: "0.1.0",
      roles: ["normalizer:application/pdf"],
    },
    { plugin_id: "connector.rss", version: "1.0.0", roles: ["connector:rss"] },
  ];
  const minutesAgo = (n: number) =>
    new Date(Date.now() - n * MINUTE).toISOString();
  const connector = (
    id: string,
    namespace: string,
    extra: Record<string, unknown> = {},
  ) => ({
    connector_id: id,
    corpus_id: "demo",
    source_namespace: namespace,
    kind: "rss",
    config: { url: `https://news.example.org/${id}.xml` },
    schedule: { interval_seconds: 300 },
    health_policy: {
      silent_after_seconds: 86400,
      credential_warning_seconds: 1209600,
    },
    enabled: true,
    created_at: minutesAgo(3000),
    health: {
      state: "active",
      evaluated_at: minutesAgo(1),
      last_success_at: minutesAgo(2),
      last_item_at: minutesAgo(6),
      usage: {
        day: new Date().toISOString().slice(0, 10),
        items_read: 128,
        previous_day_items_read: 402,
      },
      ...extra,
    },
  });
  const connectors = [
    connector("con_world", "Dépêches exemple"),
    connector("con_tech", "Revue technique", {
      last_success_at: minutesAgo(4),
      usage: {
        day: new Date().toISOString().slice(0, 10),
        items_read: 37,
        previous_day_items_read: 90,
      },
    }),
    connector("con_weather", "Météo locale", {
      last_success_at: minutesAgo(50),
      last_error: { code: "source_unavailable", at: minutesAgo(3) },
      usage: {
        day: new Date().toISOString().slice(0, 10),
        items_read: 12,
        previous_day_items_read: 33,
      },
    }),
  ];

  const hour = Math.floor(Date.now() / 3600000) * 3600000;
  const snapshot = () => ({
    live: true,
    documents: [],
    stats: {
      per_minute: 6,
      per_minute_window: "1h",
      searchable_p95_ms: 900,
      searchable_day_p95_ms: 860,
      waiting: 7,
      stuck: 0,
      waiting_by_step: {
        received: {
          count: 1,
          oldest_since: new Date(Date.now() - 4000).toISOString(),
        },
        cut: { count: 0 },
        searchable: { count: 0 },
        vectors: {
          count: 6,
          oldest_since: new Date(Date.now() - 3 * MINUTE).toISOString(),
        },
        alerts: { count: 0 },
      },
      errors: 0,
      hours: Array.from({ length: 24 }, (_, i) => ({
        start: new Date(hour - (23 - i) * 3600000).toISOString(),
        count: 300 + Math.round(120 * Math.sin(i / 3)),
        errors: 0,
      })),
      hours_of: "searchable",
    },
  });

  // The documents stored per day over three weeks, two of them empty, and
  // Records without a current Version counted in the total only.
  const stored = [3, 5, 4, 0, 6, 8, 7, 5, 9, 0, 4, 6, 7, 8, 5, 6, 9, 11, 10, 8, 12];
  const history = (url: URL) => {
    const today = Date.parse(
      new Date().toLocaleDateString("en-CA", {
        timeZone: url.searchParams.get("tz") || "UTC",
      }),
    );
    const days = stored.map((count, i) => ({
      day: new Date(today - (stored.length - 1 - i) * 86400000)
        .toISOString()
        .slice(0, 10),
      count,
    }));
    return {
      time_zone: url.searchParams.get("tz"),
      total: stored.reduce((n, c) => n + c, 0) + 3,
      undated: 3,
      first_day: days[0].day,
      today: days.at(-1)?.day,
      truncated: false,
      days,
    };
  };

  await page.route(/\/demo\/admin(\/.*)?$/, async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    reads.push(path + url.search);
    if (path === "/demo/admin") return json(route, snapshot());
    if (path === "/demo/admin/stream")
      return route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: `retry: 600000\nevent: status\ndata: {"live":true}\n\n`,
      });
    if (path === "/demo/admin/plugins") return json(route, { items: active });
    if (path === "/demo/admin/history") return json(route, history(url));
    const kind = path.match(/^\/demo\/admin\/stats\/(\w[\w-]*)$/)?.[1];
    const window = (url.searchParams.get("window") || "1h") as Window;
    const now = Date.now();
    const list = (items: object[]) => {
      const bounds = series(window, now, () => null);
      return {
        window,
        resolution_seconds: RESOLUTION[window],
        from: bounds.from,
        to: bounds.to,
        items,
      };
    };
    if (kind === "steps")
      return json(
        route,
        list(
          Object.entries(steps).map(([step, shape]) => {
            const { summary, points } = series(window, now, shape);
            return { step, summary, points };
          }),
        ),
      );
    if (kind === "plugins")
      return json(
        route,
        list(
          plugins
            .map((p) => ({ p, s: series(window, now, p.shape) }))
            .filter(({ s }) => s.summary.count > 0)
            .map(({ p, s }) => ({
              plugin_id: p.id,
              plugin_version: p.version,
              operation: p.operation,
              summary: s.summary,
              points: s.points,
            })),
        ),
      );
    return json(route, { message: "Page introuvable." }, 404);
  });
  await page.route(/\/v0\/connectors(\?.*)?$/, (route) =>
    json(route, { items: connectors }),
  );

  return {
    reads,
    stop: (plugin) => {
      stopped.add(plugin);
      restarted.delete(plugin);
    },
    restart: (plugin) => {
      restarted.add(plugin);
    },
  };
}
