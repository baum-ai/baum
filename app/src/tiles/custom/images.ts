import { useEffect, useMemo, useState } from "react";
import { useAttachmentUrl } from "@baum/viewer/host";

/** A tile made in chat can show images the host stored for it: where its
 *  schema has a string, its data holds a reference, `baum-image:att_<id>`,
 *  that the attachment route serves for the pull request (see "Images in a
 *  custom tile" in oss/ARCHITECTURE.md). A frame's policy admits images only
 *  as `data:` URLs and the frame never reaches the daemon, so the page reads
 *  each referenced image itself and hands the frame its data with the image
 *  as a `data:` URL in the reference's place. */

const IMAGE_REF = /^baum-image:(att_[0-9a-f]{32})$/;

/** The stored image ids the data refers to, each once. */
export function imageRefs(data: unknown): string[] {
  const ids = new Set<string>();
  const walk = (value: unknown) => {
    if (typeof value === "string") {
      const id = IMAGE_REF.exec(value)?.[1];
      if (id) ids.add(id);
    } else if (Array.isArray(value)) value.forEach(walk);
    else if (typeof value === "object" && value !== null) Object.values(value).forEach(walk);
  };
  walk(data);
  return [...ids];
}

/** How many characters of `data:` URLs one tile's data gets, counting every
 *  reference (as the daemon's `addedtiles.MaxInlined`): data can repeat a
 *  reference far past the images' own limits. */
export const MAX_INLINED = 24 * 1024 * 1024;

/** The data with each reference replaced by its image's `data:` URL, or by
 *  an empty string when the image could not be read or the URLs before it
 *  already reach `MAX_INLINED`. */
export function withImages(
  data: unknown,
  urls: ReadonlyMap<string, string>,
  limit = MAX_INLINED,
): unknown {
  let total = 0;
  const swap = (value: unknown): unknown => {
    if (typeof value === "string") {
      const id = IMAGE_REF.exec(value)?.[1];
      if (!id) return value;
      const url = urls.get(id) ?? "";
      total += url.length;
      return total > limit ? "" : url;
    }
    if (Array.isArray(value)) return value.map(swap);
    if (typeof value === "object" && value !== null)
      return Object.fromEntries(Object.entries(value).map(([key, each]) => [key, swap(each)]));
    return value;
  };
  return swap(data);
}

/** The images read on this page, by address. A failed read is forgotten, so
 *  the tile's next mount reads it again. */
const read = new Map<string, Promise<string>>();

/** The image at `url` as a `data:` URL; empty when it is not an image. */
export function imageDataURL(url: string, load: typeof fetch = fetch): Promise<string> {
  let answer = read.get(url);
  if (!answer) {
    answer = load(url)
      .then(async (response) => {
        const type = (response.headers.get("content-type") ?? "").split(";")[0]!.trim();
        if (!response.ok || !/^image\/[a-z0-9.+-]+$/i.test(type)) throw new Error("not an image");
        return `data:${type};base64,${base64(new Uint8Array(await response.arrayBuffer()))}`;
      })
      .catch(() => {
        read.delete(url);
        return "";
      });
    read.set(url, answer);
  }
  return answer;
}

function base64(bytes: Uint8Array): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000)
    binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}

/** Forgets the images read, for tests. */
export function forgetImages() {
  read.clear();
}

/** The tile's data as its frame gets it: with its stored images inlined.
 *  `pending` while they are read; data without references is as it is. */
export function useInlinedImages(data: unknown): { data: unknown; pending: boolean } {
  const resolve = useAttachmentUrl();
  const ids = useMemo(() => imageRefs(data), [data]);
  const [loaded, setLoaded] = useState<{ from: unknown; data: unknown } | null>(null);
  useEffect(() => {
    if (ids.length === 0 || !resolve) return;
    let live = true;
    void Promise.all(ids.map((id) => imageDataURL(resolve(id)))).then((urls) => {
      if (live)
        setLoaded({
          from: data,
          data: withImages(data, new Map(ids.map((id, i) => [id, urls[i]!]))),
        });
    });
    return () => {
      live = false;
    };
  }, [ids, data, resolve]);
  // Without the attachment route, as in a preview, the images draw nothing.
  const without = useMemo(
    () => (ids.length > 0 && !resolve ? withImages(data, new Map()) : data),
    [ids, resolve, data],
  );
  if (ids.length === 0 || !resolve) return { data: without, pending: false };
  if (!loaded || loaded.from !== data) return { data, pending: true };
  return { data: loaded.data, pending: false };
}
