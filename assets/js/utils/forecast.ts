import type {
  Forecast,
  ForecastSlot,
  SolarDetails,
  UiForecast,
  UiForecastSlot,
  UiTimeseriesEntry,
  UiSolarDetails,
} from "@/types/evcc";
import deepCopy from "./deepClone";

export enum ForecastType {
  Solar = "solar",
  Price = "price",
  Co2 = "co2",
}

function expandSlots(slots?: ForecastSlot[]): UiForecastSlot[] | undefined {
  return slots?.map(([start, end, value, estimate]) => ({
    start: start * 1000,
    end: end * 1000,
    value,
    ...(estimate ? { estimate: true } : {}),
  }));
}

function expandSolar(solar?: SolarDetails): UiSolarDetails | undefined {
  if (!solar) return undefined;
  const { timeseries, ...rest } = solar;
  return {
    ...rest,
    timeseries: timeseries?.map(([ts, val]) => ({ ts: ts * 1000, val })),
  };
}

// The API publishes forecast slots as [start, end, value] and solar entries as
// [ts, val] with timestamps in unix seconds. Expand them to objects with unix
// milliseconds so components can keep using named fields and `new Date(...)`.
export function expandForecast(forecast?: Forecast): UiForecast {
  const { grid, co2, solar, planner, feedin, temperature } = forecast ?? {};
  return {
    grid: expandSlots(grid),
    co2: expandSlots(co2),
    solar: expandSolar(solar),
    planner: expandSlots(planner),
    feedin: expandSlots(feedin),
    temperature: expandSlots(temperature),
  };
}

// return the date in local YYYY-MM-DD format
function toDayString(date: Date): string {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

// return only slots that are on a given date, ignores slots that are in the past
export function filterEntriesByDate(
  entries: UiTimeseriesEntry[],
  dayString: string
): UiTimeseriesEntry[] {
  const now = new Date();
  return entries.filter(({ ts }) => {
    const isPast = new Date(ts) < now;
    const dateMatches = toDayString(new Date(ts)) === dayString;
    return !isPast && dateMatches;
  });
}

// return the date in local YYYY-MM-DD format
export function dayStringByOffset(day: number): string {
  const date = new Date();
  date.setDate(date.getDate() + day);
  return toDayString(date);
}

// return the highest slot for a given day (0 = today, 1 = tomorrow, etc.)
export function highestSlotIndexByDay(entries: UiTimeseriesEntry[], day: number = 0): number {
  const dayString = dayStringByOffset(day);
  const dayEntries = filterEntriesByDate(entries, dayString);
  const sortedEntries = dayEntries.sort((a, b) => b.val - a.val);
  const highestEntry = sortedEntries[0];
  if (!highestEntry) return -1;
  return entries.findIndex((entry) => entry.ts === highestEntry.ts);
}

// short-term correction timings, as in the optimizer
const NOWCAST_FADE = 6 * 60 * 60 * 1000;
const HOLD_PLATEAU = 24 * 60 * 60 * 1000;
const HOLD_FADE = 24 * 60 * 60 * 1000;

function dayStart(now: number, offset: number): number {
  const d = new Date(now);
  d.setDate(d.getDate() + offset);
  return d.setHours(0, 0, 0, 0);
}

export function adjustedSolar(solar?: UiSolarDetails): UiSolarDetails | undefined {
  if (!solar?.scale) return solar;

  const { scale, nowcast, hold } = solar;
  const result = deepCopy(solar);

  const now = Date.now();
  const endOfToday = dayStart(now, 1);
  const holdSince = hold ? Date.parse(hold.since) : NaN;

  // weight of the short-term correction for a slot
  const weight = (ts: number): number => {
    if (ts < now) return 0;
    if (hold && !Number.isNaN(holdSince)) {
      const age = ts - holdSince;
      return age <= HOLD_PLATEAU ? 1 : Math.max(0, 1 - (age - HOLD_PLATEAU) / HOLD_FADE);
    }
    if (nowcast !== undefined && ts < endOfToday) return Math.max(0, 1 - (ts - now) / NOWCAST_FADE);
    return 0;
  };
  const short = nowcast ?? hold?.ratio ?? scale;

  // raw and adjusted energy per day: today counts from now on
  const days = [
    { from: now, to: endOfToday, raw: 0, adjusted: 0 },
    { from: endOfToday, to: dayStart(now, 2), raw: 0, adjusted: 0 },
    { from: dayStart(now, 2), to: dayStart(now, 3), raw: 0, adjusted: 0 },
  ];

  result.timeseries?.forEach((entry) => {
    const raw = entry.val;
    entry.val *= scale + (short - scale) * weight(entry.ts);
    const day = days.find((d) => entry.ts >= d.from && entry.ts < d.to);
    if (day) {
      day.raw += raw;
      day.adjusted += entry.val;
    }
  });

  const dayScale = (i: number) => (days[i].raw > 0 ? days[i].adjusted / days[i].raw : scale);
  if (result.today) result.today.energy *= dayScale(0);
  if (result.tomorrow) result.tomorrow.energy *= dayScale(1);
  if (result.dayAfterTomorrow) result.dayAfterTomorrow.energy *= dayScale(2);

  result.scale = 1 / scale; // invert to allow back-adjustment

  return result;
}

export function isStaticTariff(slots?: UiForecastSlot[]): boolean {
  if (!Array.isArray(slots) || slots.length === 0) return false;
  const firstValue = slots[0].value;
  return slots.every((slot) => slot.value === firstValue);
}

export interface SlotWithValue {
  start: string | number | Date;
  value: number;
}

// find index of first slot with lowest sum over span (e.g. span=4 for 1h with 15min slots)
export function findLowestSumSlotIndex(slots: SlotWithValue[], span: number): number {
  if (slots.length < span) return -1;

  let lowestSum = Infinity;
  let lowestIndex = -1;

  for (let i = 0; i <= slots.length - span; i++) {
    const sum = slots.slice(i, i + span).reduce((acc, slot) => acc + slot.value, 0);
    if (sum < lowestSum) {
      lowestSum = sum;
      lowestIndex = i;
    }
  }

  return lowestIndex;
}
