import type { Mode } from "../types";
export const MODES: Array<{ value: Mode; label: string; hint: string }> = [
  {
    value: "hybrid",
    label: "Hybride",
    hint: "Le sens et les mots de votre recherche.",
  },
  {
    value: "semantic",
    label: "Sémantique",
    hint: "Retrouvez une idée, même formulée autrement.",
  },
  {
    value: "lexical",
    label: "Mots-clés",
    hint: "Recherchez les mots présents dans vos textes.",
  },
];
export function ModeSwitch({
  mode,
  onChange,
}: {
  mode: Mode;
  onChange: (mode: Mode) => void;
}) {
  return (
    <div className="mode-switch" role="group" aria-label="Mode de recherche">
      {MODES.map((item) => (
        <button
          key={item.value}
          type="button"
          aria-pressed={mode === item.value}
          title={item.hint}
          onClick={() => onChange(item.value)}
        >
          {item.label}
        </button>
      ))}
    </div>
  );
}
