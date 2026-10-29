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

/** Deletes every inbox through the daemon's API, from the signed-in page, so a
 *  failed or retried test leaves none for the specs that follow. */
export async function deleteAllInboxes(page: Page): Promise<void> {
  try {
    if (!page.url().includes("/s/")) return;
    await page.evaluate(async () => {
      const read = await fetch("api/inboxes", { cache: "no-store" });
      if (!read.ok) return;
      const { revision, inboxes } = (await read.json()) as { revision: number; inboxes: unknown[] };
      if (inboxes.length === 0) return;
      await fetch("api/inboxes", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ revision, inboxes: [] }),
      });
    });
  } catch {
    // A page that never loaded has made none.
  }
}
