import { test, expect, type Page, type Route } from "@playwright/test";
import { fakeEngine, type Engine } from "./fake-engine";

// The Admin tab's usage section (THE-798) against the engine's rollups as
// the facade relays them (/demo/admin/stats/*), answered here from synthetic
// buckets: the real bundle, no core. usage-stack.spec.ts covers the same
// section end to end on the local demo.

type Window = "24h" | "7d";
// One clock for the fake engine and the assertions, so a bucket boundary
// passing during a test never shifts one against the other.
const NOW = Date.now();
const RESOLUTION: Record<Window, number> = { "24h": 900, "7d": 7200 };
const SPAN: Record<Window, number> = { "24h": 24 * 3600, "7d": 7 * 24 * 3600 };

// A deterministic amount per bucket, busier in the afternoon.
const wave = (i: number, scale: number, phase = 0) =>
  Math.max(
    0,
    Math.round(scale * (1 + Math.sin((i + phase) / 5)) - scale * 0.4),
  );

function buckets(window: Window, scale: number, phase = 0, every = 1) {
  const step = RESOLUTION[window] * 1000;
  const now = NOW;
  const from = Math.floor((now - SPAN[window] * 1000) / step) * step;
  const points: { start: string; count: number }[] = [];
  for (let t = from, i = 0; t < now; t += step, i++) {
    const count = i % every === 0 ? wave(i, scale, phase) : 0;
    if (count > 0) points.push({ start: new Date(t).toISOString(), count });
  }
  return {
    from: new Date(from).toISOString(),
    to: new Date(now).toISOString(),
    points,
  };
}
const sum = (points: { count: number }[]) =>
  points.reduce((n, p) => n + p.count, 0);

const SOURCES = [
  ["Dépêches exemple", 9],
  ["Revue technique", 4],
  ["Météo locale", 3],
  ["web-demo", 1],
  ["Agence régionale", 1],
  ["Blog municipal", 0.6],
  ["Lettre hebdomadaire", 0.4],
] as const;
// Two more sources than the read lists, so the legend's "Autres" covers them.
const UNLISTED = { sources: 2, documents: 7 };

function received(window: Window, limit: number) {
  const all = SOURCES.map(([namespace, scale], i) => {
    const b = buckets(window, scale * (window === "7d" ? 8 : 1), i * 3);
    return {
      source_namespace: namespace,
      count: sum(b.points),
      points: b.points,
      from: b.from,
      to: b.to,
    };
  });
  const items = all.slice(0, limit);
  return {
    window,
    resolution_seconds: RESOLUTION[window],
    from: all[0].from,
    to: all[0].to,
    total: all.reduce((n, i) => n + i.count, 0) + UNLISTED.documents,
    sources: all.length + UNLISTED.sources,
    items: items.map(({ source_namespace, count, points }) => ({
      source_namespace,
      count,
      points,
    })),
  };
}

function searches(window: Window) {
  const series = (
    mode: string,
    scale: number,
    p50: number,
    p95: number,
    phase: number,
  ) => {
    const b = buckets(window, scale, phase);
    return {
      mode,
      profile: "default",
      results: sum(b.points) * 6,
      over_objective: mode === "hybrid" ? 3 : 0,
      summary: {
        count: sum(b.points),
        errors: mode === "hybrid" ? 2 : 0,
        p50_ms: p50,
        p95_ms: p95,
        mean_ms: p50 * 1.2,
      },
      points: b.points.map((p, i) => ({
        ...p,
        errors: 0,
        p50_ms: p50,
        p95_ms: Math.round(p95 * (1 + 0.25 * Math.sin(i / 6))),
      })),
      window: b,
    };
  };
  const items = [
    series("hybrid", 5, 180, 420, 0),
    series("lexical", 3, 35, 90, 4),
  ];
  const b = items[0].window;
  return {
    window,
    resolution_seconds: RESOLUTION[window],
    from: b.from,
    to: b.to,
    items: items.map(({ window: _, ...rest }) => rest),
  };
}

function matches(window: Window) {
  const b = buckets(window, 1.5, 2, 3);
  return {
    window,
    resolution_seconds: RESOLUTION[window],
    from: b.from,
    to: b.to,
    total: sum(b.points),
    items: [{ evaluator: "alerts", count: sum(b.points), points: b.points }],
  };
}

const QUERIES = [
  "orages et grêle",
  "grève des transports",
  "prix de l’énergie",
  "conseil municipal",
  "rentrée scolaire",
];
function topQueries(window: Window, recording: boolean) {
  if (!recording)
    return { window, resolution_seconds: 3600, recording: false, items: [] };
  const hourly = (i: number) => {
    const now = NOW;
    const hours = SPAN[window] / 3600;
    const points = [];
    for (let h = hours - 1; h >= 0; h--) {
      // Each query has its own shape over the window: rising, fading or bursty.
      const age = h / hours;
      const shape = [
        1 - age,
        age,
        Math.sin(age * 9) ** 2,
        1 - age,
        (h % 11) / 11,
      ][i];
      const count = (h * 7 + i) % 3 === 0 ? 0 : Math.round((6 - i) * shape);
      if (count)
        points.push({
          start: new Date(
            Math.floor(now / 3600000) * 3600000 - h * 3600000,
          ).toISOString(),
          count,
        });
    }
    return points;
  };
  return {
    window,
    resolution_seconds: 3600,
    recording: true,
    items: QUERIES.map((query, i) => ({ query, points: hourly(i) }))
      .map((q) => ({ ...q, count: sum(q.points) }))
      .sort((a, b) => b.count - a.count),
  };
}

interface Options {
  recording?: boolean;
  /** Kinds answered with an engine failure, until healed. */
  failing?: Set<string>;
  empty?: Set<string>;
  /** 7-day reads wait for this before they are answered. */
  week?: Promise<void>;
}

let engine: Engine;
const asked: string[] = [];
async function usageEngine(page: Page, options: Options = {}) {
  engine = await fakeEngine(page);
  asked.length = 0;
  const json = (route: Route, data: unknown, status = 200) =>
    route.fulfill({ status, contentType: "application/json", json: data });
  await page.route("**/demo/admin/stream", (route) => route.abort());
  await page.route("**/demo/admin", (route) =>
    json(route, {
      documents: [],
      live: true,
      stats: {
        per_minute: 0,
        per_minute_window: "1h",
        searchable_p95_ms: null,
        searchable_day_p95_ms: null,
        waiting: 0,
        stuck: 0,
        waiting_by_step: {},
        errors: 0,
        hours: [],
        hours_of: "received",
      },
    }),
  );
  await page.route("**/demo/admin/stats/**", async (route) => {
    const url = new URL(route.request().url());
    const kind = url.pathname.split("/").pop()!;
    const window = (url.searchParams.get("window") || "1h") as Window;
    const limit = Number(url.searchParams.get("limit") || 20);
    asked.push(`${kind}?${url.searchParams}`);
    if (window === "7d") await options.week;
    if (options.failing?.has(kind))
      return json(route, { message: "Le moteur ne répond pas." }, 503);
    const data =
      kind === "received"
        ? received(window, limit)
        : kind === "searches"
          ? searches(window)
          : kind === "matches"
            ? matches(window)
            : kind === "top-queries"
              ? topQueries(window, options.recording ?? true)
              : { window, resolution_seconds: 60, from: "", to: "", items: [] };
    if (options.empty?.has(kind))
      return json(route, { ...data, items: [], total: 0, sources: 0 });
    return json(route, data);
  });
}
test.afterEach(async () => {
  await engine.close();
});

const usage = (page: Page) => page.getByRole("region", { name: "Utilisation" });
const block = (page: Page, name: string) =>
  usage(page).getByRole("region", { name });

test("l’utilisation montre les documents par source, les recherches, les alertes et les requêtes, sur 24 h puis 7 jours", async ({
  page,
}, info) => {
  let answerWeek = () => {};
  await usageEngine(page, {
    week: new Promise((resolve) => (answerWeek = resolve)),
  });
  await page.setViewportSize({ width: 1440, height: 1600 });
  await page.goto("/?view=admin");
  await page.getByRole("tab", { name: "Utilisation" }).click();

  const documents = block(page, "Documents reçus");
  const day = received("24h", 20);
  await expect(documents).toContainText(
    `${day.total.toLocaleString("fr-FR")} documents`,
  );
  await expect(documents).toContainText(`de ${day.sources} sources en 24 h`);
  const legend = documents
    .getByRole("list", { name: "Sources" })
    .getByRole("listitem");
  // Five sources stacked, the rest folded together, largest first; the hand-added namespace is named.
  await expect(legend).toHaveCount(6);
  await expect(legend.first()).toContainText("Dépêches exemple");
  await expect(legend.nth(3)).toContainText("À la main");
  await expect(legend.last()).toContainText("4 autres sources");

  // Pointing at a column shows its range and its sources.
  const chart = documents.getByRole("img", {
    name: /^Documents reçus par source, 24 h : /,
  });
  // The columns hold every bucket the read listed: all but the sources past its limit.
  await expect(chart).toHaveAccessibleName(
    new RegExp(
      `: ${(day.total - UNLISTED.documents).toLocaleString("fr-FR").replace(/\s/g, "\\s")} documents,`,
    ),
  );
  const columns = chart.locator(".usage-slot");
  await columns.nth(20).hover();
  await expect(chart.locator(".usage-tip")).toContainText(
    /\d{2}:\d{2}–\d{2}:\d{2}/,
  );
  await expect(chart.locator(".usage-tip")).toContainText(/\d+ documents?$/);

  const searchBlock = block(page, "Recherches");
  await expect(searchBlock).toContainText(
    "2 échecs · 3 hors objectif de temps",
  );
  const modes = searchBlock.getByRole("table", {
    name: /Recherches et temps de réponse par mode/,
  });
  await expect(modes.getByRole("row", { name: /Idées proches/ })).toContainText(
    "420 ms",
  );
  await expect(modes.getByRole("row", { name: /Mots exacts/ })).toContainText(
    "35 ms",
  );

  await expect(block(page, "Alertes déclenchées")).toContainText(
    `${matches("24h").total} articles attrapés`,
  );
  const queries = block(page, "Requêtes fréquentes").getByRole("listitem");
  await expect(queries).toHaveCount(QUERIES.length);
  await expect(queries.first()).toContainText(
    topQueries("24h", true).items[0].query,
  );
  await usage(page).screenshot({
    path: info.outputPath("usage-24h-light-tooltip.png"),
  });
  await page.mouse.move(0, 0);
  await usage(page).screenshot({
    path: info.outputPath("usage-24h-light.png"),
  });

  // The keyboard walks the columns too.
  await chart.focus();
  await page.keyboard.press("End");
  await expect(documents.locator("[aria-live=polite]")).toContainText(
    /documents?$/,
  );
  await expect(chart.locator(".usage-slot").last()).toHaveAttribute(
    "data-point",
    "true",
  );

  // 7 days: the day's numbers stay, dimmed and busy, until the week arrives;
  // then every block reads the week, in quarter-day columns.
  await usage(page).getByRole("button", { name: "7 j" }).click();
  await expect(
    usage(page).getByRole("button", { name: "7 j" }),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(documents).toHaveAttribute("aria-busy", "true");
  await expect(documents).toContainText(`de ${day.sources} sources en 24 h`);
  answerWeek();
  await expect(documents).not.toHaveAttribute("aria-busy");
  const week = received("7d", 20);
  await expect(documents).toContainText(
    `de ${week.sources} sources en 7 jours`,
  );
  await expect(
    documents.getByRole("img", {
      name: /^Documents reçus par source, 7 jours : /,
    }),
  ).toHaveAccessibleName(
    new RegExp(
      `: ${(week.total - UNLISTED.documents).toLocaleString("fr-FR").replace(/\s/g, "\\s")} documents,`,
    ),
  );
  for (const kind of ["received", "searches", "matches", "top-queries"])
    expect(
      asked.some((a) => a.startsWith(kind) && a.includes("window=7d")),
      `${kind} read over 7 days`,
    ).toBe(true);
  expect(asked.find((a) => a.startsWith("received"))).toContain("limit=20");

  await page.emulateMedia({ colorScheme: "dark" });
  await usage(page).screenshot({ path: info.outputPath("usage-7d-dark.png") });
});

test("sans enregistrement du texte, les requêtes fréquentes disent comment l’activer", async ({
  page,
}, info) => {
  await usageEngine(page, { recording: false });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/?view=admin");
  await page.getByRole("tab", { name: "Utilisation" }).click();
  const queries = block(page, "Requêtes fréquentes");
  await expect(queries.getByRole("status")).toContainText(
    "Le texte des recherches n’est pas enregistré",
  );
  await expect(queries.locator("code")).toHaveText(
    "observability.record_query_text",
  );
  await expect(queries.getByRole("listitem")).toHaveCount(0);
  // The other blocks are unaffected.
  await expect(
    block(page, "Recherches").getByRole("table", { name: /temps de réponse/ }),
  ).toBeVisible();
  await queries.screenshot({
    path: info.outputPath("usage-recording-off.png"),
  });
});

test("un bloc vide ou en échec ne cache pas les autres, et se relit sur Réessayer", async ({
  page,
}, info) => {
  const failing = new Set(["matches"]);
  await usageEngine(page, { failing, empty: new Set(["received"]) });
  await page.setViewportSize({ width: 390, height: 900 });
  await page.goto("/?view=admin");
  await page.getByRole("tab", { name: "Utilisation" }).click();
  await expect(block(page, "Documents reçus")).toContainText(
    "0 document en 24 h",
  );
  await expect(block(page, "Documents reçus").getByRole("img")).toHaveCount(0);
  const alerts = block(page, "Alertes déclenchées");
  await expect(alerts.getByRole("alert")).toContainText(
    "Le moteur ne répond pas.",
  );
  await expect(
    block(page, "Recherches").getByRole("table", { name: /temps de réponse/ }),
  ).toBeVisible();
  const wide = await page.evaluate(() =>
    [...document.querySelectorAll(".usage-grid *")]
      .filter(
        (e) =>
          !e.closest(".visually-hidden") &&
          e.getBoundingClientRect().right > innerWidth + 1,
      )
      .map((e) => `${e.tagName}.${e.className}`),
  );
  expect(wide, "nothing in the section wider than the phone").toEqual([]);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await usage(page).screenshot({ path: info.outputPath("usage-phone.png") });

  failing.clear();
  await alerts.getByRole("button", { name: "Réessayer" }).click();
  await expect(alerts).toContainText("articles attrapés");
});
