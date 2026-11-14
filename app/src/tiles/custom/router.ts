import { RateGuard } from "./protocol";

/** One `message` listener for every tile frame on the page. A frame is known
 *  by its window (`iframe.contentWindow`, which keeps its identity across the
 *  frame's navigations), registered before the frame is given its address so
 *  its first message is never missed. A message from any other window, or
 *  whose origin is not the opaque `"null"` a sandboxed frame has, is dropped
 *  before anything reads it; so a frame cannot speak for another tile, and a
 *  frame that somehow had an origin (a sandbox without its flags) is not
 *  heard at all. A frame's messages are counted (`RateGuard`) before their
 *  data is touched: past the rate they are dropped unread, and a frame that
 *  floods is reported once. */

export type FrameListener = {
  heard: (data: unknown) => void;
  flooded: () => void;
};

type Entry = { listener: FrameListener; rate: RateGuard; reported: boolean };

const frames = new Map<MessageEventSource, Entry>();
let listening = false;

function route(event: MessageEvent) {
  if (event.source === null) return;
  const entry = frames.get(event.source);
  if (!entry || event.origin !== "null") return;
  const admission = entry.rate.admit(performance.now());
  if (admission === "flood") {
    if (!entry.reported) {
      entry.reported = true;
      entry.listener.flooded();
    }
    return;
  }
  if (admission === "drop") return;
  entry.listener.heard(event.data);
}

/** Hears `source`'s messages until the returned function is called. */
export function listenToFrame(source: MessageEventSource, listener: FrameListener): () => void {
  const entry: Entry = { listener, rate: new RateGuard(), reported: false };
  frames.set(source, entry);
  if (!listening) {
    window.addEventListener("message", route);
    listening = true;
  }
  return () => {
    if (frames.get(source) === entry) frames.delete(source);
    if (frames.size === 0 && listening) {
      window.removeEventListener("message", route);
      listening = false;
    }
  };
}

/** The frames heard now, for tests. */
export function listenedFrames(): number {
  return frames.size;
}
