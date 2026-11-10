import { readFile } from "node:fs/promises";
import { join } from "node:path";
import type { Page } from "@playwright/test";

// The fixture daemon publishes its launch URL in a private credential file.
export async function launchURL(): Promise<string> {
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      const path = join(process.env.BAUM_APP_STATE_DIR ?? "", "credential.json");
      const { url } = JSON.parse(await readFile(path, "utf8"));
      if (typeof url === "string") return url;
    } catch {
      // Not written yet.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error("the daemon did not publish its credential");
}

/** The Default inbox's query, as a fresh installation gets it. */
export const FOR_ME = "author=@me OR assignee=@me OR review-requested=@me";

/** Leaves exactly one inbox, the Default with its query, through the daemon's
 *  API, from the signed-in page, so a failed or retried test leaves the
 *  fixture as a fresh installation is: the daemon refuses an empty list, and
 *  the app opens the first inbox. The first inbox keeps its id and becomes the
 *  Default; every other goes. */
export async function resetInboxes(page: Page): Promise<void> {
  try {
    if (!page.url().includes("/s/")) return;
    await page.evaluate(async (query) => {
      const read = await fetch("api/inboxes", { cache: "no-store" });
      if (!read.ok) return;
      const { revision, inboxes } = (await read.json()) as {
        revision: number;
        inboxes: Record<string, unknown>[];
      };
      const first = inboxes[0];
      if (inboxes.length === 1 && first?.name === "Default" && first.query === query && first.badge)
        return;
      const base = first ?? {
        id: "00000000000000000000000000000001",
        color: "blue",
      };
      await fetch("api/inboxes", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          revision,
          inboxes: [
            {
              ...base,
              name: "Default",
              what: "any",
              owners: [],
              risk: "any",
              repositories: [],
              no_bots: false,
              query,
              badge: true,
            },
          ],
        }),
      });
    }, FOR_ME);
  } catch {
    // A page that never loaded has made none.
  }
}

/** Clears the inbox's snoozes through the daemon, so a run that failed with a
 *  pull request snoozed leaves the specs that follow their whole queue. */
export async function clearSnoozes(page: Page): Promise<void> {
  try {
    if (!page.url().includes("/s/")) return;
    await page.evaluate(async () => {
      const read = await fetch("api/ui-state/inbox", { cache: "no-store" });
      if (!read.ok) return;
      const { revision, value } = (await read.json()) as {
        revision: number;
        value: Record<string, unknown> | null;
      };
      if (!value) return;
      await fetch("api/ui-state/inbox", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          revision,
          mutation_id: `${crypto.randomUUID()}:1`,
          value: { ...value, snoozed: {}, held: {} },
        }),
      });
    });
  } catch {
    // A page that never loaded set none.
  }
}
