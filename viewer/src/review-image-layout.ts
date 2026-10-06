/** How the description images of one block are laid out: rows of one image,
 *  or of two that share a height, each image's display width in CSS pixels. */

export type ImageSize = { width: number; height: number };

/** One row: the images' positions in the block and their display widths. */
export type ImageRow = { index: number; width: number }[];

/** The gap between two images in a row, in CSS pixels (`gap-4`). */
export const IMAGE_GAP = 16;

/** The tallest an image in a section is drawn: most of a short laptop
 *  screen, so a phone screenshot does not need scrolling past. */
export function sectionImageHeight(viewportHeight: number): number {
  return Math.round(Math.min(640, viewportHeight * 0.7));
}

/** Two images share a row only if one is at most this many times wider for
 *  its height than the other; a panorama beside a phone screenshot leaves one
 *  of them a sliver. */
const MAX_ASPECT_SPREAD = 2;

/** Sharing a row may shrink an image to this share of its width alone, or
 *  further while it stays at least `READABLE_WIDTH` wide. Two phone
 *  screenshots then sit side by side in a narrow pane, while two wide ones
 *  only do in a pane wide enough to read them. */
const MIN_PAIRED_SHARE = 2 / 3;
const READABLE_WIDTH = 400;

const aspect = (size: ImageSize) => size.width / size.height;

/** An image alone: the pane's width, at most its own pixels and `maxHeight`
 *  tall. Without a measured pane its CSS box caps it instead. */
function alone(size: ImageSize, paneWidth: number, maxHeight: number) {
  return Math.min(paneWidth, Math.min(maxHeight, size.height) * aspect(size));
}

/** Lays out images in order. Neighbours share a row, at one height, when
 *  their proportions are close and neither shrinks past readability; any
 *  other image takes a row to itself. `paneWidth` is null until measured, and
 *  then every image is alone. */
export function layoutImages(
  sizes: readonly ImageSize[],
  paneWidth: number | null,
  maxHeight: number,
): ImageRow[] {
  const width = paneWidth ?? Number.POSITIVE_INFINITY;
  const rows: ImageRow[] = [];
  for (let index = 0; index < sizes.length; index += 1) {
    const first = sizes[index] as ImageSize;
    const second = sizes[index + 1];
    if (paneWidth !== null && second && pairs(first, second, width, maxHeight)) {
      const height = sharedHeight(first, second, width, maxHeight);
      rows.push([
        { index, width: Math.floor(height * aspect(first)) },
        { index: index + 1, width: Math.floor(height * aspect(second)) },
      ]);
      index += 1;
    } else {
      rows.push([{ index, width: Math.floor(alone(first, width, maxHeight)) }]);
    }
  }
  return rows;
}

function sharedHeight(first: ImageSize, second: ImageSize, paneWidth: number, maxHeight: number) {
  return Math.min(
    (paneWidth - IMAGE_GAP) / (aspect(first) + aspect(second)),
    maxHeight,
    first.height,
    second.height,
  );
}

function pairs(first: ImageSize, second: ImageSize, paneWidth: number, maxHeight: number) {
  const [a, b] = [aspect(first), aspect(second)];
  if (Math.max(a, b) / Math.min(a, b) > MAX_ASPECT_SPREAD) return false;
  const height = sharedHeight(first, second, paneWidth, maxHeight);
  return [first, second].every(
    (size) =>
      height * aspect(size) >=
      Math.min(alone(size, paneWidth, maxHeight) * MIN_PAIRED_SHARE, READABLE_WIDTH),
  );
}

/** The viewer's image size: its own pixels, shrunk to fit the viewport
 *  around the dialog's margin, padding, border and `headerHeight` so it never
 *  needs scrolling. */
export function viewerImageWidth(
  size: ImageSize,
  viewport: { width: number; height: number },
  headerHeight: number,
): number {
  const room = { width: viewport.width - 60, height: viewport.height - 54 - headerHeight };
  return Math.max(1, Math.floor(Math.min(size.width, room.width, room.height * aspect(size))));
}
