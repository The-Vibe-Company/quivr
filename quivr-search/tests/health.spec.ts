import { test, expect } from "@playwright/test";
import {
  bottleneck,
  pluginRows,
  stepRows,
  type Waiting,
} from "../src/lib/health";
import type {
  PluginCallStats,
  StatsPoint,
  StepStats,
} from "../src/lib/adminStats";

// The owner of the Admin tab's judgements (THE-797): when a step is slow
// against its own usual time, which step the sentence names, and when a
// plugin is healthy, degraded, down or idle. The sections only render these.

const TO = Date.parse("2026-09-30T12:00:00Z");
const MIN = 60000;
/** One point per minute of the last hour; `at(minutesAgo)` shapes it. */
const minutes = (at: (ago: number) => Partial<StatsPoint> | null) => {
  const points: StatsPoint[] = [];
  for (let ago = 59; ago >= 0; ago--) {
    const p = at(ago);
    if (p)
      points.push({
        start: new Date(TO - (ago + 1) * MIN).toISOString(),
        count: 1,
        errors: 0,
        p50_ms: 0,
        p95_ms: 0,
        ...p,
      });
  }
  return points;
};
const list = <T>(items: T[]) => ({
  window: "1h" as const,
  resolution_seconds: 60,
  from: new Date(TO - 60 * MIN).toISOString(),
  to: new Date(TO).toISOString(),
  items,
});
const step = (name: string, points: StatsPoint[]): StepStats => {
  const count = points.reduce((n, p) => n + p.count, 0);
  return {
    step: name,
    summary: {
      count,
      errors: 0,
      p50_ms: points[0]?.p50_ms,
      p95_ms: points[0]?.p95_ms,
    },
    points,
  };
};
const call = (points: StatsPoint[]): PluginCallStats => ({
  plugin_id: "p",
  plugin_version: "1",
  operation: "segment_and_embed",
  summary: {
    count: points.reduce((n, p) => n + p.count, 0),
    errors: points.reduce((n, p) => n + p.errors, 0),
  },
  points,
});
// The vectors step at `usual` ms for 50 minutes, then `recent` ms.
const vectors = (usual: number, recent: number, recentCount = 1) =>
  step(
    "enriched",
    minutes((ago) =>
      ago < 10 ? { p95_ms: recent, count: recentCount } : { p95_ms: usual },
    ),
  );
const judge = (
  s: StepStats,
  waiting: Partial<Record<string, Waiting>> = {},
) => {
  const rows = stepRows(list([s]), null, waiting);
  return {
    row: rows.find((r) => r.key === "vectors")!,
    verdict: bottleneck(rows, "1h", TO),
  };
};

test("une étape est ralentie à deux fois son p95 habituel, au-dessus de son plancher", () => {
  const cases: [string, StepStats, boolean][] = [
    ["twice its usual", vectors(1500, 3000), true],
    ["just under twice", vectors(1500, 2900), false],
    ["twice, under the 1 s floor", vectors(300, 900), false],
    [
      "too few recent calls",
      step(
        "enriched",
        minutes((ago) =>
          ago < 2 ? { p95_ms: 9000 } : ago < 10 ? null : { p95_ms: 1500 },
        ),
      ),
      false,
    ],
    [
      "too little history",
      step(
        "enriched",
        minutes((ago) => (ago < 15 ? { p95_ms: 9000 } : null)),
      ),
      false,
    ],
  ];
  for (const [name, s, slow] of cases)
    expect(judge(s).row.slow, name).toBe(slow);
});

test("la phrase nomme l’étape ralentie, sinon une file qui dure, sinon la plus longue", () => {
  const slow = judge(vectors(1500, 4500), { vectors: { count: 3 } }).verdict;
  expect(slow).toEqual({
    tone: "warn",
    step: "vectors",
    text: "L’étape « Vecteurs » ralentit tout : 4,5 s au p95 ces 10 dernières minutes, contre 1,5 s d’habitude. 3 documents attendent à cette étape.",
  });
  // A queue whose oldest document waits longer than twice the usual p95.
  const since = new Date(TO - 5 * MIN).toISOString();
  const queued = judge(vectors(1500, 1500), {
    vectors: { count: 4, oldest_since: since },
  }).verdict;
  expect(queued.step).toBe("vectors");
  expect(queued.text).toBe(
    "Des documents s’accumulent à l’étape « Vecteurs » : 4 documents en attente, le plus ancien depuis 5 min.",
  );
  expect(
    judge(vectors(1500, 1500), { vectors: { count: 1, oldest_since: since } })
      .verdict.text,
  ).toBe(
    "Un document attend à l’étape « Vecteurs » depuis 5\u00a0min, plus longtemps que d’habitude.",
  );
  // A document waiting under a minute is ordinary work, even for a step
  // that usually takes a second or has no measure yet (seen in CI: an
  // alert decision 16 s after the text became searchable).
  const fresh = new Date(TO - 30000).toISOString();
  expect(
    judge(vectors(1500, 1500), { vectors: { count: 4, oldest_since: fresh } })
      .verdict.tone,
  ).toBe("ok");
  expect(
    bottleneck(
      stepRows(list([vectors(1500, 1500)]), null, {
        alerts: { count: 1, oldest_since: fresh },
      }),
      "1h",
      TO,
    ).tone,
  ).toBe("ok");
  expect(judge(vectors(1500, 1500)).verdict.text).toBe(
    "Tout s’écoule normalement. L’étape la plus longue est « Vecteurs », 1,5 s au p95, comme d’habitude.",
  );
  expect(bottleneck(stepRows(list([]), null), "1h", TO).tone).toBe("quiet");
});

test("un plugin est en panne, dégradé, rétabli ou au repos selon ses dernières minutes", () => {
  // The hour of calls the Plugins section reads, for one plugin.
  const state = (at: (ago: number) => Partial<StatsPoint> | null) => {
    const hour = list([call(minutes(at))]);
    return pluginRows([], hour, hour)[0];
  };
  const cases: [
    string,
    (ago: number) => Partial<StatsPoint> | null,
    string,
    RegExp,
  ][] = [
    [
      "every call of the last minute fails",
      (ago) => ({ count: 10, errors: ago < 2 ? 10 : 0 }),
      "down",
      /20 appels sur 20 en échec/,
    ],
    [
      "the one call of a quiet minute fails",
      (ago) =>
        ago === 0 ? { count: 1, errors: 1 } : ago < 3 ? null : { count: 1 },
      "degraded",
      /1 appel sur 2 en échec/,
    ],
    [
      "one call in ten fails",
      (ago) => ({ count: 10, errors: ago === 0 ? 1 : 0 }),
      "degraded",
      /1 appel sur 20 en échec/,
    ],
    [
      "calls succeed again after failures",
      (ago) => ({ count: 10, errors: ago >= 2 && ago < 4 ? 10 : 0 }),
      "ok",
      /^Rétabli/,
    ],
    [
      "failures older than five minutes",
      (ago) => ({ count: 10, errors: ago > 6 ? 10 : 0 }),
      "ok",
      /^50 appels réussis/,
    ],
    [
      "no call for five minutes",
      (ago) => (ago < 5 ? null : { count: 10, errors: 10 }),
      "idle",
      /Aucun appel/,
    ],
  ];
  for (const [name, at, want, reason] of cases) {
    const got = state(at);
    expect(got.state, name).toBe(want);
    expect(got.reason, name).toMatch(reason);
  }
});
