/** One `message` listener for every tile frame on the page. A frame is known
 *  by its window (`iframe.contentWindow`, which keeps its identity across the
 *  frame's navigations), registered before the frame is given its address so
 *  its first message is never missed. A message from any other window, or
 *  whose origin is not the opaque `"null"` a sandboxed frame has, is dropped
 *  before anything reads it; so a frame cannot speak for another tile, and a
 *  frame that somehow had an origin (a sandbox without its flags) is not
 *  heard at all. */

export type FrameListener = (data: unknown) => void;

const frames = new Map<MessageEventSource, FrameListener>();
let listening = false;

function route(event: MessageEvent) {
  if (event.source === null) return;
  const listener = frames.get(event.source);
  if (!listener || event.origin !== "null") return;
  listener(event.data);
}

/** Hears `source`'s messages until the returned function is called. */
export function listenToFrame(source: MessageEventSource, listener: FrameListener): () => void {
  frames.set(source, listener);
  if (!listening) {
    window.addEventListener("message", route);
    listening = true;
  }
  return () => {
    if (frames.get(source) === listener) frames.delete(source);
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
