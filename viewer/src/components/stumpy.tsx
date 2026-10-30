import { type ReactNode, useEffect, useId, useRef } from "react";

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

const INK = "#1e120a";
const SHINE = "#fff6e0";
const STEM = "#2e7d3e";

export function Stumpy({
  mood = "awake",
  size = 96,
  followPointer = false,
  className,
}: {
  mood?: StumpyMood;
  size?: number;
  /** The eyes glance towards the pointer. Only for `awake`, and held still
   *  for a reader who asks for reduced motion. */
  followPointer?: boolean;
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
      </>
    );
  } else {
    body = (
      <>
        <ellipse cx="80" cy="149" rx="32" ry="4" fill="#2a1d15" opacity="0.18" />
        {sprout}
        {log}
        <g ref={eyes}>
          {eye(72.5, 83.4)}
          {eye(97.2, 83.4)}
        </g>
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
      </defs>
      {body}
    </svg>
  );
}
