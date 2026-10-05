// The rules perf/measure.mjs applies to what it measured (perf/budgets.json).

const ok = (status) => status >= 200 && status <= 299;

/** An endpoint's status over its samples: the first refusal, else the last answer. */
export function worstStatus(statuses) {
  return statuses.find((status) => !ok(status)) ?? statuses.at(-1);
}

/**
 * Every budget the report breaks, in words. Endpoint times count only on the
 * seeded local stack (`local`); an endpoint answering an error fails anywhere.
 */
export function breaches(report, budgets, { local }) {
  const out = [];
  for (const [name, page] of Object.entries(report.pages)) {
    if (page.lcp_ms > budgets.pages.lcp_ms) out.push(`${name}: LCP ${page.lcp_ms} ms > ${budgets.pages.lcp_ms}`);
    if (page.cls > budgets.pages.cls) out.push(`${name}: CLS ${page.cls} > ${budgets.pages.cls}`);
    if (page.js_kb > budgets.pages.js_kb) out.push(`${name}: JavaScript ${page.js_kb} KB > ${budgets.pages.js_kb}`);
  }
  if (report.inp_ms > budgets.inp_ms) out.push(`INP ${report.inp_ms} ms > ${budgets.inp_ms}`);
  for (const [name, row] of Object.entries(report.endpoints))
    if (!ok(row.status)) out.push(`${name}: HTTP ${row.status}`);
  if (local)
    for (const [name, row] of Object.entries(report.endpoints)) {
      const budget = budgets.endpoints[name];
      if (budget && row.p95_ms > budget) out.push(`${name}: p95 ${row.p95_ms} ms > ${budget}`);
    }
  return out;
}
