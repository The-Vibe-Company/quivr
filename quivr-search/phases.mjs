// Where a search spends its time: the phases the engine reports in the
// Server-Timing header of each /v0/search answer, kept for the last searches
// the facade relayed. Read-only numbers for the Admin tab; nothing is stored.

const WINDOW = 500;

/** `routing;dur=1.5, encode;dur=4` as { routing: 1.5, encode: 4 }. */
export function parseServerTiming(header) {
  const phases = {};
  for (const metric of String(header || "").split(",")) {
    const [name, ...params] = metric.trim().split(";");
    const dur = params
      .map((param) => param.trim().split("="))
      .find(([key]) => key === "dur");
    const ms = dur ? Number(dur[1]) : NaN;
    if (name && Number.isFinite(ms) && ms >= 0) phases[name] = ms;
  }
  return phases;
}

/** Nearest-rank percentile of a sorted list. */
const percentile = (sorted, p) => sorted[Math.ceil(p * sorted.length) - 1];

/** The last `size` searches, each phase's p50 and p95 over them. */
export function createPhaseWindow(size = WINDOW) {
  const searches = [];
  return {
    record(header) {
      const phases = parseServerTiming(header);
      if (!Object.keys(phases).length) return;
      searches.push(phases);
      if (searches.length > size) searches.shift();
    },
    summary() {
      const names = [...new Set(searches.flatMap((s) => Object.keys(s)))];
      return {
        searches: searches.length,
        phases: names.map((name) => {
          const sorted = searches
            .map((s) => s[name])
            .filter((ms) => ms !== undefined)
            .sort((a, b) => a - b);
          return {
            phase: name,
            p50_ms: percentile(sorted, 0.5),
            p95_ms: percentile(sorted, 0.95),
          };
        }),
      };
    },
  };
}
