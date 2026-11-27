import { useEffect, useState } from "react";
import { appURL } from "../../api";
import type { Tile, TileContext, TileSize } from "../types";
import { preflightHere } from "./check-host";
import { isBuiltinType } from "./library";
import { resolveRefs } from "./refs";

/** The first-show preflight: before the page mounts the frame of a custom
 *  tile that is not of a built-in type, the daemon draws that tile's data
 *  with its type once in the check window, unless it already has (it keeps
 *  each pair of definition and data). A tile that drew, or failed in a way
 *  the frame's own failure faces show, mounts; one that hung stays down,
 *  and the daemon refuses its type until it restarts. Only where the check
 *  window exists: elsewhere a hang would freeze the page that asked. */

export type Preflight = "checking" | "show" | "hung";

/** What the page wrote when the tile hung. */
export const HUNG_WORDS = "This tile stopped responding when it was checked.";

/** Each pair's answer on this page, so a tile drawn again (scrolled back to,
 *  its detail opened) does not ask again. */
const answers = new Map<string, Promise<"show" | "hung">>();

/** The page's preflights, one after another. Each request stays open until
 *  its tile is drawn, and the check window shares this page's connections to
 *  the daemon (Chromium opens at most 6 to one host): a page with several
 *  custom tiles asking at once would hold them all, and the check window
 *  could not claim the jobs it waits on. The daemon draws one at a time
 *  anyway. */
let line: Promise<unknown> = Promise.resolve();

function ask(
  tile: Tile,
  ctx: TileContext,
  size: TileSize,
  digest: string,
): Promise<"show" | "hung"> {
  const key = `${digest}\n${JSON.stringify(tile.data)}`;
  let answer = answers.get(key);
  if (!answer) {
    const labels = ctx.labels;
    const body = JSON.stringify({
      digest: `sha256:${digest}`,
      data: tile.data,
      size,
      refs: {
        glance: resolveRefs(tile.data, ctx.source, "glance", labels).refs,
        detail: resolveRefs(tile.data, ctx.source, "detail", labels).refs,
      },
    });
    answer = line
      .then(() =>
        fetch(appURL("api/tile-checks/preflight"), {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body,
        }),
      )
      .then((response) => (response.ok ? response.json() : null))
      .then((body: { verdict?: unknown } | null) => (body?.verdict === "hang" ? "hung" : "show"))
      // Whatever went wrong asking, the tile is shown: its frame's own
      // guards still apply.
      .catch(() => "show" as const);
    line = answer;
    answers.set(key, answer);
  }
  return answer;
}

/** Whether the tile's frame may mount yet. */
export function usePreflight(
  tile: Tile,
  ctx: TileContext,
  size: TileSize,
  digest: string | null,
  enabled = preflightHere(),
): Preflight {
  const wanted = enabled && digest !== null && tile.type === "custom" && !isBuiltinType(tile);
  const [state, setState] = useState<Preflight>(wanted ? "checking" : "show");
  useEffect(() => {
    if (!wanted || !digest) {
      setState("show");
      return;
    }
    let live = true;
    setState("checking");
    void ask(tile, ctx, size, digest).then((answer) => {
      if (live) setState(answer);
    });
    return () => {
      live = false;
    };
    // The pair is what is asked about: the digest and the data.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wanted, digest, tile.data]);
  return state;
}

/** Forgets the answers, for tests. */
export function forgetPreflights() {
  answers.clear();
  line = Promise.resolve();
}
