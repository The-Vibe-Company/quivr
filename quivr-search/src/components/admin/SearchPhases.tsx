// Recherche: where a search spends its time. One row per phase the engine
// reports in Server-Timing, with its p50 and p95 over the last searches the
// demo relayed, on a scale shared by every phase.
import { useCallback, useEffect, useState } from "react";
import { AdminSection, type SectionProps } from "./AdminSection";
import { APIError, request } from "../../lib/search";
import { short } from "../../lib/admin";
import type { StatsStatus } from "../../lib/adminStats";

interface SearchPhases {
  searches: number;
  phases: { phase: string; p50_ms: number; p95_ms: number }[];
}

const LABELS: Record<string, string> = {
  routing: "Aiguillage",
  coverage: "Couverture",
  plugin: "Plugin de recherche",
  encode: "Encodage de la requête",
  index: "Index",
  hydrate: "Lecture des passages",
};
const POLL_MS = 15000;

function useSearchPhases(onUnauthorized: () => void) {
  const [data, setData] = useState<SearchPhases | null>(null);
  const [status, setStatus] = useState<StatsStatus>("loading");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    const read = () =>
      request<SearchPhases>("/demo/admin/search-phases", undefined, controller.signal)
        .then((next) => {
          setData(next);
          setStatus("ready");
        })
        .catch((e) => {
          if (controller.signal.aborted) return;
          if (e instanceof APIError && e.status === 401) return onUnauthorized();
          setStatus("error");
        });
    setStatus("loading");
    void read();
    const timer = setInterval(() => {
      if (!document.hidden) void read();
    }, POLL_MS);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, [attempt, onUnauthorized]);
  return { data, status, reload: useCallback(() => setAttempt((n) => n + 1), []) };
}

export function SearchPhases({ onUnauthorized, bare }: SectionProps) {
  const { data, status, reload } = useSearchPhases(onUnauthorized);
  const top = Math.max(1, ...(data?.phases.map((p) => p.p95_ms) ?? []));
  return (
    <AdminSection
      id="search-phases"
      title="Phases d’une recherche"
      bare={bare}
      status={status}
      onRetry={reload}
    >
      {!data?.searches ? (
        <p className="admin-section-note">Aucune recherche mesurée depuis le démarrage.</p>
      ) : (
        <>
          <table className="search-phases">
            <caption className="visually-hidden">
              Durée de chaque phase sur les {data.searches} dernières recherches
            </caption>
            <thead>
              <tr>
                <th scope="col">Phase</th>
                <th scope="col">p50</th>
                <th scope="col">p95</th>
                <th scope="col">
                  <span className="visually-hidden">Part du p95 le plus long</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {data.phases.map((p) => (
                <tr key={p.phase}>
                  <th scope="row">{LABELS[p.phase] ?? p.phase}</th>
                  <td>{short(p.p50_ms)}</td>
                  <td>{short(p.p95_ms)}</td>
                  <td aria-hidden="true">
                    <meter min={0} max={top} value={p.p95_ms} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="neck-legend">
            Sur les {data.searches.toLocaleString("fr-FR")} dernières recherches.
          </p>
        </>
      )}
    </AdminSection>
  );
}
