import { type ReactNode, type RefObject, useEffect, useId, useRef } from "react";

/** Stumpy, the log from the logo with a leaf and a face. The hosted
 *  application keeps the plain b as its mark, and there Stumpy only turns up
 *  at the edges of the work — nothing waiting, a missing page, a failure — and
 *  at most once per screen. Never beside a loader or in the middle of a task.
 *  The desktop app wears Stumpy as its mark: in its rail, as its app icon and
 *  as its tray icon.
 *
 *  - `awake`  eyes open, leaf up: the public pages.
 *  - `dozing` eyes shut, a couple of z's: nothing left to do.
 *  - `pruned` the leaf has dropped and Stumpy looks down at it: not found.
 *  - `fallen` tipped on its side, wincing: something broke.
 *
 *  Decorative: the copy beside it says what happened. */
export type StumpyMood = "awake" | "dozing" | "pruned" | "fallen";

/** Something seasonal Stumpy wears `awake`, knocked off onto the ground
 *  when `fallen`: sunglasses for summer, a witch's hat for autumn, a Santa
 *  hat for winter. */
export type StumpyAccessory = "sunglasses" | "witch" | "santa";

const INK = "#1e120a";
const SHINE = "#fff6e0";
const STEM = "#2e7d3e";

export function Stumpy({
  mood = "awake",
  size = 96,
  followPointer = false,
  eyesRef,
  accessory,
  className,
}: {
  mood?: StumpyMood;
  size?: number;
  /** The eyes glance towards the pointer. Only for `awake`, and held still
   *  for a reader who asks for reduced motion. */
  followPointer?: boolean;
  /** Receives the `awake` eyes, for a caller that points them itself: a
   *  `transform` of up to 4 units each way glances that way. */
  eyesRef?: RefObject<SVGGElement | null>;
  accessory?: StumpyAccessory;
  className?: string;
}) {
  const id = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  const eyes = useRef<SVGGElement>(null);
  const follow = followPointer && mood === "awake";

  useEffect(() => {
    const group = eyes.current;
    if (!follow || !group) return;
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    let frame = 0;
    const move = (event: PointerEvent) => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const box = group.ownerSVGElement?.getBoundingClientRect();
        if (!box) return;
        const dx = event.clientX - (box.left + box.width / 2);
        const dy = event.clientY - (box.top + box.height * 0.55);
        const distance = Math.hypot(dx, dy) || 1;
        const reach = Math.min(1, distance / 240);
        group.setAttribute(
          "transform",
          `translate(${((dx / distance) * 4 * reach).toFixed(2)} ${((dy / distance) * 4 * reach).toFixed(2)})`,
        );
      });
    };
    window.addEventListener("pointermove", move, { passive: true });
    return () => {
      cancelAnimationFrame(frame);
      window.removeEventListener("pointermove", move);
    };
  }, [follow]);

  const bark = `url(#${id}-bark)`;
  const face = `url(#${id}-face)`;
  const leaf = `url(#${id}-leaf)`;

  const log = (
    <>
      <circle cx="80" cy="94" r="44" fill={bark} />
      <circle cx="80" cy="94" r="35.2" fill={face} />
      <circle
        cx="80"
        cy="94"
        r="35.2"
        fill="none"
        stroke="#4e2e16"
        strokeOpacity="0.35"
        strokeWidth="2"
      />
    </>
  );
  const sprout = (
    <g transform="translate(80 51) scale(1.3) translate(-80 -51)">
      <path
        d="M80 51 C80 43 81 37 83 31"
        fill="none"
        stroke={STEM}
        strokeWidth="4"
        strokeLinecap="round"
      />
      <path d="M83 33 C87 21 102 21 101 29 C100 36 89 38 83 33 Z" fill={leaf} />
      <path
        d="M83 33 Q92 28.5 101 29"
        fill="none"
        stroke={STEM}
        strokeWidth="0.7"
        strokeLinecap="round"
      />
    </g>
  );
  const eye = (x: number, y: number) => (
    <>
      <ellipse cx={x} cy={y} rx="5.7" ry="9.9" fill={INK} />
      <circle cx={x + 2.1} cy={y - 5} r="1.7" fill={SHINE} />
    </>
  );

  // The hat's shapes are drawn about its brim at (72, 50).
  const wear = (place: string) =>
    accessory === "witch" ? (
      <g transform={`${place} translate(72 50) scale(1.18) translate(-72 -50)`}>
        <path d="M51 49 C58 36 60 23 49 8 C65 14 77 30 93 49 Z" fill={`url(#${id}-witch)`} />
        <path d="M53.5 41 L87 41 L91 46 L51.5 46 Z" fill="#e9a23b" />
        <rect
          x="66"
          y="40.5"
          width="9"
          height="6"
          rx="1"
          fill="none"
          stroke="#f6d27a"
          strokeWidth="1.6"
        />
        <ellipse cx="72" cy="50" rx="33" ry="6" fill={`url(#${id}-witch)`} />
      </g>
    ) : accessory === "santa" ? (
      <g transform={`${place} translate(72 50) scale(1.12) translate(-72 -50)`}>
        <path
          d="M46 50 C47 42 49 37 52 34 C46 34 40 37 35 41 C38 29 48 17 66 13 C84 10 96 26 98 50 Z"
          fill={`url(#${id}-santa)`}
        />
        <rect x="42" y="44" width="60" height="11" rx="5.5" fill="#fffaf0" stroke="#e6dac2" />
        <circle cx="34" cy="43" r="6" fill="#fffaf0" stroke="#e6dac2" />
      </g>
    ) : null;
  // The sunglasses are drawn over the eyes, about their bridge at (85, 83).
  const lens = (cx: number) => (
    <>
      <path
        d={`M${cx - 11} 77 H${cx + 11} Q${cx + 11.5} 87 ${cx + 5} 90.5 Q${cx} 92 ${cx - 5} 90.5 Q${cx - 11.5} 87 ${cx - 11} 77 Z`}
        fill={`url(#${id}-shades)`}
        stroke={INK}
        strokeWidth="1.4"
        strokeLinejoin="round"
      />
      <path
        d={`M${cx - 6} 80 L${cx - 1} 80`}
        stroke="#fff"
        strokeOpacity="0.55"
        strokeWidth="1.6"
        strokeLinecap="round"
      />
    </>
  );
  const shades = (place: string) => (
    <g transform={place}>
      <g fill="none" stroke={INK} strokeWidth="2" strokeLinecap="round">
        <path d="M83.5 79 Q84.85 76.5 86.2 79" />
        <path d="M61.5 78 L49 76" />
        <path d="M108.2 78 L111 76" />
      </g>
      {lens(72.5)}
      {lens(97.2)}
    </g>
  );
  // Hats sit on the crown of the log, tipped a little to the left so the
  // leaf still peeks out to the right. Whatever Stumpy wears lands on the
  // ground in front when it falls.
  const worn = accessory === "sunglasses" ? shades("") : wear("translate(0 3) rotate(-10 72 50)");
  const knockedOff =
    accessory === "sunglasses"
      ? shades("translate(122 140) rotate(-8) scale(0.6) translate(-85 -83)")
      : wear("translate(54 88) rotate(14 72 50) translate(72 50) scale(0.55) translate(-72 -50)");

  let body: ReactNode;
  if (mood === "dozing") {
    body = (
      <>
        {sprout}
        {log}
        <g fill="none" stroke={INK} strokeWidth="2.6" strokeLinecap="round">
          <path d="M62.5 88 Q68 93.5 73.5 88" />
          <path d="M86.5 88 Q92 93.5 97.5 88" />
        </g>
        <g fontFamily="var(--font-sans)" fontWeight="700">
          <text x="118" y="64" fontSize="14" fill="#7a4a26">
            z
          </text>
          <text x="128" y="52" fontSize="10" fill="#a86b3c">
            z
          </text>
        </g>
      </>
    );
  } else if (mood === "pruned") {
    body = (
      <>
        <g transform="translate(80 51) scale(1.3) translate(-80 -51)">
          <path
            d="M80 51 C80 46 80.5 43 81.5 40"
            fill="none"
            stroke={STEM}
            strokeWidth="4"
            strokeLinecap="round"
          />
        </g>
        {log}
        {eye(72.7, 98)}
        {eye(97.3, 98)}
        <g transform="translate(118 132) rotate(-160) scale(1.1)">
          <path d="M3 -18 C7 -30 22 -30 21 -22 C20 -15 9 -13 3 -18 Z" fill={leaf} />
        </g>
      </>
    );
  } else if (mood === "fallen") {
    body = (
      <>
        <ellipse cx="84" cy="146" rx="46" ry="4" fill="#2a1d15" opacity="0.16" />
        <g transform="translate(6 9) rotate(-72 80 94)">
          {sprout}
          {log}
          <g fill="none" stroke={INK} strokeWidth="3" strokeLinecap="round" strokeLinejoin="round">
            <path d="M66 80 L76 86 L66 92" />
            <path d="M101 80 L91 86 L101 92" />
          </g>
        </g>
        {knockedOff}
      </>
    );
  } else {
    body = (
      <>
        <ellipse cx="80" cy="149" rx="32" ry="4" fill="#2a1d15" opacity="0.18" />
        {sprout}
        {log}
        <g
          ref={(group) => {
            eyes.current = group;
            if (eyesRef) eyesRef.current = group;
          }}
        >
          {/* Glancing eyes would peek out from behind sunglasses. */}
          {accessory === "sunglasses" ? null : (
            <>
              {eye(72.5, 83.4)}
              {eye(97.2, 83.4)}
            </>
          )}
        </g>
        {worn}
      </>
    );
  }

  return (
    <svg
      width={size}
      height={size}
      viewBox="4 4 152 152"
      aria-hidden="true"
      focusable="false"
      className={className}
    >
      <defs>
        <radialGradient id={`${id}-bark`} cx="0.3" cy="0.25" r="0.85">
          <stop offset="0" stopColor="#a86b3c" />
          <stop offset="0.6" stopColor="#7a4a26" />
          <stop offset="1" stopColor="#4e2e16" />
        </radialGradient>
        <radialGradient id={`${id}-face`} cx="0.4" cy="0.35" r="0.75">
          <stop offset="0" stopColor="#fff3da" />
          <stop offset="0.7" stopColor="#f3d9ac" />
          <stop offset="1" stopColor="#d9b27c" />
        </radialGradient>
        <linearGradient id={`${id}-leaf`} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#8ed07a" />
          <stop offset="1" stopColor={STEM} />
        </linearGradient>
        {accessory === "sunglasses" ? (
          <linearGradient id={`${id}-shades`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stopColor="#3a3f4d" />
            <stop offset="1" stopColor="#0e1016" />
          </linearGradient>
        ) : null}
        {accessory === "witch" ? (
          <linearGradient id={`${id}-witch`} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0" stopColor="#4a3366" />
            <stop offset="1" stopColor="#1f1430" />
          </linearGradient>
        ) : null}
        {accessory === "santa" ? (
          <linearGradient id={`${id}-santa`} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0" stopColor="#e2554a" />
            <stop offset="1" stopColor="#a52a22" />
          </linearGradient>
        ) : null}
      </defs>
      {body}
    </svg>
  );
}
