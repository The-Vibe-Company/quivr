// Where a search spends its time: the phases the engine reports in the
// Server-Timing header of each /v0/search answer, kept for the last searches
// the facade relayed. Read-only numbers for the Admin tab; nothing is stored.

const WINDOW = 500;

/** Split `input` on unquoted `delimiter`, respecting W3C/RFC 9110 quoted strings and `\"` escapes. */
function splitUnquoted(input, delimiter) {
  const parts = [];
  let start = 0;
  let inQuotes = false;
  let escaped = false;
  for (let i = 0; i < input.length; i++) {
    const ch = input[i];
    if (escaped) {
      escaped = false;
      continue;
    }
    if (inQuotes) {
      if (ch === "\\") escaped = true;
      else if (ch === '"') inQuotes = false;
      continue;
    }
    if (ch === '"') inQuotes = true;
    else if (ch === delimiter) {
      parts.push(input.slice(start, i));
      start = i + 1;
    }
  }
  parts.push(input.slice(start));
  return parts;
}

/** Decode a token or quoted-string parameter value, returning null when malformed. */
function parseParamValue(raw) {
  const value = raw.trim();
  if (!value.startsWith('"')) return value;
  let out = "";
  let escaped = false;
  for (let i = 1; i < value.length; i++) {
    const ch = value[i];
    if (escaped) {
      out += ch;
      escaped = false;
      continue;
    }
    if (ch === "\\") {
      escaped = true;
      continue;
    }
    if (ch === '"') {
      return i === value.length - 1 ? out : null;
    }
    out += ch;
  }
  return null;
}

/** `routing;dur=1.5, encode;dur=4` as a null-prototype map { routing: 1.5, encode: 4 }. */
export function parseServerTiming(header) {
  const phases = Object.create(null);
  for (const metric of splitUnquoted(String(header || ""), ",")) {
    const [rawName, ...params] = splitUnquoted(metric, ";");
    const name = rawName.trim();
    if (!name) continue;
    let durRaw;
    for (const param of params) {
      const eq = param.indexOf("=");
      if (eq === -1) continue;
      if (param.slice(0, eq).trim().toLowerCase() === "dur") {
        durRaw = param.slice(eq + 1);
        break;
      }
    }
    if (durRaw === undefined) continue;
    const decoded = parseParamValue(durRaw);
    if (decoded === null) continue;
    const trimmed = decoded.trim();
    if (!trimmed) continue;
    const ms = Number(trimmed);
    if (Number.isFinite(ms) && ms >= 0) phases[name] = ms;
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
