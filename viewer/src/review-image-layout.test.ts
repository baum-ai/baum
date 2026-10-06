import { describe, expect, it } from "vitest";

import {
  IMAGE_GAP,
  layoutImages,
  sectionImageHeight,
  viewerImageWidth,
} from "./review-image-layout";

// Sizes of real description screenshots.
const phone = { width: 1170, height: 2532 };
const retina = { width: 2880, height: 1800 };
const tall = { width: 800, height: 1350 };
const banner = { width: 3000, height: 400 };
const tiny = { width: 120, height: 40 };

const widths = (rows: ReturnType<typeof layoutImages>) =>
  rows.map((row) => row.map(({ index, width }) => [index, width]));

describe("layoutImages", () => {
  it("draws every image alone at its capped size before the pane is measured", () => {
    expect(widths(layoutImages([phone, retina], null, 640))).toEqual([
      [[0, Math.floor(640 * (1170 / 2532))]],
      [[1, Math.floor(640 * 1.6)]],
    ]);
  });

  it("pairs landscape screenshots only when the pane leaves each readable", () => {
    expect(layoutImages([retina, retina], 620, 640)).toHaveLength(2);
    const [row] = layoutImages([retina, retina], 1240, 640);
    expect(row?.map(({ width }) => width)).toEqual([612, 612]);
  });

  it("pairs portrait screenshots at one height when that costs them nothing", () => {
    const [row, ...rest] = layoutImages([tall, tall], 620, 640);
    expect(rest).toEqual([]);
    const height = (620 - IMAGE_GAP) / (2 * (800 / 1350));
    expect(row?.map(({ width }) => width)).toEqual([
      Math.floor(Math.min(height, 640) * (800 / 1350)),
      Math.floor(Math.min(height, 640) * (800 / 1350)),
    ]);
  });

  it("never pairs images of very different proportions", () => {
    expect(widths(layoutImages([phone, retina], 1600, 640))).toEqual([
      [[0, Math.floor(640 * (1170 / 2532))]],
      [[1, 1024]],
    ]);
    expect(layoutImages([retina, banner], 1600, 640)).toHaveLength(2);
  });

  it("keeps a small image at its own size, alone or paired", () => {
    expect(widths(layoutImages([tiny], 620, 640))).toEqual([[[0, 120]]]);
    expect(widths(layoutImages([tiny, tiny], 620, 640))).toEqual([
      [
        [0, 120],
        [1, 120],
      ],
    ]);
  });

  it("pairs greedily in order and leaves an odd one alone", () => {
    expect(
      layoutImages([tall, tall, tall], 620, 640).map((row) => row.map(({ index }) => index)),
    ).toEqual([[0, 1], [2]]);
  });
});

describe("image heights", () => {
  it("caps section images below the viewport height", () => {
    expect(sectionImageHeight(800)).toBe(560);
    expect(sectionImageHeight(1200)).toBe(640);
  });

  it("fits the viewer image below its header without enlarging it", () => {
    expect(viewerImageWidth(phone, { width: 390, height: 844 }, 120)).toBe(
      Math.floor((844 - 54 - 120) * (1170 / 2532)),
    );
    expect(viewerImageWidth(phone, { width: 1440, height: 900 }, 58)).toBe(
      Math.floor((900 - 112) * (1170 / 2532)),
    );
    expect(viewerImageWidth(retina, { width: 1440, height: 900 }, 58)).toBe(
      Math.floor((900 - 112) * 1.6),
    );
    expect(viewerImageWidth(retina, { width: 390, height: 844 }, 58)).toBe(330);
    expect(viewerImageWidth(tiny, { width: 1440, height: 900 }, 58)).toBe(120);
  });
});
