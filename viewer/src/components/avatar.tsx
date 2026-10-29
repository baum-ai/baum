import { cn } from "./classnames";
import { initialsFor } from "./format";
import { useState } from "react";

type AvatarProps = {
  /** A real image when there is one. Falls back to initials when absent or
   *  when it fails to load. */
  src?: string | null;
  name: string;
  size?: number;
  shape?: "circle" | "square";
  className?: string;
  /** Fills the initials fallback with this colour instead of the neutral
   *  default, for views where each person needs to read as a consistent
   *  colour rather than a name looked up in prose. Ignored once `src` gives a
   *  real picture. */
  tint?: string;
  /** The initials fallback as the dark rail draws its mark, forest on sand,
   *  for a tile that sits on the rail, where the neutral grey reads stark. The
   *  same in both themes. */
  chrome?: boolean;
};

export function Avatar({
  src,
  name,
  size = 28,
  shape = "circle",
  className,
  tint,
  chrome = false,
}: AvatarProps) {
  const radius = shape === "circle" ? "rounded-full" : "rounded-(--radius-sm)";
  // A picture that fails to load falls back to the initials rather than a
  // broken-image glyph: the name is always known, the picture is a bonus.
  const [failed, setFailed] = useState(false);

  if (src && !failed) {
    return (
      <img
        src={src}
        alt=""
        width={size}
        height={size}
        // GitHub's avatar CDN 404s requests carrying a referrer from an unknown
        // origin, which would leave a broken image where a face should be.
        referrerPolicy="no-referrer"
        onError={() => setFailed(true)}
        className={cn("flex-none object-cover", radius, className)}
        style={{ width: size, height: size }}
      />
    );
  }

  return (
    <span
      aria-hidden
      className={cn(
        "inline-flex flex-none items-center justify-center font-medium",
        !tint &&
          (chrome
            ? "bg-(--brand-sand-100) text-(--brand-forest)"
            : "bg-(--status-neutral-bg) text-(--text-secondary)"),
        radius,
        className,
      )}
      style={{
        width: size,
        height: size,
        fontSize: Math.round(size * 0.36),
        ...(tint
          ? {
              background: `color-mix(in srgb, ${tint} 15%, var(--surface-card))`,
              color: "var(--text-primary)",
            }
          : {}),
      }}
    >
      {initialsFor(name)}
    </span>
  );
}
