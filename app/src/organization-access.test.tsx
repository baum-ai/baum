import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { OrganizationAccessNotice } from "./organization-access";

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

const approval = "https://github.com/settings/connections/applications/x";
const toggle = () => node.querySelector<HTMLButtonElement>(".organization-access-toggle")!;

it("stays one collapsed line, then lists every organization with a request link", async () => {
  const hidden = ["acme", "beta", "gamma", "delta", "epsilon"];
  await act(async () =>
    root.render(
      <OrganizationAccessNotice access={{ hidden, approval_url: approval }} recheck={vi.fn()} />,
    ),
  );
  expect(node.textContent).toBe("baum cannot see repositories in 5 of your organizations.");
  expect(toggle().getAttribute("aria-expanded")).toBe("false");
  expect(node.querySelector("a")).toBeNull();
  await act(async () => toggle().click());
  expect(toggle().getAttribute("aria-expanded")).toBe("true");
  expect(document.getElementById(toggle().getAttribute("aria-controls")!)).not.toBeNull();
  expect(node.textContent).toContain("These organizations restrict OAuth app access");
  const rows = [...node.querySelectorAll(".organization-access-list li")];
  expect(rows.map((row) => row.querySelector(".organization-access-name")!.textContent)).toEqual(
    hidden,
  );
  for (const [i, row] of rows.entries()) {
    const link = row.querySelector("a")!;
    expect(link.getAttribute("href")).toBe(approval);
    expect(link.getAttribute("aria-label")).toBe(`Request access to ${hidden[i]} on GitHub`);
  }
  await act(async () => toggle().click());
  expect(node.querySelector(".organization-access-list")).toBeNull();
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
  expect(node.textContent).toBe("baum cannot see repositories in 1 of your organizations.");
  await act(async () => toggle().click());
  expect(node.textContent).toContain("This organization restricts OAuth app access");
  const button = [...node.querySelectorAll("button")].find((b) => b.textContent === "Check again")!;
  await act(async () => button.click());
  expect(recheck).toHaveBeenCalledOnce();
  expect(button.disabled).toBe(true);
  expect(button.textContent).toBe("Checking…");
  await act(async () => done());
  expect(button.disabled).toBe(false);
});
