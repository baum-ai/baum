import { describe, expect, it } from "vitest";

import {
  formatBucket,
  durationSeconds,
  EM_DASH,
  formatCompact,
  formatCount,
  formatDate,
  formatDelta,
  formatDuration,
  formatHours,
  formatSHA,
  formatShare,
  formatTime,
  formatUSD,
  initialsFor,
} from "./format";

describe("format helpers", () => {
  it("formats dollars by magnitude with the plain dollar sign", () => {
    expect(formatUSD(null)).toBe(EM_DASH);
    expect(formatUSD(0)).toBe("$0.0000");
    expect(formatUSD(1.25)).toBe("$1.25");
    expect(formatUSD(1234.5)).toBe("$1,235");
  });

  // The suite runs pinned to en-US, where the default symbol is already "$",
  // so these name their locales to guard non-US readers.
  it("keeps the plain dollar sign in locales where the dollar is foreign", () => {
    expect(formatUSD(1.25, "en-GB")).toBe("$1.25");
    expect(formatUSD(1.25, "fr-FR")).toBe("1,25\u00a0$");
    for (const locale of ["en-GB", "en-AU", "fr-FR", "de-DE", "ar-EG", "sq-AL", "he-IL"]) {
      const formatted = formatUSD(1234.5, locale);
      expect(formatted, locale).toContain("$");
      expect(formatted, locale).not.toMatch(/US\$|USD|\$US/);
    }
  });

  it("distinguishes missing and invalid numbers from zero", () => {
    expect(formatCount(null)).toBe(EM_DASH);
    expect(formatCount(Number.NaN)).toBe(EM_DASH);
    expect(formatCount(0)).toBe("0");
  });

  it("formats compact values, shares, deltas, and durations", () => {
    expect(formatCompact(999)).toBe("999");
    expect(formatCompact(1200)).toMatch(/1[.,]2K/i);
    expect(formatShare(0.125, 1)).toMatch(/12[.,]5%/);
    expect(formatDelta(0.2)).toBe("+20%");
    expect(formatDuration(59)).toBe("59s");
    expect(formatDuration(60)).toBe("1m");
    expect(formatDuration(5400)).toMatch(/1[.,]5h/);
    expect(formatHours(0)).toBe("<1h");
  });

  it("handles invalid dates and time intervals", () => {
    expect(formatDate("not-a-date")).toBe(EM_DASH);
    expect(formatTime("not-a-date")).toBe(EM_DASH);
    expect(formatTime(null)).toBe(EM_DASH);
    expect(formatTime("2026-07-01T10:05:00Z")).toMatch(/\d{1,2}:05/);
    expect(durationSeconds("2026-07-01T10:00:00Z", "2026-07-01T10:01:30Z")).toBe(90);
    expect(durationSeconds("2026-07-02", "2026-07-01")).toBeNull();
  });

  it("formats names and commit identifiers", () => {
    expect(initialsFor("Ada Lovelace")).toBe("AL");
    expect(initialsFor("acme/backend")).toBe("AB");
    expect(initialsFor("///")).toBe("?");
    expect(formatSHA("0123456789abcdef")).toBe("0123456");
  });
});

describe("formatBucket in the report timezone", () => {
  it("names the day the series was bucketed in, not the reader's day", () => {
    // Midnight UTC on the 23rd is still the 22nd in Los Angeles.
    const bucket = "2026-07-23T00:00:00Z";
    expect(formatBucket(bucket, "day", "UTC")).toBe(
      new Intl.DateTimeFormat(undefined, {
        month: "short",
        day: "numeric",
        timeZone: "UTC",
      }).format(new Date(bucket)),
    );
    expect(formatBucket(bucket, "day", "UTC")).not.toBe(
      formatBucket(bucket, "day", "America/Los_Angeles"),
    );
  });

  it("falls back to the reader's zone for an unknown zone name", () => {
    expect(formatBucket("2026-07-23T12:00:00Z", "day", "Not/AZone")).toBe(
      formatBucket("2026-07-23T12:00:00Z", "day"),
    );
  });
});
