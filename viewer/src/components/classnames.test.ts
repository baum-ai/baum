import { describe, expect, it } from "vitest";
import { cn } from "./classnames";

describe("cn", () => {
  it("joins classes and skips empty values", () => {
    expect(cn("flex", false, null, undefined, "gap-2")).toBe("flex gap-2");
  });

  it("lets a later utility replace a conflicting earlier one", () => {
    expect(cn("h-7 w-7 px-4", "h-9 px-2")).toBe("w-7 h-9 px-2");
    expect(cn("text-(--text-tertiary)", "text-(--text-primary)")).toBe("text-(--text-primary)");
  });

  it("treats the palette utilities as colours", () => {
    expect(cn("text-tertiary", "text-primary")).toBe("text-primary");
    expect(cn("bg-surface-card", "bg-(--surface-hover)")).toBe("bg-(--surface-hover)");
    expect(cn("border-subtle", "border-status-critical")).toBe("border-status-critical");
  });

  it("keeps a type-scale step beside a text colour", () => {
    expect(cn("text-3xs text-secondary")).toBe("text-3xs text-secondary");
    expect(cn("text-xs text-(--text-secondary)")).toBe("text-xs text-(--text-secondary)");
    expect(cn("text-public-note text-primary")).toBe("text-public-note text-primary");
    expect(cn("text-3xs", "text-base")).toBe("text-base");
    expect(cn("text-public-note", "text-base")).toBe("text-base");
  });

  it("treats the palette shadows as shadows", () => {
    expect(cn("shadow-card", "shadow-none")).toBe("shadow-none");
    expect(cn("shadow-raised", "shadow-(--action-shadow)")).toBe("shadow-(--action-shadow)");
    expect(cn("shadow-overlay border-default")).toBe("shadow-overlay border-default");
  });

  it("keeps utilities under different variants apart", () => {
    expect(cn("text-tertiary hover:text-primary", "text-secondary")).toBe(
      "hover:text-primary text-secondary",
    );
  });
});
