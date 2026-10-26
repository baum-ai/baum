import type { ReactNode } from "react";
import "./nine-dots.css";

// Grid cells in the order green travels around them, clockwise from the top
// left; the centre (4) stays still.
const orbit = [0, 1, 2, 5, 8, 7, 6, 3];

/** The one loader: green passes around a 3×3 grid of brown dots, 150 ms per
 *  dot in a 1.2 s loop, as the brand world draws it. It reads down to 16 px.
 *  Its look lives in nine-dots.css alone; callers choose with props.
 *
 *  - `tone="brand"` (default): brown dots, green passing.
 *  - `tone="text"`: the surrounding text colour, for a busy button or any fill
 *    the brand colours would not read on, spaced to sit before a label. */
export function NineDots({
  size = 16,
  tone = "brand",
  label,
}: {
  size?: number;
  tone?: "brand" | "text";
  label?: string;
}) {
  return (
    <svg
      className={tone === "text" ? "nine-dots nine-dots-text" : "nine-dots"}
      width={size}
      height={size}
      viewBox="0 0 160 160"
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      {Array.from({ length: 9 }, (_, cell) => {
        const step = orbit.indexOf(cell);
        return (
          <circle
            key={cell}
            cx={32 + 48 * (cell % 3)}
            cy={32 + 48 * Math.floor(cell / 3)}
            r={19}
            className={step < 0 ? undefined : `nine-dots-step-${step}`}
          />
        );
      })}
    </svg>
  );
}

/** Something still working, announced as a status. Inline it is one line: the
 *  loader, then the message. As a block it stands in for content on its way:
 *  a larger loader, the message and an optional line saying what is
 *  happening. */
export function LoadingStatus({
  children,
  detail,
  block = false,
  id,
}: {
  children: ReactNode;
  detail?: ReactNode;
  block?: boolean;
  id?: string;
}) {
  return (
    <div
      id={id}
      role="status"
      className={block ? "loading-status loading-block" : "loading-status"}
    >
      <NineDots size={block ? 44 : 16} />
      <span className="loading-message">
        <span className="loading-title">{children}</span>
        {detail ? <span className="loading-detail">{detail}</span> : null}
      </span>
    </div>
  );
}
