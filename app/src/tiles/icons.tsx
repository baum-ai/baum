import type { ReactNode } from "react";
import type { IconProps } from "@baum/viewer/components/icons";

/** Glyphs for the tile types the viewer's set has none for, drawn on its grid
 *  (16 units, a 1.5 stroke, `currentColor`). */
function Glyph({ size = 16, className, children }: IconProps & { children: ReactNode }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.5}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      className={className}
    >
      {children}
    </svg>
  );
}

export function ImageIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <rect x="2" y="3" width="12" height="10" rx="1.5" />
      <circle cx="6" cy="6.5" r="1.2" />
      <path d="m2.5 12 3.5-3.5 2.5 2.5 2-2 3 3" />
    </Glyph>
  );
}

export function QuoteIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M3 4.5h10M3 8h7M3 11.5h4" />
      <path d="M12.5 9.5v3l-1.5-1" />
    </Glyph>
  );
}

export function MapIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="m2 4 4-1.5 4 1.5 4-1.5v9.5L10 13.5 6 12 2 13.5Z" />
      <path d="M6 2.5V12M10 4v9.5" />
    </Glyph>
  );
}

export function LayersIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="m8 2.5 6 3-6 3-6-3Z" />
      <path d="m2 8.5 6 3 6-3M2 11.5l6 3 6-3" />
    </Glyph>
  );
}

export function CodeIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="m5.5 4.5-3.5 3.5 3.5 3.5M10.5 4.5 14 8l-3.5 3.5" />
    </Glyph>
  );
}

export function TableIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <rect x="2" y="3" width="12" height="10" rx="1.5" />
      <path d="M2 6.5h12M6.5 6.5V13" />
    </Glyph>
  );
}

export function DiagramIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <rect x="2" y="2.5" width="5" height="4" rx="1" />
      <rect x="9" y="9.5" width="5" height="4" rx="1" />
      <path d="M4.5 6.5v3.5a1.5 1.5 0 0 0 1.5 1.5h3" />
    </Glyph>
  );
}

export function GridIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <rect x="2" y="2" width="5" height="5" rx="1" />
      <rect x="9" y="2" width="5" height="5" rx="1" />
      <rect x="2" y="9" width="5" height="5" rx="1" />
      <rect x="9" y="9" width="5" height="5" rx="1" />
    </Glyph>
  );
}
