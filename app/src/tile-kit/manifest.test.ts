import { describe, expect, it } from "vitest";
import { manifest, read } from "./harness";

const css = read("v1/kit.css");
const tokens = read("tokens.css");

describe("the kit's manifest", () => {
  it("names a rule for every class and attribute value", () => {
    for (const entry of manifest.classes) {
      expect(css, entry.name).toMatch(new RegExp(`\\.${entry.name}(?![a-z0-9-])`));
      for (const attribute of entry.attributes ?? []) {
        for (const value of attribute.values ?? []) {
          expect(css, `${entry.name} ${attribute.name}=${value}`).toContain(
            `[${attribute.name}="${value}"]`,
          );
        }
      }
    }
  });

  it("defines every token in the generated tokens or the kit", () => {
    for (const token of manifest.tokens) {
      expect(`${tokens}\n${css}`, token.name).toContain(`${token.name}:`);
    }
  });

  it("describes every entry", () => {
    for (const entry of [
      ...manifest.api,
      ...manifest.elements,
      ...manifest.classes,
      ...manifest.tokens,
    ]) {
      expect(entry, entry.name).toMatchObject({
        name: expect.any(String),
        kind: expect.any(String),
      });
      expect((entry as { description?: string }).description, entry.name).toBeTruthy();
    }
  });

  it("styles the kit's tokens for both themes from the generated palette", () => {
    expect(tokens).toContain(':root[data-theme="dark"]');
    expect(tokens).not.toContain("prefers-color-scheme");
    expect(css).not.toContain("prefers-color-scheme");
  });
});
