/**
 * Light or dark theme. The app follows the system until the viewer picks a
 * theme in the rail; the choice is kept in this browser and set as
 * data-theme on <html>, which the stylesheets read.
 */
export type Theme = "light" | "dark";

const KEY = "quivr-theme";

function stored(): Theme | null {
  try {
    const value = localStorage.getItem(KEY);
    return value === "light" || value === "dark" ? value : null;
  } catch {
    return null;
  }
}

/** Applies a saved choice before the first render, so the page does not flash. */
export function applySavedTheme() {
  const theme = stored();
  if (theme) document.documentElement.dataset.theme = theme;
}

export function currentTheme(): Theme {
  return (
    stored() ??
    (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light")
  );
}

/** Calls back when the system theme changes and no theme was chosen here. */
export function onSystemTheme(callback: (theme: Theme) => void) {
  const query = matchMedia("(prefers-color-scheme: dark)");
  const listener = () => {
    if (!stored()) callback(query.matches ? "dark" : "light");
  };
  query.addEventListener("change", listener);
  return () => query.removeEventListener("change", listener);
}

export function saveTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme;
  try {
    localStorage.setItem(KEY, theme);
  } catch {
    // Private windows may refuse storage: the theme still applies until reload.
  }
}
