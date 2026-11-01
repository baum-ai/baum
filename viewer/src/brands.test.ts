import { describe, expect, it } from "vitest";

import { type Brand, harnessBrand, modelBrand } from "./brands";
import { BRAND_ARTWORK } from "./components/brand-artwork";

describe("brand mark colours", () => {
  it("defines an artwork color for every brand", () => {
    const brands = [
      "claude",
      "cursor",
      "deepseek",
      "gemini",
      "grok",
      "openai",
      "pi",
    ] satisfies Brand[];
    for (const brand of brands) expect(BRAND_ARTWORK[brand]).toBeTruthy();
  });
});

describe("harnessBrand", () => {
  it("matches vendor slugs and looser spellings of them", () => {
    expect(harnessBrand("claude_code")).toBe("claude");
    expect(harnessBrand("Claude Code")).toBe("claude");
    expect(harnessBrand("codex")).toBe("openai");
    expect(harnessBrand("cursor")).toBe("cursor");
    expect(harnessBrand("pi")).toBe("pi");
    expect(harnessBrand("gemini_cli")).toBe("gemini");
  });

  it("gives an unknown or missing vendor no mark", () => {
    expect(harnessBrand("aider")).toBeNull();
    expect(harnessBrand("")).toBeNull();
    expect(harnessBrand(null)).toBeNull();
  });
});

describe("modelBrand", () => {
  it("matches models by the family in their name", () => {
    expect(modelBrand("claude-opus-5-5[1m]")).toBe("claude");
    expect(modelBrand("gpt-5.6-sol")).toBe("openai");
    expect(modelBrand("gpt-5.3-codex")).toBe("openai");
    expect(modelBrand("codex-auto-review")).toBe("openai");
    expect(modelBrand("grok-4.5")).toBe("grok");
    expect(modelBrand("composer-2.5-fast")).toBe("cursor");
    expect(modelBrand("deepseek-flash")).toBe("deepseek");
  });

  it("prefers the model family over the reseller that served it", () => {
    expect(modelBrand("cursor-grok-4.5-high-fast")).toBe("grok");
    expect(modelBrand("cursor-gpt-5")).toBe("openai");
    expect(modelBrand("cursor-auto")).toBe("cursor");
  });

  it("looks past a gateway prefix", () => {
    expect(modelBrand("openai/gpt-5.5")).toBe("openai");
    expect(modelBrand("anthropic/claude-sonnet-5")).toBe("claude");
    expect(modelBrand("openrouter/deepseek-flash")).toBe("deepseek");
  });

  it("gives an unknown or unreported model no mark", () => {
    expect(modelBrand("<synthetic>")).toBeNull();
    expect(modelBrand("")).toBeNull();
  });
});
