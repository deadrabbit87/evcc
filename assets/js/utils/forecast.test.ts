import { afterEach, beforeEach, describe, expect, test, vi } from "vite-plus/test";
import { adjustedSolar, expandForecast, findLowestSumSlotIndex, isStaticTariff } from "./forecast";

describe("expandForecast", () => {
  test("expands slots to milliseconds", () => {
    expect(expandForecast({ grid: [[1735689600, 1735693200, 0.25]] }).grid).toEqual([
      { start: 1735689600000, end: 1735693200000, value: 0.25 },
    ]);
  });

  test("keeps the estimate marker", () => {
    expect(expandForecast({ grid: [[1735689600, 1735693200, 0.25, 1]] }).grid).toEqual([
      { start: 1735689600000, end: 1735693200000, value: 0.25, estimate: true },
    ]);
  });

  test("expands the solar timeseries", () => {
    expect(expandForecast({ solar: { scale: 1, timeseries: [[1735689600, 1000]] } }).solar).toEqual(
      { scale: 1, timeseries: [{ ts: 1735689600000, val: 1000 }] }
    );
  });

  test("handles missing input", () => {
    expect(expandForecast(undefined)).toEqual({
      grid: undefined,
      co2: undefined,
      solar: undefined,
      planner: undefined,
      feedin: undefined,
      temperature: undefined,
    });
  });
});

describe("findLowestSumSlotIndex", () => {
  test("finds lowest sum with span of 4", () => {
    const slots = [
      { start: 1735689600000, value: 10 },
      { start: 1735690500000, value: 8 },
      { start: 1735691400000, value: 6 },
      { start: 1735692300000, value: 4 }, // sum 28 (index 0)
      { start: 1735693200000, value: 2 }, // sum 20 (index 1)
      { start: 1735694100000, value: 3 }, // sum 14 (index 2)
      { start: 1735695000000, value: 5 }, // sum 13 (index 3) lowest
      { start: 1735695900000, value: 7 }, // sum 17 (index 4)
    ];
    expect(findLowestSumSlotIndex(slots, 4)).toBe(3);
  });

  test("returns -1 when not enough slots", () => {
    const slots = [
      { start: 1735689600000, value: 10 },
      { start: 1735690500000, value: 8 },
    ];
    expect(findLowestSumSlotIndex(slots, 4)).toBe(-1);
  });

  test("handles exact span length", () => {
    const slots = [
      { start: 1735689600000, value: 5 },
      { start: 1735690500000, value: 3 },
      { start: 1735691400000, value: 2 },
      { start: 1735692300000, value: 1 },
    ];
    expect(findLowestSumSlotIndex(slots, 4)).toBe(0);
  });

  test("finds lowest at end", () => {
    const slots = [
      { start: 1735689600000, value: 10 },
      { start: 1735690500000, value: 10 },
      { start: 1735691400000, value: 1 },
      { start: 1735692300000, value: 1 },
      { start: 1735693200000, value: 1 },
    ];
    expect(findLowestSumSlotIndex(slots, 3)).toBe(2);
  });

  test("returns first index when multiple equal sums", () => {
    const slots = [
      { start: 1735689600000, value: 2 },
      { start: 1735690500000, value: 2 }, // sum 4
      { start: 1735691400000, value: 2 }, // sum 4 (same)
      { start: 1735692300000, value: 2 },
    ];
    expect(findLowestSumSlotIndex(slots, 2)).toBe(0);
  });
});

describe("isStaticTariff", () => {
  test("returns false for undefined", () => {
    expect(isStaticTariff(undefined)).toBe(false);
  });

  test("returns false for empty array", () => {
    expect(isStaticTariff([])).toBe(false);
  });

  test("returns true when all values are identical", () => {
    const slots = [
      { start: 1735689600000, end: 1735690500000, value: 0.25 },
      { start: 1735690500000, end: 1735691400000, value: 0.25 },
      { start: 1735691400000, end: 1735692300000, value: 0.25 },
    ];
    expect(isStaticTariff(slots)).toBe(true);
  });

  test("returns false when values differ", () => {
    const slots = [
      { start: 1735689600000, end: 1735690500000, value: 0.25 },
      { start: 1735690500000, end: 1735691400000, value: 0.3 },
      { start: 1735691400000, end: 1735692300000, value: 0.25 },
    ];
    expect(isStaticTariff(slots)).toBe(false);
  });
});

describe("adjustedSolar", () => {
  const hour = 3600 * 1000;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 10, 28, 11, 0));
  });
  afterEach(() => vi.useRealTimers());

  const solar = (nowcast?: number) => {
    const now = Date.now();
    return {
      scale: 0.5,
      nowcast,
      today: { energy: 1000, complete: true },
      tomorrow: { energy: 1000, complete: true },
      timeseries: [
        { ts: now - hour, val: 1000 },
        { ts: now + hour, val: 1000 },
        { ts: now + 24 * hour, val: 1000 },
      ],
    };
  };

  test("long-term scale without nowcast", () => {
    const res = adjustedSolar(solar());
    expect(res?.today?.energy).toBe(500);
    expect(res?.timeseries?.map((e) => e.val)).toEqual([500, 500, 500]);
  });

  test("nowcast fades back to the scale over the rest of today", () => {
    const res = adjustedSolar(solar(0.1));
    // 1h ahead: 5/6 nowcast, 1/6 scale. Past slot and tomorrow keep the scale
    expect(res?.timeseries?.map((e) => e.val)).toEqual([
      500,
      1000 * (0.5 + (0.1 - 0.5) * (5 / 6)),
      500,
    ]);
    expect(res?.tomorrow?.energy).toBe(500);
    // remaining energy follows the adjusted slots
    expect(res?.today?.energy).toBeCloseTo(1000 * (0.5 + (0.1 - 0.5) * (5 / 6)), 6);
  });
});
