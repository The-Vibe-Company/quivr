import { useEffect, useState } from "react";
import { FileText } from "@phosphor-icons/react";
import { fetchDocument } from "../lib/search";
import { Dialog } from "./Dialog";
import { Highlight } from "./Highlight";
import type { DocumentDetail } from "../types";
export function DocumentPanel({
  record,
  version,
  terms,
  onClose,
}: {
  record: string;
  version: string;
  terms: string[];
  onClose: () => void;
}) {
  const [doc, setDoc] = useState<DocumentDetail | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setError("");
    setDoc(null);
    fetchDocument(record, version, controller.signal)
      .then(setDoc)
      .catch((error) => {
        if (!controller.signal.aborted) setError(error.message);
      });
    return () => controller.abort();
  }, [record, version, attempt]);
  // Connector items carry a title Part; texts pasted in the app do not.
  const collected = doc?.manifest.parts.some((part) => part.role === "title");
  return (
    <Dialog title="Document source" onClose={onClose} wide>
      <div className="document-body">
        <div className="eyebrow">
          <FileText size={16} aria-hidden="true" /> Texte original
        </div>
        <h1>{collected ? "Document collecté" : "Votre document"}</h1>
        {!doc && !error && <p role="status">Chargement du texte…</p>}
        {error && (
          <div className="notice" role="alert">
            <p>{error}</p>
            <button
              className="button"
              onClick={() => setAttempt((value) => value + 1)}
            >
              Réessayer
            </button>
          </div>
        )}
        {doc && (
          <>
            <p className="document-caption">
              {collected
                ? "Le texte tel qu’il a été collecté, sans modification."
                : "Le texte tel que vous l’avez ajouté, sans modification."}
            </p>
            {doc.manifest.parts
              // Markup kept beside a text Part is not shown as text.
              .filter((part) => part.role !== "source_html")
              .map((part) => (
                <div
                  key={part.key}
                  className="canonical-text"
                  data-testid="canonical-text"
                >
                  <Highlight text={part.content.text} terms={terms} />
                </div>
              ))}
          </>
        )}
      </div>
    </Dialog>
  );
}
