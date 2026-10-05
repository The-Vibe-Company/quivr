// Fails the build when the bundle outgrows perf/budgets.json (THE-1041): the
// JavaScript and CSS a first visit loads, and each chunk loaded later.
import { readFileSync, readdirSync } from "node:fs";
import { gzipSync } from "node:zlib";

const dist = new URL("../dist/", import.meta.url);
const { bundle } = JSON.parse(readFileSync(new URL("budgets.json", import.meta.url)));
const html = readFileSync(new URL("index.html", dist), "utf8");
const kb = (name) => gzipSync(readFileSync(new URL("." + name, dist)), { level: 9 }).length / 1024;
const linked = (pattern) => [...html.matchAll(pattern)].map((m) => m[1]);
const initialJS = linked(/(?:src|href)="(\/assets\/[^"]+\.js)"/g);
const initialCSS = linked(/href="(\/assets\/[^"]+\.css)"/g);
const later = readdirSync(new URL("assets/", dist))
  .map((name) => "/assets/" + name)
  .filter((name) => name.endsWith(".js") && !initialJS.includes(name));
const sum = (names) => names.reduce((total, name) => total + kb(name), 0);
const checks = [
  ["JavaScript of a first visit", sum(initialJS), bundle.initial_js_kb],
  ["CSS of a first visit", sum(initialCSS), bundle.initial_css_kb],
  ...later.map((name) => [`chunk ${name}`, kb(name), bundle.chunk_js_kb]),
];
let over = 0;
for (const [what, size, budget] of checks) {
  const ok = size <= budget;
  if (!ok) over++;
  console.log(`${ok ? "ok  " : "OVER"} ${what}: ${size.toFixed(1)} KB gzip (budget ${budget} KB)`);
}
if (over) {
  console.error(`${over} bundle budget(s) exceeded: see quivr-search/perf/budgets.json.`);
  process.exit(1);
}
