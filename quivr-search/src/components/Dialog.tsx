import { useEffect, useRef, type ReactNode } from "react";
import { X } from "@phosphor-icons/react";
export function Dialog({
  title,
  onClose,
  children,
  wide = false,
  closeLabel,
  label,
}: {
  title: string;
  /** The dialog's accessible name when the visible title is too short. */
  label?: string;
  onClose: () => void;
  children: ReactNode;
  wide?: boolean;
  closeLabel?: string;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const opener =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    const dialog = ref.current!;
    dialog.showModal();
    // A field marked `data-autofocus` takes the focus from the close button.
    dialog.querySelector<HTMLElement>("[data-autofocus]")?.focus();
    const prior = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      dialog.close();
      document.body.style.overflow = prior;
      opener?.focus();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      className={`modal ${wide ? "document-modal" : ""}`}
      aria-label={label || title}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClick={(event) => {
        if (event.target === ref.current) {
          const r = ref.current.getBoundingClientRect();
          if (
            event.clientX < r.left ||
            event.clientX > r.right ||
            event.clientY < r.top ||
            event.clientY > r.bottom
          )
            onClose();
        }
      }}
    >
      <header className="modal-head">
        <span>{title}</span>
        <button
          type="button"
          className="icon-button"
          onClick={onClose}
          aria-label={
            closeLabel ||
            `Fermer ${title === "Ajouter du texte" ? "l’ajout de texte" : "le document"}`
          }
        >
          <X size={19} aria-hidden="true" />
        </button>
      </header>
      {children}
    </dialog>
  );
}
