// Measures the demo against perf/budgets.json (THE-1041): each page's load
// (LCP, CLS, JavaScript) under a 4G-class link, the slowest interactions
// (INP), the facade's endpoints (p50, p95 and what they waited on in the
// core) and, on request, how long an alert takes to catch a new text.
//
//   QUIVR_DEMO_URL=https://… QUIVR_DEMO_PASSWORD=… npm run perf --prefix quivr-search
//
// QUIVR_DEMO_URL     the demo to measure (default http://127.0.0.1:5183)
// QUIVR_DEMO_PASSWORD  its shared password, if it has one
// PERF_RUNS          loads per page and interaction sessions (default 3)
// PERF_ALERT_LAG     1 adds a text and an alert to measure the catch delay:
//                    only on a demo you may write to (make demo-perf sets it)
// PERF_REPORT        a file for the JSON report
// Exits 1 when a budget is exceeded. Endpoint budgets hold for the seeded
// local stack only: a remote deployment's endpoints are reported, not checked.
import { readFileSync, writeFileSync } from "node:fs";
import { chromium } from "@playwright/test";

const base = (process.env.QUIVR_DEMO_URL || "http://127.0.0.1:5183").replace(/\/$/, "");
const password = process.env.QUIVR_DEMO_PASSWORD || "";
const runs = Number(process.env.PERF_RUNS || 3);
const budgets = JSON.parse(readFileSync(new URL("budgets.json", import.meta.url)));
const local = ["127.0.0.1", "localhost", "[::1]"].includes(new URL(base).hostname);
const { profile } = budgets;

// Each tab but the Fil has its own chunk (vite build); a page's JavaScript is
// the shared bundle and its own chunk, not the others fetched when idle.
const PAGES = [
  { name: "Fil", path: "/", ready: ".feed-rows .row" },
  { name: "Recherche", path: "/?q=port", ready: ".feed-rows .row" },
  { name: "Alertes", path: "/?view=alerts", ready: ".alerts-table tbody tr, .alerts-empty, .empty-state", chunk: "AlertsView" },
  { name: "Sources", path: "/?view=sources", ready: ".source-cards .source-card", chunk: "ConnectorsView" },
  { name: "Admin", path: "/?view=admin", ready: ".flow-rows .flow-row, .admin-board .notice", chunk: "AdminView" },
];
const TAB_CHUNK = /\/assets\/(AlertsView|ConnectorsView|AdminView|AddText)-[^/]+\.js$/;

const median = (values) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)];
const percentile = (values, p) =>
  [...values].sort((a, b) => a - b)[Math.min(values.length - 1, Math.floor(values.length * p))];

// Runs in the page before its scripts: the largest paint, layout shifts that
// no input caused, and each interaction's longest event.
function observe() {
  const perf = (window.__perf = { lcp: 0, cls: 0, label: "", events: [] });
  new PerformanceObserver((list) => {
    for (const entry of list.getEntries()) perf.lcp = entry.startTime;
  }).observe({ type: "largest-contentful-paint", buffered: true });
  new PerformanceObserver((list) => {
    for (const entry of list.getEntries()) if (!entry.hadRecentInput) perf.cls += entry.value;
  }).observe({ type: "layout-shift", buffered: true });
  new PerformanceObserver((list) => {
    for (const entry of list.getEntries())
      if (entry.interactionId) perf.events.push({ label: perf.label, ms: entry.duration });
  }).observe({ type: "event", durationThreshold: 16, buffered: true });
}

async function context(browser) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  if (password) {
    const login = await ctx.request.post(base + "/demo/login", {
      data: { password },
      headers: { Origin: base },
    });
    if (!login.ok()) throw new Error(`Login refused (HTTP ${login.status()})`);
  }
  const page = await ctx.newPage();
  const cdp = await ctx.newCDPSession(page);
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: profile.cpu_slowdown });
  await page.addInitScript(observe);
  return { ctx, page, cdp };
}

async function load(browser, target) {
  const { ctx, page, cdp } = await context(browser);
  await cdp.send("Network.enable");
  await cdp.send("Network.setCacheDisabled", { cacheDisabled: true });
  await cdp.send("Network.emulateNetworkConditions", {
    offline: false,
    latency: profile.latency_ms,
    downloadThroughput: (profile.download_kbps * 1024) / 8,
    uploadThroughput: (profile.upload_kbps * 1024) / 8,
  });
  await page.goto(base + target.path, { waitUntil: "load" });
  await page.waitForSelector(target.ready, { timeout: 60000 });
  // LCP and layout shifts settle once the data has painted.
  await page.waitForTimeout(1500);
  const scripts = await page.evaluate(() =>
    performance
      .getEntriesByType("resource")
      .filter((r) => r.name.endsWith(".js"))
      .map((r) => ({ name: r.name, size: r.transferSize })),
  );
  const kb = (list) => list.reduce((sum, r) => sum + r.size, 0) / 1024;
  const own = (r) => {
    const tab = TAB_CHUNK.exec(r.name)?.[1];
    return !tab || tab === target.chunk;
  };
  const sample = {
    ...(await page.evaluate(() => ({ lcp: window.__perf.lcp, cls: window.__perf.cls }))),
    pageJS: kb(scripts.filter(own)),
    allJS: kb(scripts),
  };
  await ctx.close();
  return sample;
}

// One session over the page: an article, the tabs, a search typed and cleared.
async function interactions(browser) {
  const { ctx, page } = await context(browser);
  await page.goto(base + "/");
  await page.waitForSelector(".feed-rows .row");
  await page.waitForTimeout(1000);
  const steps = [
    ["open an article", () => page.locator(".feed-rows .row").nth(2).click()],
    ["next article (↓)", () => page.keyboard.press("ArrowDown")],
    ["close it (Esc)", () => page.keyboard.press("Escape")],
    ["Alertes tab", () => page.locator('.rail-tab[data-section="alerts"]').click()],
    ["Sources tab", () => page.locator('.rail-tab[data-section="sources"]').click()],
    ["Admin tab", () => page.locator('.rail-tab[data-section="admin"]').click()],
    ["back to Fil", () => page.locator('.rail-tab[data-section="feed"]').click()],
    ["type a search", async () => {
      await page.locator('input[type="search"]').click();
      await page.keyboard.type("port", { delay: 80 });
    }],
    ["clear the search", () => page.locator(".bar-clear").click()],
  ];
  for (const [label, act] of steps) {
    await page.evaluate((value) => (window.__perf.label = value), label);
    await act();
    await page.waitForTimeout(1200);
  }
  const events = await page.evaluate(() => window.__perf.events);
  await ctx.close();
  const worst = {};
  for (const { label, ms } of events) worst[label] = Math.max(worst[label] || 0, ms);
  return worst;
}

async function api(path, init = {}) {
  return fetch(base + path, {
    ...init,
    headers: {
      Origin: base,
      "Content-Type": "application/json",
      "Accept-Encoding": "br, gzip",
      cookie: api.cookie || "",
      ...init.headers,
    },
  });
}

async function endpoints() {
  if (password) {
    const login = await api("/demo/login", { method: "POST", body: JSON.stringify({ password }) });
    api.cookie = (login.headers.get("set-cookie") || "").split(";")[0];
  }
  const json = async (path) => (await api(path)).json();
  const { corpus_id: corpus } = await json("/demo/session");
  const alerts = (await json("/demo/alerts")).items || [];
  const feed = (await json("/demo/feed")).items || [];
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  const day = (offset) => new Date(today.getTime() + offset * 864e5).toISOString();
  const bounds = Array.from({ length: 15 }, (_, i) => day(1 - i)).join(",");
  const post = (body) => ({ method: "POST", body: JSON.stringify(body) });
  const search = (mode) => post({ query: "port grève", mode, profile: "default", limit: 50, corpus_ids: [corpus] });
  const calls = {
    "GET /demo/feed": ["/demo/feed"],
    "GET /demo/feed/days": ["/demo/feed/days?bounds=" + encodeURIComponent(bounds)],
    "GET /demo/feed/page": [`/demo/feed/page?after=${encodeURIComponent(day(-1))}&before=${encodeURIComponent(day(1))}`],
    "GET /demo/alerts": ["/demo/alerts"],
    ...(alerts[0] && { "GET /demo/alerts/:id": ["/demo/alerts/" + alerts[0].alert_id] }),
    "POST /demo/alerts/preview": ["/demo/alerts/preview", post({ expression: { kind: "keywords", match: { term: "port" } } })],
    "GET /demo/admin": ["/demo/admin"],
    "GET /v0/connectors": ["/v0/connectors"],
    "POST /v0/search lexical": ["/v0/search", search("lexical")],
    "POST /v0/search hybrid": ["/v0/search", search("hybrid")],
    ...(feed[0] && { "GET /v0/records/:id/versions/:v": [`/v0/records/${feed[0].record_id}/versions/${feed[0].version_id}`] }),
  };
  const out = {};
  for (const [name, [path, init]] of Object.entries(calls)) {
    const times = [];
    const core = [];
    let status = 0;
    let bytes = 0;
    let wire = 0;
    for (let i = 0; i < 20; i++) {
      const started = performance.now();
      const response = await api(path, init);
      bytes = (await response.arrayBuffer()).byteLength;
      // Sent compressed when the facade compresses, else as read.
      wire = Number(response.headers.get("content-length")) || bytes;
      times.push(performance.now() - started);
      status = response.status;
      const timing = /core;dur=([\d.]+);desc="(\d+)/.exec(response.headers.get("server-timing") || "");
      if (timing) core.push([Number(timing[1]), Number(timing[2])]);
    }
    out[name] = {
      status,
      p50_ms: Math.round(percentile(times, 0.5)),
      p95_ms: Math.round(percentile(times, 0.95)),
      kb: +(bytes / 1024).toFixed(1),
      wire_kb: +(wire / 1024).toFixed(1),
      core_ms: core.length ? Math.round(median(core.map((c) => c[0]))) : null,
      core_calls: core.length ? median(core.map((c) => c[1])) : null,
    };
  }
  return out;
}

// How long an alert takes to catch a text added now, with an alert kept for it.
async function alertLag() {
  const term = "sondedelai";
  let alert = ((await (await api("/demo/alerts")).json()).items || []).find((a) => a.name === "Sonde de délai");
  if (!alert)
    alert = await (await api("/demo/alerts", {
      method: "POST",
      body: JSON.stringify({
        idempotency_key: "perf-alert-lag-probe",
        name: "Sonde de délai",
        expression: { kind: "keywords", match: { term } },
      }),
    })).json();
  const { corpus_id: corpus } = await (await api("/demo/session")).json();
  const lags = [];
  for (let i = 0; i < 5; i++) {
    const key = `perf-lag-${Date.now()}-${i}`;
    const started = Date.now();
    const added = await api("/v0/records", {
      method: "POST",
      body: JSON.stringify({
        idempotency_key: key,
        source: { corpus_id: corpus, namespace: "web-demo", record_key: key },
        content: { kind: "text", text: `Mesure du délai d’alerte ${term} ${key}.` },
      }),
    });
    if (!added.ok) throw new Error(`The probe text was refused (HTTP ${added.status})`);
    for (;;) {
      if (Date.now() - started > 300000) throw new Error("No catch within 5 minutes");
      const detail = await (await api("/demo/alerts/" + alert.alert_id)).json();
      if ((detail.matches || []).some((m) => `${m.title} ${m.excerpt}`.includes(key))) break;
      await new Promise((resolve) => setTimeout(resolve, 250));
    }
    lags.push(Date.now() - started);
  }
  return { p50_ms: median(lags), max_ms: Math.max(...lags), samples: lags };
}

const browser = await chromium.launch();
const report = { url: base, at: new Date().toISOString(), profile, pages: {}, interactions: {}, endpoints: {} };
for (const target of PAGES) {
  const samples = [];
  for (let i = 0; i < runs; i++) samples.push(await load(browser, target));
  report.pages[target.name] = {
    lcp_ms: Math.round(median(samples.map((s) => s.lcp))),
    cls: +Math.max(...samples.map((s) => s.cls)).toFixed(3),
    js_kb: +median(samples.map((s) => s.pageJS)).toFixed(1),
    all_js_kb: +median(samples.map((s) => s.allJS)).toFixed(1),
  };
}
const sessions = [];
for (let i = 0; i < runs; i++) sessions.push(await interactions(browser));
await browser.close();
for (const label of Object.keys(sessions[0]))
  report.interactions[label] = Math.round(median(sessions.map((s) => s[label] || 0)));
report.inp_ms = Math.max(0, ...Object.values(report.interactions));
report.endpoints = await endpoints();
if (process.env.PERF_ALERT_LAG === "1") report.alert_lag = await alertLag();

const breaches = [];
for (const [name, page] of Object.entries(report.pages)) {
  if (page.lcp_ms > budgets.pages.lcp_ms) breaches.push(`${name}: LCP ${page.lcp_ms} ms > ${budgets.pages.lcp_ms}`);
  if (page.cls > budgets.pages.cls) breaches.push(`${name}: CLS ${page.cls} > ${budgets.pages.cls}`);
  if (page.js_kb > budgets.pages.js_kb)
    breaches.push(`${name}: JavaScript ${page.js_kb} KB > ${budgets.pages.js_kb}`);
}
if (report.inp_ms > budgets.inp_ms) breaches.push(`INP ${report.inp_ms} ms > ${budgets.inp_ms}`);
if (local)
  for (const [name, row] of Object.entries(report.endpoints)) {
    const budget = budgets.endpoints[name];
    if (budget && row.p95_ms > budget) breaches.push(`${name}: p95 ${row.p95_ms} ms > ${budget}`);
  }
report.breaches = breaches;

console.log(`\n${base} — ${profile.about}\n`);
console.table(report.pages);
console.table(report.interactions);
console.log(`INP (slowest interaction, median of ${runs} sessions): ${report.inp_ms} ms`);
console.table(report.endpoints);
if (report.alert_lag) console.log("Alert catch delay:", report.alert_lag);
if (process.env.PERF_REPORT) writeFileSync(process.env.PERF_REPORT, JSON.stringify(report, null, 2));
if (breaches.length) {
  console.error("\nBudgets exceeded (perf/budgets.json):\n- " + breaches.join("\n- "));
  process.exit(1);
}
console.log("\nEvery budget holds.");
