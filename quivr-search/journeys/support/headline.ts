import type { Screen } from 'e2e';
import { expect } from 'e2e';

/**
 * A word of five letters or more from the Fil's newest headline, lower-cased:
 * live data moves, so journeys search for what is on screen, never a fixed word.
 */
export async function headlineWord(screen: Screen): Promise<string> {
  const first = screen.getByRole('list', 'Derniers éléments').getByRole('link').first();
  await expect(first).toBeVisible({ timeout: 15_000 });
  const word = ((await first.textContent()) ?? '')
    .split(/[^\p{L}]+/u)
    .find((part) => part.length >= 5);
  if (!word) throw new Error('the newest headline has no word of five letters or more to search for');
  return word.toLocaleLowerCase('fr');
}

/** The heading of a search's results, "N articles sur « word »". */
export const resultsFor = (word: string) =>
  new RegExp(`^[1-9][\\d\\s\\u00a0\\u202f]*\\+? articles? sur « ${word} »$`);
