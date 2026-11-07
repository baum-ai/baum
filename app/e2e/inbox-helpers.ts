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

/** Leaves exactly one inbox, the empty-query Default, through the daemon's
 *  API, from the signed-in page, so a failed or retried test leaves the
 *  fixture as a fresh installation is: the daemon refuses an empty list, and
 *  the app opens the first inbox. The first inbox keeps its id and becomes the
 *  Default; every other goes. */
export async function resetInboxes(page: Page): Promise<void> {
  try {
    if (!page.url().includes("/s/")) return;
    await page.evaluate(async () => {
      const read = await fetch("api/inboxes", { cache: "no-store" });
      if (!read.ok) return;
      const { revision, inboxes } = (await read.json()) as {
        revision: number;
        inboxes: Record<string, unknown>[];
      };
      const first = inboxes[0];
      if (inboxes.length === 1 && first?.name === "Default" && first.query === "" && first.badge)
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
              query: "",
              badge: true,
            },
          ],
        }),
      });
    });
  } catch {
    // A page that never loaded has made none.
  }
}
