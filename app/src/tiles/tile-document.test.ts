import { describe, expect, it } from "vitest";
import { landingOf } from "./layout";
import { itemTarget, placeOf, readExpansions, withExpansions } from "./place";
import {
  changesInState,
  mustShowRisks,
  parseTileDocument,
  reviewIdOf,
  riskInChange,
  tileDocOf,
  tilesMissing,
  withPendingChanges,
  withTiles,
  type TileDocument,
} from "./tile-document";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { golden, goldens } from "./goldens-test-helper";

describe("the Go goldens", () => {
  it("are all read by the structural guard", () => {
    const names = Object.keys(goldens);
    expect(names.length).toBeGreaterThan(20);
    for (const [path, module] of Object.entries(goldens)) {
      const parsed = parseTileDocument(module.default);
      expect(parsed, path).not.toBeNull();
      // The fields every component reads, in the shapes the types say.
      const doc = parsed satisfies TileDocument | null;
      for (const tile of doc!.tiles) {
        expect(typeof tile.id, path).toBe("string");
        expect(["ready", "pending", "failed", "unavailable"], path).toContain(tile.state);
      }
      for (const entry of doc!.layout.entries)
        expect(["full", "half", "third"]).toContain(entry.size);
      expect(doc!.layout.landing).toBeLessThanOrEqual(doc!.layout.entries.length);
    }
  });

  it("rejects what is not a tile document", () => {
    expect(parseTileDocument(null)).toBeNull();
    expect(parseTileDocument({ schema: "baum.review/1" })).toBeNull();
    const broken = structuredClone(golden("base")) as unknown as Record<string, unknown>;
    (broken.layout as { entries: unknown[] }).entries.push({ tile: "nowhere", size: "third" });
    expect(parseTileDocument(broken)).toBeNull();
  });
});

describe("the substrate", () => {
  it("resolves a renamed duplicate by its change and its ID there", () => {
    const doc = golden("duplicate-ids");
    const renamed = riskInChange(doc, "sec_queue_limits", "rsk_retry_storm");
    expect(renamed?.id).toBe("rsk_retry_storm_queue_limits_2");
    expect(reviewIdOf(renamed!)).toBe("rsk_retry_storm");
    expect(riskInChange(doc, "sec_retry_policy", "rsk_retry_storm")?.id).toBe("rsk_retry_storm");
    // The risk score's reasons keep the IDs the changes gave their risks.
    for (const reason of doc.risk_score!.reasons)
      expect(riskInChange(doc, reason.section, reason.risk!)?.change).toBe(reason.section);
  });

  it("lists the must-read risks of current changes, critical first", () => {
    expect(mustShowRisks(golden("critical-risks")).map((risk) => risk.tier)).toEqual([
      "critical",
      "high",
    ]);
    expect(mustShowRisks(golden("no-current-logical"))).toEqual([]);
  });

  it("marks retried changes pending", () => {
    const doc = golden("details-states");
    expect(changesInState(doc, "failed").map((change) => change.id)).toEqual(["sec_config_flag"]);
    const retrying = withPendingChanges(doc, ["sec_config_flag"]);
    expect(changesInState(retrying, "failed")).toEqual([]);
    expect(retrying.tiles.find((tile) => tile.id === "sec_config_flag")?.state).toBe("pending");
  });

  it("travels with the review through the page's copies", () => {
    const review = { schema: "baum.review/1" } as unknown as ReviewDocument;
    const tiled = withTiles(review, golden("base"));
    expect(tileDocOf({ ...tiled })?.schema).toBe("baum.tiles/1");
    expect(tilesMissing(withTiles({ ...review }, null))).toBe(true);
    expect(tilesMissing(review)).toBe(false);
  });
});

describe("the landing page", () => {
  it("shows a group's first members and holds the rest behind its control", () => {
    const doc = golden("many-changes");
    const landing = landingOf(doc, readExpansions(new URLSearchParams()));
    const kinds = landing.items.map((item) =>
      item.kind === "tile" ? item.tile.id : `${item.kind}:${"group" in item ? item.group.id : ""}`,
    );
    expect(kinds.slice(0, 3)).toEqual(["overview", "risks", "heading:changes"]);
    const more = landing.items.find((item) => item.kind === "more");
    // Nine changes: six shown, the other three behind the group's control.
    expect(more?.kind === "more" && more.hidden.map((tile) => tile.id)).toEqual([
      "sec_part_1",
      "sec_part_0",
      "sec_part_4",
    ]);
    expect(landing.rest.map((tile) => tile.id)).toEqual([
      "evidence",
      "media",
      "intent",
      "context",
      "decisions",
      "everything_else",
    ]);
  });

  it("opens a group and the rest as the address asks", () => {
    const doc = golden("many-changes");
    const search = withExpansions(new URLSearchParams("pr=7"), {
      more: true,
      groups: new Set(["changes"]),
    });
    expect(search.toString()).toBe("pr=7&more=1&expand=changes");
    const landing = landingOf(doc, readExpansions(search));
    expect(landing.rest).toEqual([]);
    expect(landing.items.some((item) => item.kind === "more")).toBe(false);
    expect(landing.items.filter((item) => item.kind === "less")).toHaveLength(1);
    expect(landing.items.filter((item) => item.kind === "tile")).toHaveLength(
      doc.layout.entries.length,
    );
  });

  it("holds an obsolete group back entirely", () => {
    const doc = golden("stale-without-hunks");
    const landing = landingOf(doc, { more: true, groups: new Set() });
    const obsolete = landing.items.find(
      (item) => item.kind === "more" && item.group.id === "obsolete",
    );
    expect(obsolete?.kind === "more" && obsolete.hidden.map((tile) => tile.id)).toEqual([
      "sec_old_approach",
      "sec_old_formatting",
    ]);
  });
});

describe("addresses", () => {
  const doc = golden("base");

  it("land the full review's places on the tiles that hold them", () => {
    expect(placeOf({ kind: "summary" }, doc)).toEqual({ kind: "landing" });
    expect(placeOf({ kind: "changes" }, doc)).toEqual({ kind: "landing", focus: "group:changes" });
    const section = placeOf({ kind: "section", id: "sec_retry_policy" }, doc);
    expect(section.kind === "tile" && section.tile.id).toBe("sec_retry_policy");
    // The mechanical change is read in Everything else.
    const mechanical = placeOf({ kind: "section", id: "sec_generated_client" }, doc);
    expect(mechanical.kind === "tile" && mechanical.tile.id).toBe("everything_else");
    const risk = placeOf({ kind: "risk", section: "sec_retry_policy", id: "rsk_retry_storm" }, doc);
    expect(risk.kind === "tile" && [risk.tile.id, risk.item]).toEqual(["risks", "rsk_retry_storm"]);
    expect(placeOf({ kind: "tile", id: "nowhere" }, doc)).toEqual({ kind: "landing" });
  });

  it("send an item to the tile that gathers it", () => {
    expect(itemTarget(doc, "sec_retry_policy", "ev_retry_test")?.tile.id).toBe("evidence");
    expect(itemTarget(doc, "sec_retry_policy", "dec_doubling_delay")?.tile.id).toBe("decisions");
    // A mitigated risk is read in its change.
    const mitigated = itemTarget(doc, "sec_retry_policy", "rsk_old_backoff");
    expect([mitigated?.tile.id, mitigated?.item]).toEqual(["sec_retry_policy", "rsk_old_backoff"]);
    const renamed = itemTarget(golden("duplicate-ids"), "sec_queue_limits", "rsk_retry_storm");
    expect(renamed?.item).toBe("rsk_retry_storm_queue_limits_2");
  });
});
