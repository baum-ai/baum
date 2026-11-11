import { lazy, Suspense, type ComponentProps } from "react";

// The tiles and their precompiled validators are large and render only with
// `?tiles=1`, so they load when first shown rather than with the viewer.
const Tiles = lazy(() => import("./review-tiles").then((m) => ({ default: m.ReviewTiles })));

export function ReviewTiles(props: ComponentProps<typeof Tiles>) {
  return (
    <Suspense fallback={null}>
      <Tiles {...props} />
    </Suspense>
  );
}
