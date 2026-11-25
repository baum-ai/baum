import { describe, expect, it } from "vitest";
import { DECLARATION_SWITCH, declarationArgument, parseDeclaration } from "./contract.ts";

describe("the declaration on the renderer's command line", () => {
  it("round-trips", () => {
    for (const declaration of [
      { tileFrames: true, checkWindow: false, openLinks: true },
      { tileFrames: false, checkWindow: true, openLinks: false },
    ]) {
      const argv = ["/electron", "--type=renderer", declarationArgument(declaration)];
      expect(parseDeclaration(argv)).toEqual(declaration);
    }
  });

  it("is absent from a window that declares nothing", () => {
    expect(parseDeclaration(["/electron", "--type=renderer"])).toBeNull();
  });

  it("refuses anything but exactly three booleans, once", () => {
    const good = declarationArgument({ tileFrames: true, checkWindow: true, openLinks: true });
    for (const argv of [
      [good, good],
      [`${DECLARATION_SWITCH}not json`],
      [`${DECLARATION_SWITCH}null`],
      [`${DECLARATION_SWITCH}[true,true,true]`],
      [`${DECLARATION_SWITCH}{"tileFrames":true,"checkWindow":true}`],
      [`${DECLARATION_SWITCH}{"tileFrames":"yes","checkWindow":true,"openLinks":true}`],
      [`${DECLARATION_SWITCH}{"tileFrames":true,"checkWindow":true,"openLinks":true,"x":1}`],
    ]) {
      expect(parseDeclaration(argv), argv.join(" ")).toBeNull();
    }
  });
});
