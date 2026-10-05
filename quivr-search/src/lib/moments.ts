// The moments of the feed's timeline: the last hour, the rest of today,
// yesterday, the rest of the past seven days, and anything older.

export interface Moment<T> {
  key: string;
  label: string;
  rows: T[];
}

const weekday = new Intl.DateTimeFormat("fr-FR", {
  weekday: "long",
  day: "numeric",
  month: "short",
});
const weekdayYear = new Intl.DateTimeFormat("fr-FR", {
  weekday: "long",
  day: "numeric",
  month: "short",
  year: "numeric",
});

const dayStart = (time: number) => new Date(time).setHours(0, 0, 0, 0);

/** The local day of a time, as "2026-10-03". */
export function dayOf(value: string | number) {
  const at = new Date(value);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}`;
}

/** The local midnight a day starts at. */
const startOf = (day: string) => {
  const [y, m, d] = day.split("-").map(Number);
  return new Date(y, m - 1, d).getTime();
};

/** The day `count` days after (or before, when negative) a day. */
export function shiftDay(day: string, count: number) {
  const [y, m, d] = day.split("-").map(Number);
  return dayOf(new Date(y, m - 1, d + count).getTime());
}

/** A time as RFC 3339 in the local time zone: "2026-10-02T00:00:00+02:00". */
export function localInstant(time: number) {
  const at = new Date(time);
  const pad = (n: number) => String(Math.abs(n)).padStart(2, "0");
  const offset = -at.getTimezoneOffset();
  return (
    `${dayOf(time)}T${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}` +
    `${offset < 0 ? "-" : "+"}${pad(Math.trunc(offset / 60))}:${pad(offset % 60)}`
  );
}

/**
 * A local day as the bounds Quivr filters on: from its midnight, inclusive,
 * to the next one, exclusive, each with its own offset (a day may change
 * from summer to winter time).
 */
export function dayBounds(day: string) {
  return {
    after: localInstant(startOf(day)),
    before: localInstant(startOf(shiftDay(day, 1))),
  };
}

/** "Aujourd’hui", "Hier", then "Jeudi 1 oct.", with the year when not this one. */
export function dayLabel(day: string, now: number) {
  const start = startOf(day);
  const days = Math.round((dayStart(now) - start) / 86400000);
  if (days === 0) return "Aujourd’hui";
  if (days === 1) return "Hier";
  const format = new Date(start).getFullYear() === new Date(now).getFullYear() ? weekday : weekdayYear;
  const label = format.format(start);
  return label[0].toUpperCase() + label.slice(1);
}

/** Where a time falls on the timeline, seen from `now`. */
export function momentOf(value: string | undefined, now: number) {
  const time = value ? Date.parse(value) : NaN;
  if (Number.isNaN(time)) return { key: "older", label: "Plus ancien" };
  if (now - time < 3600000) return { key: "hour", label: "Dernière heure" };
  const days = Math.round((dayStart(now) - dayStart(time)) / 86400000);
  if (days <= 0) return { key: "today", label: "Aujourd’hui" };
  if (days === 1) return { key: "yesterday", label: "Hier" };
  if (days < 7) return { key: "week", label: "7 derniers jours" };
  return { key: "older", label: "Plus ancien" };
}

/** Groups rows, newest first, into consecutive moments. */
export function moments<T>(
  rows: T[],
  when: (row: T) => string | undefined,
  now: number,
): Moment<T>[] {
  const out: Moment<T>[] = [];
  for (const row of rows) {
    const moment = momentOf(when(row), now);
    const last = out.at(-1);
    if (last?.key === moment.key) last.rows.push(row);
    else out.push({ ...moment, rows: [row] });
  }
  return out;
}

/** Articles per day over the last `count` days, oldest first. */
export function daily(times: (string | undefined)[], now: number, count = 7) {
  const days = Array.from({ length: count }, (_, i) => {
    const start = new Date(now);
    start.setHours(0, 0, 0, 0);
    start.setDate(start.getDate() - (count - 1 - i));
    return { day: dayOf(start.getTime()), count: 0 };
  });
  const index = new Map(days.map((d, i) => [d.day, i]));
  for (const value of times) {
    const i = value ? index.get(dayOf(value)) : undefined;
    if (i !== undefined) days[i].count += 1;
  }
  return days;
}

const short = new Intl.DateTimeFormat("fr-FR", { weekday: "short" });

/** "auj.", "hier", then "lun.", "mar."… */
export function shortDay(day: string, now: number) {
  const label = dayLabel(day, now);
  if (label === "Aujourd’hui") return "auj.";
  if (label === "Hier") return "hier";
  const [y, m, d] = day.split("-").map(Number);
  return short.format(new Date(y, m - 1, d));
}

/**
 * Articles that arrived on a day, per hour: from midnight to the current
 * hour today, the whole day before.
 */
export function hourly(times: (string | undefined)[], day: string, now: number) {
  const [y, m, d] = day.split("-").map(Number);
  const start = new Date(y, m - 1, d).getTime();
  const end = new Date(y, m - 1, d + 1).getTime();
  const today = start === dayStart(now);
  const counts = new Array<number>(today ? new Date(now).getHours() + 1 : 24).fill(0);
  for (const value of times) {
    const time = value ? Date.parse(value) : NaN;
    if (time >= start && time < end && time <= now) counts[new Date(time).getHours()] += 1;
  }
  return { counts, today };
}
