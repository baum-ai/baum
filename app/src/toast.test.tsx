import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ToastProvider, useToast, type Toast } from "./toast";

let node: HTMLDivElement;
let root: Root;
let show: (toast: Toast) => void;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  vi.useFakeTimers();
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.useRealTimers();
});

function Raise() {
  show = useToast();
  return null;
}

async function render() {
  await act(async () =>
    root.render(
      <ToastProvider>
        <Raise />
      </ToastProvider>,
    ),
  );
}
const toast = () => node.querySelector('[aria-live="polite"] .local-toast');

it("shows one toast at a time, in a live region, gone after a few seconds", async () => {
  await render();
  expect(toast()).toBeNull();
  await act(async () =>
    show({
      title: "Answered “Not yet”",
      detail: "Push it and open a PR?",
      context: "Session page background colors",
      icon: { src: "/claude.svg", alt: "Claude Code" },
    }),
  );
  expect(toast()!.querySelector(".local-toast-title")!.textContent).toBe("Answered “Not yet”");
  expect(toast()!.querySelector(".local-toast-detail")!.textContent).toBe("Push it and open a PR?");
  expect(toast()!.querySelector(".local-toast-context")!.textContent).toBe(
    "Session page background colors",
  );
  // A newer one replaces it, and stays its own full time.
  await act(async () => vi.advanceTimersByTime(5000));
  await act(async () => show({ title: "Could not end the session", error: true }));
  expect(toast()!.classList.contains("error")).toBe(true);
  await act(async () => vi.advanceTimersByTime(5000));
  expect(toast()).not.toBeNull();
  await act(async () => vi.advanceTimersByTime(1000));
  expect(toast()).toBeNull();
});

it("runs its action and goes, or goes when dismissed", async () => {
  await render();
  const undo = vi.fn();
  await act(async () => show({ title: "Deleted the inbox", action: { label: "Undo", run: undo } }));
  await act(async () => toast()!.querySelector<HTMLButtonElement>(".local-toast-action")!.click());
  expect(undo).toHaveBeenCalledOnce();
  expect(toast()).toBeNull();
  await act(async () => show({ title: "Replied" }));
  await act(async () =>
    toast()!.querySelector<HTMLButtonElement>('button[aria-label="Dismiss"]')!.click(),
  );
  expect(toast()).toBeNull();
});
