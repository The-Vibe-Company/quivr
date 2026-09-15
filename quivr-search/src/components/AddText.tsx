import { useEffect, useRef, useState } from "react";
import { Check, FileText, Plus, ArrowRight } from "@phosphor-icons/react";
import { addText, fetchReceipt } from "../lib/search";
import { Dialog } from "./Dialog";
import type { Receipt } from "../types";
const STORAGE = "quivr-demo-draft.v1";
type Draft = { text: string; key: string; receipt?: Receipt };
function fresh(): Draft {
  return { text: "", key: crypto.randomUUID() };
}
function readDraft(storage: string): Draft {
  try {
    const d = JSON.parse(sessionStorage.getItem(storage) || "null");
    return d && typeof d.text === "string" && typeof d.key === "string"
      ? d
      : fresh();
  } catch {
    return fresh();
  }
}
export function AddText({
  corpus,
  onClose,
  onOpen,
  onAdded,
}: {
  corpus: string;
  onClose: () => void;
  onOpen: (record: string, version: string) => void;
  onAdded: () => void;
}) {
  const storage = `${STORAGE}:${corpus}`;
  const [draft, setDraft] = useState<Draft>(() => readDraft(storage));
  const [sending, setSending] = useState(false);
  const sendingRef = useRef(false);
  const [error, setError] = useState("");
  const [pollAttempt, setPollAttempt] = useState(0);
  const [storageUnavailable, setStorageUnavailable] = useState(false);
  const save = (next: Draft) => {
    setDraft(next);
    try {
      sessionStorage.setItem(storage, JSON.stringify(next));
    } catch {
      setStorageUnavailable(true);
    }
  };
  const bytes = new TextEncoder().encode(draft.text).length;
  const ready = draft.receipt?.availability?.searchable === true;
  const blocked = draft.receipt?.processing.state === "blocked";
  const semanticPending =
    ready && !blocked && draft.receipt?.processing.state !== "idle";
  useEffect(() => {
    const id = draft.receipt?.receipt_id;
    if (!id || blocked || (ready && !semanticPending)) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const deadline = Date.now() + 90000;
    let notified = ready;
    const poll = async () => {
      try {
        const receipt = await fetchReceipt(id, controller.signal);
        if (controller.signal.aborted) return;
        const next = { ...draft, receipt };
        save(next);
        if (receipt.availability?.searchable && !notified) {
          notified = true;
          onAdded();
        }
        if (
          receipt.processing.state === "blocked" ||
          (receipt.availability?.searchable &&
            receipt.processing.state === "idle")
        )
          return;
        if (Date.now() > deadline) {
          setError(
            "Votre texte est enregistré. Le traitement continue ; vous pouvez vérifier son état à nouveau.",
          );
          return;
        }
        timer = setTimeout(poll, 700);
      } catch (error) {
        if (!controller.signal.aborted)
          setError(
            error instanceof Error
              ? error.message
              : "Impossible de vérifier cet ajout.",
          );
      }
    };
    void poll();
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
    // Poll owns the successive receipt snapshots until completion or explicit retry.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draft.receipt?.receipt_id, pollAttempt]);
  async function submit() {
    if (sendingRef.current || !draft.text.trim() || bytes > 262144) return;
    sendingRef.current = true;
    setSending(true);
    setError("");
    save(draft);
    try {
      const receipt = await addText(draft.text, draft.key, corpus);
      save({ ...draft, receipt });
      onAdded();
    } catch (error) {
      setError(
        error instanceof Error
          ? error.message
          : "L’envoi a échoué. Réessayez avec ce même texte.",
      );
    } finally {
      sendingRef.current = false;
      setSending(false);
    }
  }
  return (
    <Dialog title="Ajouter du texte" onClose={onClose}>
      <div className="add-content">
        {storageUnavailable && (
          <p className="muted" role="status">
            Votre navigateur ne conserve pas ce brouillon après un rechargement.
            L’ajout reste possible.
          </p>
        )}
        {!draft.receipt ? (
          <>
            <div className="modal-icon">
              <FileText size={24} aria-hidden="true" />
            </div>
            <h2>Vos textes, à portée de recherche.</h2>
            <p className="muted">
              Collez une note, un article ou quelques paragraphes. Retrouvez-les
              ensuite avec vos propres mots.
            </p>
            <form
              onSubmit={(event) => {
                event.preventDefault();
                void submit();
              }}
            >
              <label className="field-label" htmlFor="source-text">
                Votre texte
              </label>
              <textarea
                id="source-text"
                value={draft.text}
                onChange={(event) => {
                  save({ text: event.target.value, key: crypto.randomUUID() });
                  setError("");
                }}
                disabled={sending}
                autoFocus
                placeholder="Collez votre texte ici…"
                aria-describedby="text-help"
              />
              <div className="field-help" id="text-help">
                <span>Texte brut · conservé à l’identique</span>
                <span className={bytes > 262144 ? "error-text" : ""}>
                  {Math.ceil(bytes / 1024)} / 256 Ko
                </span>
              </div>
              {bytes > 262144 && (
                <p className="error-text" role="alert">
                  Ce texte est trop long. Gardez un maximum de 256 Ko.
                </p>
              )}
              {error && (
                <p className="error-text" role="alert">
                  {error}
                </p>
              )}
              <div className="modal-actions">
                <button className="button" type="button" onClick={onClose}>
                  Fermer
                </button>
                <button
                  className="button primary"
                  type="submit"
                  disabled={sending || !draft.text.trim() || bytes > 262144}
                >
                  <Plus size={17} aria-hidden="true" />
                  {sending ? "Envoi en cours…" : "Ajouter à la démo"}
                </button>
              </div>
            </form>
          </>
        ) : (
          <>
            <div className={`modal-icon ${ready ? "success" : ""}`}>
              <Check size={25} aria-hidden="true" />
            </div>
            <h2>
              {ready
                ? "Votre texte est prêt."
                : blocked
                  ? "Votre texte est conservé."
                  : "Texte reçu."}
            </h2>
            <p className="muted">
              {ready
                ? "Il fait maintenant partie de votre espace de recherche."
                : blocked
                  ? "Ce contenu ne peut pas être rendu disponible pour la recherche."
                  : "Vous pouvez fermer cette fenêtre. Nous préparons votre texte pour la recherche."}
            </p>
            <ol className="ingestion-steps" aria-live="polite">
              <li data-done="true">
                <Check size={16} aria-hidden="true" />
                <span>Texte enregistré</span>
              </li>
              <li data-done={ready}>
                <span className="step-indicator">
                  {ready ? <Check size={16} aria-hidden="true" /> : "2"}
                </span>
                <span>
                  {ready
                    ? "Disponible pour la recherche"
                    : blocked
                      ? "Traitement bloqué"
                      : "Préparation de la recherche…"}
                </span>
              </li>
              {ready && blocked && (
                <li className="muted">
                  La recherche par mots-clés reste disponible. La recherche par
                  sens n’a pas pu être préparée.
                </li>
              )}
              {semanticPending && (
                <li className="muted">
                  La recherche par mots-clés est prête. La recherche par sens se
                  prépare encore.
                </li>
              )}
            </ol>
            {error && (
              <div className="notice" role="alert">
                <p>{error}</p>
                <button
                  className="button"
                  onClick={() => {
                    setError("");
                    setPollAttempt((value) => value + 1);
                  }}
                >
                  Vérifier à nouveau
                </button>
              </div>
            )}
            <div className="modal-actions">
              <button
                className="button"
                onClick={() => {
                  save(fresh());
                  setError("");
                }}
              >
                Ajouter un autre texte
              </button>
              {draft.receipt.record_id && draft.receipt.version_id && (
                <button
                  className="button primary"
                  onClick={() =>
                    onOpen(
                      draft.receipt!.record_id!,
                      draft.receipt!.version_id!,
                    )
                  }
                >
                  Lire le document <ArrowRight size={17} aria-hidden="true" />
                </button>
              )}
            </div>
          </>
        )}
      </div>
    </Dialog>
  );
}
