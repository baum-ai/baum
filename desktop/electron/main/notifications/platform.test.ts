import { describe, expect, it } from "vitest";
import { escapeMarkup } from "./platform.ts";

describe("platform", () => {
  it("markup characters are escaped", () => {
    expect(escapeMarkup("Fix <b>x</b> & y")).toBe("Fix &lt;b&gt;x&lt;/b&gt; &amp; y");
  });
});
