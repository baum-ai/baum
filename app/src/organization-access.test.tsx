import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { OrganizationAccessNotice, organizationNames } from "./organization-access";

let root: Root;
let node: HTMLDivElement;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
});

it("lists organization names in plain words", () => {
  expect(organizationNames(["a"])).toBe("a");
  expect(organizationNames(["a", "b"])).toBe("a and b");
  expect(organizationNames(["a", "b", "c"])).toBe("a, b and c");
  expect(organizationNames(["a", "b", "c", "d", "e"])).toBe("a, b, c and 2 more");
});

it("renders nothing without a hidden organization", async () => {
  await act(async () =>
    root.render(
      <OrganizationAccessNotice access={{ hidden: [], approval_url: "x" }} recheck={vi.fn()} />,
    ),
  );
  expect(node.textContent).toBe("");
});

it("uses the singular for one organization and disables Check again while checking", async () => {
  let done!: () => void;
  const recheck = vi.fn(() => new Promise<void>((resolve) => (done = resolve)));
  await act(async () =>
    root.render(
      <OrganizationAccessNotice
        access={{ hidden: ["solo"], approval_url: "https://github.com/settings/x" }}
        recheck={recheck}
      />,
    ),
  );
  expect(node.textContent).toContain("This organization restricts OAuth app access");
  const button = node.querySelector("button")!;
  await act(async () => button.click());
  expect(recheck).toHaveBeenCalledOnce();
  expect(button.disabled).toBe(true);
  expect(button.textContent).toBe("Checking…");
  await act(async () => done());
  expect(button.disabled).toBe(false);
});
