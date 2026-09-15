import type { RefObject } from "react";
import { MagnifyingGlass, ArrowRight, X } from "@phosphor-icons/react";
interface Props {
  value: string;
  onChange: (value: string) => void;
  onSubmit: (value: string) => void;
  inputRef: RefObject<HTMLInputElement | null>;
  busy?: boolean;
}
export function SearchBar({
  value,
  onChange,
  onSubmit,
  inputRef,
  busy,
}: Props) {
  return (
    <form
      className="searchbar-field"
      role="search"
      onSubmit={(event) => {
        event.preventDefault();
        if (value.trim()) onSubmit(value.trim());
      }}
    >
      <MagnifyingGlass
        className="searchbar-icon"
        size={21}
        aria-hidden="true"
      />
      <input
        ref={inputRef}
        className="searchbar-input"
        type="search"
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder="Une idée, une question, quelques mots…"
        aria-label="Rechercher dans vos textes"
        autoComplete="off"
        enterKeyHint="search"
      />
      {value && (
        <button
          className="icon-button"
          type="button"
          aria-label="Effacer la recherche"
          onClick={() => {
            onChange("");
            inputRef.current?.focus();
          }}
        >
          <X size={16} aria-hidden="true" />
        </button>
      )}
      <button
        className="search-submit"
        type="submit"
        aria-label="Lancer la recherche"
        disabled={!value.trim() || busy}
      >
        <ArrowRight size={20} weight="bold" aria-hidden="true" />
      </button>
    </form>
  );
}
