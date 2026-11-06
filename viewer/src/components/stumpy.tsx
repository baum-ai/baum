import { type ReactNode, type RefObject, useEffect, useId, useRef } from "react";
import { STUMPY_ARTWORK as artwork } from "./stumpy-artwork";

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

/** Something Stumpy wears `awake`, knocked off onto the ground when `fallen`.
 *  Seasonal: sunglasses for summer, a witch's hat for autumn, a Santa hat for
 *  winter. Historical, one per era: an `eggshell` cap (dinosaurs), the
 *  pharaoh's `nemes` (Egypt), a `laurel` wreath (Rome), a gold `crown`
 *  (medieval), a painter's `beret` (Renaissance), a `phrygian` liberty cap
 *  (the Revolution), a `tophat` (industry), a glass space `helmet` (the Moon),
 *  a small `cosmonaut` helmet (Venus) and a neon `visor`, a flower `garland` (Babylon's Hanging
 *  Gardens), a samurai `kabuto` (feudal Japan), a backwards `cap` (the late
 *  1990s) and a robot's `antennae` (the age of AI), a fur `furhood` (the Ice
 *  Age), a Viking `horned` helmet, a Maya `quetzal` feather headdress and a
 *  Stone Age `bone` in a leopard-fur headband. */
export type StumpyAccessory =
  | "sunglasses"
  | "witch"
  | "santa"
  | "eggshell"
  | "nemes"
  | "laurel"
  | "crown"
  | "beret"
  | "phrygian"
  | "tophat"
  | "helmet"
  | "cosmonaut"
  | "visor"
  | "garland"
  | "kabuto"
  | "cap"
  | "antennae"
  | "furhood"
  | "horned"
  | "quetzal"
  | "bone";

/** The laurel wreath: leaf pairs along the top arc of the log, pointing
 *  towards the crown, around (80, 94). */
const LAUREL_RADIUS = 41;
const LAUREL_LEAVES = Array.from({ length: 9 }, (_, i) => {
  const angle = -160 + i * (140 / 8);
  const rad = (angle * Math.PI) / 180;
  const heading = i < 5 ? angle + 90 : angle - 90;
  return {
    x: 80 + LAUREL_RADIUS * Math.cos(rad),
    y: 94 + LAUREL_RADIUS * Math.sin(rad),
    heading,
  };
});
/** The garland: leaves and blossoms along a shallow arc across the crown, in
 *  brim coordinates. Larger blossoms ride higher in the middle. */
const GARLAND_X = [44, 51, 58, 65, 72, 79, 86, 93, 100];
const garlandY = (x: number) => 57 - 5 * ((x - 72) / 28) ** 2;
const GARLAND_BLOSSOMS: [number, number, number, "white" | "pink" | "yellow"][] = [
  [47, garlandY(47) - 1, 3.8, "pink"],
  [56, garlandY(56) - 3, 4.4, "white"],
  [65, garlandY(65) - 4, 4, "yellow"],
  [74, garlandY(74) - 5, 4.8, "pink"],
  [83, garlandY(83) - 4, 4, "white"],
  [91, garlandY(91) - 3, 4.4, "yellow"],
  [98, garlandY(98) - 1, 3.8, "white"],
];
/** The kabuto's neck-guard plates, layered outwards and downwards. */
const KABUTO_PLATES = [
  { x1: 40, x2: 104, y1: 49, y2: 57 },
  { x1: 34, x2: 110, y1: 56, y2: 63 },
  { x1: 29, x2: 115, y1: 62, y2: 69 },
];
/** Quetzal feathers fan out from the band at (62, 46): angle left of
 *  straight up, length and colour. */
const QUETZAL_FEATHERS = [
  { angle: -74, length: 36, tone: "green" },
  { angle: -56, length: 41, tone: "red" },
  { angle: -40, length: 44, tone: "turquoise" },
  { angle: -24, length: 46, tone: "green" },
  { angle: -9, length: 42, tone: "red" },
  { angle: 6, length: 36, tone: "turquoise" },
] as const;
const EGG_SPECKS: [number, number, number][] = [
  [58, 38, 1.6],
  [66, 31, 1.2],
  [75, 36, 1.8],
  [84, 30, 1.3],
  [90, 40, 1.5],
  [62, 44, 1.2],
  [80, 43, 1.1],
];

const INK = artwork.ink;
const SHINE = artwork.shine;
const STEM = artwork.stem;

/** A knocked-off hat, tipped and shrunk about its brim at (72, 50). */
const aboutBrim = (x: number, y: number, scale: number) =>
  `translate(${x} ${y}) rotate(14 72 50) translate(72 50) scale(${scale}) translate(-72 -50)`;

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
        stroke={artwork.barkDark}
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
        <path d="M53.5 41 L87 41 L91 46 L51.5 46 Z" fill={artwork.hatBand} />
        <rect
          x="66"
          y="40.5"
          width="9"
          height="6"
          rx="1"
          fill="none"
          stroke={artwork.hatStitch}
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
        <rect
          x="42"
          y="44"
          width="60"
          height="11"
          rx="5.5"
          fill={artwork.fur}
          stroke={artwork.furEdge}
        />
        <circle cx="34" cy="43" r="6" fill={artwork.fur} stroke={artwork.furEdge} />
      </g>
    ) : accessory === "eggshell" ? (
      <g transform={place}>
        <path
          d="M47 51 C45 34 57 25 72 25 C87 25 99 34 97 51 L91 45 L85 52 L79 45 L73 53 L67 45 L61 52 L55 45 Z"
          fill={`url(#${id}-egg)`}
          stroke={artwork.eggDark}
          strokeWidth="1"
          strokeLinejoin="round"
        />
        {EGG_SPECKS.map(([x, y, r], i) => (
          <circle
            key={`${x}-${y}`}
            cx={x}
            cy={y}
            r={r}
            fill={i % 2 ? artwork.eggSpeckGreen : artwork.eggSpeckTeal}
          />
        ))}
        <path
          d="M72 26 L69 32 L74 36 L70 42"
          fill="none"
          stroke={artwork.eggDark}
          strokeWidth="1.2"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </g>
    ) : accessory === "nemes" ? (
      // Drawn about the middle of the head at x 72; worn nudged onto the log.
      <g transform={place}>
        <path
          d="M45 46 C41 62 37 82 36 102 L50 102 C50 82 51 64 53 52 Z"
          fill={`url(#${id}-nemes)`}
        />
        <path
          d="M99 46 C103 62 107 82 108 102 L94 102 C94 82 93 64 91 52 Z"
          fill={`url(#${id}-nemes)`}
        />
        <rect x="36" y="99" width="14" height="4" rx="1" fill={`url(#${id}-gold)`} />
        <rect x="94" y="99" width="14" height="4" rx="1" fill={`url(#${id}-gold)`} />
        <path d="M45 52 C43 30 56 20 72 20 C88 20 101 30 99 52 Z" fill={`url(#${id}-nemes)`} />
        <path
          d="M44 47 Q72 57 100 47 L100 54 Q72 64 44 54 Z"
          fill={`url(#${id}-gold)`}
          stroke={artwork.goldDark}
          strokeWidth="0.8"
        />
        <path
          d="M72 32 C77 32 78 40 75 47 C74 50 70 50 69 47 C66 40 67 32 72 32 Z"
          fill={`url(#${id}-gold)`}
          stroke={artwork.goldDark}
          strokeWidth="0.8"
        />
        <circle cx="72" cy="38" r="1.5" fill={artwork.ruby} />
      </g>
    ) : accessory === "laurel" ? (
      <g transform={place}>
        <path
          d={`M${80 - LAUREL_RADIUS * 0.94} 80 A${LAUREL_RADIUS} ${LAUREL_RADIUS} 0 0 1 ${80 + LAUREL_RADIUS * 0.94} 80`}
          fill="none"
          stroke={STEM}
          strokeWidth="2.4"
          strokeLinecap="round"
        />
        {LAUREL_LEAVES.map(({ x, y, heading }) =>
          [-32, 32].map((spread) => (
            <ellipse
              key={`${x}-${spread}`}
              cx="5.5"
              cy="0"
              rx="7"
              ry="3.2"
              transform={`translate(${x.toFixed(2)} ${y.toFixed(2)}) rotate(${heading + spread})`}
              fill={`url(#${id}-laurel)`}
              stroke={STEM}
              strokeWidth="0.6"
            />
          )),
        )}
        <path
          d="M42 80 L33 99 L39.5 95.5 L40 104 L47 84 Z"
          fill={artwork.santaLight}
          stroke={artwork.santaDark}
          strokeWidth="0.8"
          strokeLinejoin="round"
        />
      </g>
    ) : accessory === "crown" ? (
      <g transform={place}>
        <path
          d="M50 51 L47 27 L59 39 L65 25 L72 39 L79 25 L85 39 L97 27 L94 51 Z"
          fill={`url(#${id}-gold)`}
          stroke={artwork.goldDark}
          strokeWidth="1"
          strokeLinejoin="round"
        />
        {[47, 65, 79, 97].map((x, i) => (
          <circle
            key={x}
            cx={x}
            cy={i === 0 || i === 3 ? 26 : 24}
            r="2.6"
            fill={artwork.goldLight}
          />
        ))}
        <circle cx="72" cy="37" r="2.2" fill={artwork.goldLight} />
        <rect
          x="49"
          y="42"
          width="46"
          height="10"
          rx="2"
          fill={`url(#${id}-gold)`}
          stroke={artwork.goldDark}
          strokeWidth="1"
        />
        <circle cx="72" cy="47" r="3.6" fill={artwork.ruby} stroke={artwork.goldDark} />
        <circle cx="59.5" cy="47" r="2.6" fill={artwork.sapphire} stroke={artwork.goldDark} />
        <circle cx="84.5" cy="47" r="2.6" fill={artwork.sapphire} stroke={artwork.goldDark} />
        <circle cx="71" cy="46" r="1" fill={artwork.white} opacity="0.7" />
      </g>
    ) : accessory === "beret" ? (
      <g transform={place}>
        <path
          d="M43 52 C36 40 44 26 66 25 C90 24 106 34 97 50 Q70 58 43 52 Z"
          fill={`url(#${id}-beret)`}
        />
        <path d="M43 52 Q70 58 97 50 L96 46 Q70 54 44 47 Z" fill={artwork.beretDark} />
        <path
          d="M66 25 C66 21 70 20 72 22"
          fill="none"
          stroke={artwork.beretDark}
          strokeWidth="2.6"
          strokeLinecap="round"
        />
        <path
          d="M56 44 C47 36 40 26 28 23 C32 33 42 46 54 49 Z"
          fill={artwork.white}
          stroke={artwork.furEdge}
          strokeWidth="1"
          strokeLinejoin="round"
        />
        <path
          d="M55 46 C46 37 38 28 29 24"
          fill="none"
          stroke={artwork.furEdge}
          strokeWidth="0.9"
          strokeLinecap="round"
        />
      </g>
    ) : accessory === "phrygian" ? (
      <g transform={place}>
        <path
          d="M47 52 C45 38 52 26 66 24 C76 22 90 26 95 36 C98 42 97 48 96 52 Z"
          fill={`url(#${id}-phrygian)`}
        />
        <path
          d="M66 24 C56 12 42 14 38 25 C36 31 40 35 45 33 C46 30 48 27 53 27 C58 27 63 29 70 33 Z"
          fill={`url(#${id}-phrygian)`}
          stroke={artwork.phrygianDark}
          strokeWidth="1"
          strokeLinejoin="round"
        />
        <path d="M47 52 Q72 58 96 52 L97 47 Q72 53 46 47 Z" fill={artwork.phrygianDark} />
        <g transform="translate(86 45)">
          <circle
            r="8.5"
            fill={artwork.phrygianLight}
            stroke={artwork.phrygianDark}
            strokeWidth="1"
          />
          <circle r="5.6" fill={artwork.white} />
          <circle r="2.8" fill={artwork.cockadeBlue} />
        </g>
      </g>
    ) : accessory === "tophat" ? (
      <g transform={place}>
        <path d="M55 49 L57 16 Q72 11 87 16 L89 49 Z" fill={`url(#${id}-tophat)`} />
        <ellipse cx="72" cy="15.5" rx="15.5" ry="4" fill={artwork.topHatLight} />
        <path d="M55.6 38 L88.4 38 L88.8 46 L55.2 46 Z" fill={artwork.santaDark} />
        <path
          d="M60 20 L61 44"
          stroke={artwork.white}
          strokeOpacity="0.25"
          strokeWidth="2"
          strokeLinecap="round"
        />
        <ellipse cx="72" cy="50" rx="31" ry="6" fill={`url(#${id}-tophat)`} />
      </g>
    ) : accessory === "helmet" ? (
      <g transform={place}>
        <ellipse
          cx="80"
          cy="74"
          rx="54"
          ry="66"
          fill={`url(#${id}-glass)`}
          stroke={artwork.collarDark}
          strokeWidth="2"
        />
        <path
          d="M36 52 C40 36 50 25 63 20"
          fill="none"
          stroke={artwork.white}
          strokeOpacity="0.8"
          strokeWidth="5"
          strokeLinecap="round"
        />
        <circle cx="31" cy="68" r="2.4" fill={artwork.white} opacity="0.8" />
        <path
          d="M118 96 C116 108 110 118 102 124"
          fill="none"
          stroke={artwork.white}
          strokeOpacity="0.35"
          strokeWidth="3"
          strokeLinecap="round"
        />
        <rect
          x="44"
          y="128"
          width="72"
          height="15"
          rx="7.5"
          fill={`url(#${id}-collar)`}
          stroke={artwork.collarDark}
          strokeWidth="1.2"
        />
        <path
          d="M52 133 H108"
          stroke={artwork.white}
          strokeOpacity="0.9"
          strokeWidth="1.6"
          strokeLinecap="round"
        />
      </g>
    ) : accessory === "cosmonaut" ? (
      // A snug dome just round the log, the sprout poking out through a seal
      // at its top, a white collar with a red stripe and a stubby antenna.
      <g transform={place}>
        <circle
          cx="80"
          cy="92"
          r="51"
          fill={`url(#${id}-glass)`}
          stroke={artwork.collarDark}
          strokeWidth="2"
        />
        <path
          d="M43 72 C47 58 57 48 69 44"
          fill="none"
          stroke={artwork.cosmonautGlow}
          strokeOpacity="0.75"
          strokeWidth="4.5"
          strokeLinecap="round"
        />
        <circle cx="40" cy="84" r="2.2" fill={artwork.white} opacity="0.8" />
        <ellipse
          cx="80"
          cy="42"
          rx="7"
          ry="3"
          fill={`url(#${id}-collar)`}
          stroke={artwork.collarDark}
          strokeWidth="1"
        />
        <path
          d="M112 58 L119 45"
          stroke={artwork.collarDark}
          strokeWidth="2"
          strokeLinecap="round"
        />
        <circle cx="119.5" cy="44" r="3" fill={artwork.santaLight} />
        <path
          d="M40.9 124.8 A51 51 0 0 0 119.1 124.8"
          fill="none"
          stroke={`url(#${id}-collar)`}
          strokeWidth="10"
          strokeLinecap="round"
        />
        <path
          d="M44.8 121.6 A46 46 0 0 0 115.2 121.6"
          fill="none"
          stroke={artwork.santaLight}
          strokeWidth="2.2"
          strokeLinecap="round"
        />
      </g>
    ) : accessory === "garland" ? (
      <g transform={place}>
        <path
          d="M44 52 Q72 62 100 52"
          fill="none"
          stroke={STEM}
          strokeWidth="2.2"
          strokeLinecap="round"
        />
        {GARLAND_X.map((x, i) => (
          <ellipse
            key={x}
            cx={x}
            cy={garlandY(x) + (i % 2 ? 3.5 : -3.5)}
            rx="6"
            ry="2.8"
            transform={`rotate(${i % 2 ? 40 : -40} ${x} ${garlandY(x) + (i % 2 ? 3.5 : -3.5)})`}
            fill={`url(#${id}-laurel)`}
            stroke={STEM}
            strokeWidth="0.6"
          />
        ))}
        {GARLAND_BLOSSOMS.map(([x, y, r, tone]) => {
          const petal =
            tone === "white"
              ? artwork.blossomWhite
              : tone === "pink"
                ? artwork.blossomPink
                : artwork.blossomYellow;
          return (
            <g key={x}>
              {[0, 72, 144, 216, 288].map((deg) => (
                <circle
                  key={deg}
                  cx={x + r * 0.62 * Math.cos((deg * Math.PI) / 180)}
                  cy={y + r * 0.62 * Math.sin((deg * Math.PI) / 180)}
                  r={r * 0.5}
                  fill={petal}
                  stroke={artwork.furEdge}
                  strokeWidth="0.4"
                />
              ))}
              <circle
                cx={x}
                cy={y}
                r={r * 0.34}
                fill={tone === "yellow" ? artwork.hatBand : artwork.blossomYellow}
              />
            </g>
          );
        })}
        {/* Vines trail down over the log from each end. */}
        <path
          d="M45 55 C37 60 41 68 35 74 C32 78 34 82 38 83"
          fill="none"
          stroke={STEM}
          strokeWidth="1.8"
          strokeLinecap="round"
        />
        <path
          d="M99 55 C107 60 103 66 108 71"
          fill="none"
          stroke={STEM}
          strokeWidth="1.8"
          strokeLinecap="round"
        />
        {[
          [38, 69, -50],
          [36, 78, 35],
          [106, 64, 40],
        ].map(([x, y, turn]) => (
          <ellipse
            key={`${x}-${y}`}
            cx={x}
            cy={y}
            rx="4.5"
            ry="2.2"
            transform={`rotate(${turn} ${x} ${y})`}
            fill={`url(#${id}-laurel)`}
            stroke={STEM}
            strokeWidth="0.5"
          />
        ))}
        <circle cx="38" cy="83" r="2.2" fill={artwork.blossomPink} />
      </g>
    ) : accessory === "kabuto" ? (
      <g transform={place}>
        {KABUTO_PLATES.map(({ x1, x2, y1, y2 }, i) => (
          <g key={y1}>
            <path
              d={`M${x1 + 4} ${y1} L${x2 - 4} ${y1} L${x2} ${y2} Q72 ${y2 + 3} ${x1} ${y2} Z`}
              fill={`url(#${id}-kabuto)`}
              stroke={artwork.kabutoDark}
              strokeWidth="0.8"
              strokeLinejoin="round"
            />
            <path
              d={`M${x1 + 1} ${y2 - 1} Q72 ${y2 + 2} ${x2 - 1} ${y2 - 1}`}
              fill="none"
              stroke={i % 2 ? artwork.goldDark : artwork.kabutoCord}
              strokeWidth="1.3"
              strokeLinecap="round"
            />
          </g>
        ))}
        <path
          d="M46 52 C44 34 56 23 72 23 C88 23 100 34 98 52 Z"
          fill={`url(#${id}-kabuto)`}
          stroke={artwork.kabutoDark}
          strokeWidth="0.8"
        />
        <g fill="none" stroke={artwork.kabutoLight} strokeWidth="1.1" strokeLinecap="round">
          <path d="M72 24 L72 51" />
          <path d="M66 25 C58 31 55 41 54 51" />
          <path d="M78 25 C86 31 89 41 90 51" />
          <path d="M59 28 C50 34 48 42 48 50" strokeOpacity="0.6" />
          <path d="M85 28 C94 34 96 42 96 50" strokeOpacity="0.6" />
        </g>
        <path
          d="M45 49 Q72 55 99 49 L99 53 Q72 59 45 53 Z"
          fill={`url(#${id}-gold)`}
          stroke={artwork.goldDark}
          strokeWidth="0.6"
        />
        <path
          d="M72 40 C64 40 56 34 52 14 C59 25 66 28 72 28 C78 28 85 25 92 14 C88 34 80 40 72 40 Z"
          fill={`url(#${id}-gold)`}
          stroke={artwork.goldDark}
          strokeWidth="0.8"
          strokeLinejoin="round"
        />
        <circle
          cx="72"
          cy="42"
          r="3"
          fill={artwork.goldLight}
          stroke={artwork.goldDark}
          strokeWidth="0.8"
        />
      </g>
    ) : accessory === "cap" ? (
      // Worn backwards: the bill points left, behind Stumpy.
      <g transform={place}>
        <path
          d="M50 47 C40 45 28 47 20 53 C28 57 42 56 52 54 Z"
          fill={artwork.capDark}
          stroke={artwork.kabutoDark}
          strokeWidth="0.6"
          strokeLinejoin="round"
        />
        <path
          d="M24 53 C34 53 42 52 51 50"
          fill="none"
          stroke={artwork.capLight}
          strokeOpacity="0.6"
          strokeWidth="1.2"
          strokeLinecap="round"
        />
        <path
          d="M48 52 C46 33 58 24 74 24 C90 24 99 34 97 52 Q72 58 48 52 Z"
          fill={`url(#${id}-cap)`}
        />
        <path d="M48 52 Q72 58 97 52 L97 47 Q72 53 48 47 Z" fill={artwork.capDark} />
        <g fill="none" stroke={artwork.white} strokeWidth="1.4" strokeLinecap="round">
          <path d="M74 25 C72 33 71 42 72 51" />
          <path d="M74 25 C84 29 91 38 93 49" />
          <path d="M74 25 C64 28 56 36 53 47" />
        </g>
        <circle
          cx="74"
          cy="24"
          r="2.6"
          fill={artwork.capDark}
          stroke={artwork.white}
          strokeWidth="0.8"
        />
      </g>
    ) : accessory === "antennae" ? (
      <g transform={place}>
        <g fill="none" stroke={artwork.collarDark} strokeWidth="2.2" strokeLinecap="round">
          <path d="M58 50 C56 40 62 36 56 28 C53 24 52 21 52 18" />
          <path d="M86 50 C88 40 82 34 88 26 C91 22 92 19 92 16" />
        </g>
        <path
          d="M46 58 Q72 48 98 58"
          fill="none"
          stroke={artwork.visorDark}
          strokeWidth="5"
          strokeLinecap="round"
        />
        <path
          d="M50 56 Q72 47 94 56"
          fill="none"
          stroke={artwork.neonCyan}
          strokeOpacity="0.7"
          strokeWidth="0.9"
          strokeLinecap="round"
        />
        {[53, 81].map((x) => (
          <rect
            key={x}
            x={x}
            y="44"
            width="10"
            height="8"
            rx="2.5"
            fill={`url(#${id}-collar)`}
            stroke={artwork.collarDark}
            strokeWidth="0.8"
          />
        ))}
        <circle cx="52" cy="17" r="10" fill={`url(#${id}-glowc)`} />
        <circle cx="92" cy="15" r="10" fill={`url(#${id}-glowv)`} />
        <circle
          cx="52"
          cy="17"
          r="3.2"
          fill={artwork.neonCyan}
          stroke={artwork.white}
          strokeWidth="0.8"
        />
        <circle
          cx="92"
          cy="15"
          r="3.2"
          fill={artwork.robotViolet}
          stroke={artwork.white}
          strokeWidth="0.8"
        />
      </g>
    ) : accessory === "bone" ? (
      <g transform={place}>
        <path
          d="M44 58 Q72 40 100 56"
          fill="none"
          stroke={artwork.leopardSpot}
          strokeWidth="14.4"
          strokeLinecap="round"
        />
        <path
          d="M44 58 Q72 40 100 56"
          fill="none"
          stroke={`url(#${id}-leopard)`}
          strokeWidth="12"
          strokeLinecap="round"
        />
        {[
          [50, 55, 0],
          [56, 52, 1],
          [62, 50, 0],
          [68, 47.5, 1],
          [75, 46.5, 0],
          [81, 47, 1],
          [87, 49, 0],
          [93, 52, 1],
        ].map(([x, y, up]) => (
          <g key={x}>
            <ellipse
              cx={x}
              cy={(y ?? 0) + (up ? -2.2 : 2.4)}
              rx="2.6"
              ry="1.9"
              fill={artwork.leopardSpot}
              transform={`rotate(${up ? -20 : 20} ${x} ${y})`}
            />
            <path
              d={`M${(x ?? 0) - 2.2} ${(y ?? 0) + (up ? -3.6 : 0.6)} q2.2 -2 4.4 0`}
              fill="none"
              stroke={artwork.leopardSpot}
              strokeWidth="0.9"
              strokeLinecap="round"
            />
          </g>
        ))}
        <g transform="rotate(-60 62 46) translate(62 46) scale(1.25) translate(-62 -46)">
          <rect
            x="43"
            y="43.3"
            width="38"
            height="5.4"
            rx="2.4"
            fill={artwork.hornLight}
            stroke={artwork.hornDark}
            strokeWidth="0.8"
          />
          {[
            [43.5, 43.4],
            [43.5, 48.6],
            [80.5, 43.4],
            [80.5, 48.6],
          ].map(([x, y]) => (
            <circle
              key={`${x}-${y}`}
              cx={x}
              cy={y}
              r="2.7"
              fill={artwork.hornLight}
              stroke={artwork.hornDark}
              strokeWidth="0.8"
            />
          ))}
        </g>
      </g>
    ) : accessory === "furhood" ? (
      <g transform={place}>
        <path
          d="M45 52 L43 44 L47 45 L44 36 L50 38 L50 30 L57 33 L58 25 L65 29 L68 22 L74 28 L80 23 L83 30 L90 26 L91 34 L97 33 L96 41 L101 42 L99 52 Z"
          fill={`url(#${id}-fur)`}
          stroke={artwork.pelt}
          strokeWidth="0.8"
          strokeLinejoin="round"
        />
        <g fill="none" stroke={artwork.pelt} strokeWidth="1.1" strokeLinecap="round">
          <path d="M56 36 L54 43" />
          <path d="M64 31 L63 40" />
          <path d="M73 31 L72 39" />
          <path d="M82 32 L83 40" />
          <path d="M90 36 L91 43" />
        </g>
        {[47, 53, 59, 65, 72, 79, 85, 91, 97].map((x, i) => (
          <circle
            key={x}
            cx={x}
            cy={51 + (i % 2 ? 2.5 : 0)}
            r="5.4"
            fill={artwork.peltTrim}
            stroke={artwork.furEdge}
            strokeWidth="0.8"
          />
        ))}
        <path d="M80 46 L87 41" stroke={artwork.pelt} strokeWidth="0.9" strokeLinecap="round" />
        <g transform="rotate(-35 84 44)">
          <rect
            x="78"
            y="42"
            width="12"
            height="4.4"
            rx="2.2"
            fill={artwork.hornLight}
            stroke={artwork.hornDark}
            strokeWidth="0.8"
          />
          <circle
            cx="78.5"
            cy="42.6"
            r="2.3"
            fill={artwork.hornLight}
            stroke={artwork.hornDark}
            strokeWidth="0.8"
          />
          <circle
            cx="78.5"
            cy="45.8"
            r="2.3"
            fill={artwork.hornLight}
            stroke={artwork.hornDark}
            strokeWidth="0.8"
          />
          <circle
            cx="89.5"
            cy="42.6"
            r="2.3"
            fill={artwork.hornLight}
            stroke={artwork.hornDark}
            strokeWidth="0.8"
          />
          <circle
            cx="89.5"
            cy="45.8"
            r="2.3"
            fill={artwork.hornLight}
            stroke={artwork.hornDark}
            strokeWidth="0.8"
          />
        </g>
      </g>
    ) : accessory === "horned" ? (
      <g transform={place}>
        <path
          d="M49 48 C36 49 27 41 27 21 C33 33 40 38 50 38 Z"
          fill={`url(#${id}-horn)`}
          stroke={artwork.hornDark}
          strokeWidth="0.8"
          strokeLinejoin="round"
        />
        <path
          d="M95 48 C108 49 117 41 117 21 C111 33 104 38 94 38 Z"
          fill={`url(#${id}-horn)`}
          stroke={artwork.hornDark}
          strokeWidth="0.8"
          strokeLinejoin="round"
        />
        <path
          d="M46 52 C44 34 56 23 72 23 C88 23 100 34 98 52 Z"
          fill={`url(#${id}-steel)`}
          stroke={artwork.steelDark}
          strokeWidth="0.8"
        />
        <path
          d="M45 43 Q72 49 99 43 L99 51 Q72 57 45 51 Z"
          fill={`url(#${id}-steel)`}
          stroke={artwork.steelDark}
          strokeWidth="0.8"
        />
        <path
          d="M69 24 L75 24 L75 62 Q72 66 69 62 Z"
          fill={`url(#${id}-steel)`}
          stroke={artwork.steelDark}
          strokeWidth="0.8"
          strokeLinejoin="round"
        />
        {[50, 58, 86, 94].map((x) => (
          <circle
            key={x}
            cx={x}
            cy={x < 72 ? 46.6 + (x === 50 ? 0 : 1.2) : 46.6 + (x === 94 ? 0 : 1.2)}
            r="1.4"
            fill={artwork.steelLight}
            stroke={artwork.steelDark}
            strokeWidth="0.5"
          />
        ))}
        <path
          d="M52 32 C56 27 62 25 66 25"
          fill="none"
          stroke={artwork.white}
          strokeOpacity="0.6"
          strokeWidth="2"
          strokeLinecap="round"
        />
      </g>
    ) : accessory === "quetzal" ? (
      <g transform={place}>
        {QUETZAL_FEATHERS.map(({ angle, length, tone }) => (
          <g key={angle} transform={`translate(62 47) rotate(${angle})`}>
            <path
              d={`M0 0 C7 -${length * 0.3} 7 -${length * 0.75} 0 -${length} C-7 -${length * 0.75} -7 -${length * 0.3} 0 0 Z`}
              fill={tone === "red" ? artwork.ruby : `url(#${id}-quetzal)`}
              stroke={tone === "red" ? artwork.santaDark : artwork.quetzalDeep}
              strokeWidth="0.7"
            />
            <path
              d={`M0 -2 L0 -${length * 0.92}`}
              stroke={artwork.white}
              strokeOpacity="0.55"
              strokeWidth="0.8"
              strokeLinecap="round"
            />
          </g>
        ))}
        <path
          d="M46 53 Q72 45 98 53 L98 59 Q72 51 46 59 Z"
          fill={`url(#${id}-jade)`}
          stroke={artwork.jadeDark}
          strokeWidth="0.8"
          strokeLinejoin="round"
        />
        <circle cx="72" cy="52" r="8" fill={`url(#${id}-gold)`} stroke={artwork.goldDark} />
        <circle cx="72" cy="52" r="4.6" fill={`url(#${id}-jade)`} stroke={artwork.jadeDark} />
        <circle cx="70.5" cy="50.5" r="1.2" fill={artwork.white} opacity="0.7" />
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
        stroke={artwork.white}
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
  // The visor wraps across the eyes, about its middle at (85, 83).
  const visor = (place: string) => (
    <g transform={place}>
      <path
        d="M52 82 Q52 71 66 71 H104 Q118 71 118 82 Q118 95 104 95 H66 Q52 95 52 82 Z"
        fill={`url(#${id}-visor)`}
        stroke={`url(#${id}-neon)`}
        strokeWidth="3"
        strokeLinejoin="round"
      />
      <path
        d="M58 78 Q72 75 112 77"
        fill="none"
        stroke={artwork.white}
        strokeOpacity="0.55"
        strokeWidth="1.6"
        strokeLinecap="round"
      />
      <path
        d="M60 89 H110"
        fill="none"
        stroke={`url(#${id}-neon)`}
        strokeOpacity="0.8"
        strokeWidth="2.2"
        strokeLinecap="round"
      />
    </g>
  );
  // Hats sit on the crown of the log, tipped a little to the left so the
  // leaf still peeks out to the right. Whatever Stumpy wears lands on the
  // ground in front when it falls.
  const wornPlace =
    accessory === "laurel"
      ? "rotate(-6 80 94)"
      : accessory === "nemes"
        ? "translate(8 3) rotate(-4 72 50)"
        : accessory === "helmet" || accessory === "cosmonaut"
          ? ""
          : "translate(0 3) rotate(-10 72 50)";
  const knockedPlace =
    accessory === "nemes"
      ? aboutBrim(58, 74, 0.45)
      : accessory === "laurel"
        ? "translate(130 143) rotate(6) scale(0.55) translate(-80 -66)"
        : accessory === "helmet" || accessory === "cosmonaut"
          ? "translate(126 147) rotate(14) scale(0.34) translate(-80 -142)"
          : accessory === "garland" || accessory === "kabuto"
            ? aboutBrim(54, 80, 0.5)
            : accessory === "horned"
              ? aboutBrim(46, 84, 0.5)
              : accessory === "quetzal"
                ? aboutBrim(62, 84, 0.5)
                : aboutBrim(54, 88, 0.55);
  const dropped = "translate(122 140) rotate(-8) scale(0.6) translate(-85 -83)";
  const worn =
    accessory === "sunglasses" ? shades("") : accessory === "visor" ? visor("") : wear(wornPlace);
  const knockedOff =
    accessory === "sunglasses"
      ? shades(dropped)
      : accessory === "visor"
        ? visor(dropped)
        : wear(knockedPlace);

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
        <g fontFamily="var(--font-sans, sans-serif)" fontWeight="700">
          <text x="118" y="64" fontSize="14" fill={artwork.bark}>
            z
          </text>
          <text x="128" y="52" fontSize="10" fill={artwork.barkLight}>
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
        <ellipse cx="84" cy="146" rx="46" ry="4" fill={artwork.shadow} opacity="0.16" />
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
        <ellipse cx="80" cy="149" rx="32" ry="4" fill={artwork.shadow} opacity="0.18" />
        {sprout}
        {log}
        <g
          ref={(group) => {
            eyes.current = group;
            if (eyesRef) eyesRef.current = group;
          }}
        >
          {/* Glancing eyes would peek out from behind sunglasses and the visor. */}
          {accessory === "sunglasses" || accessory === "visor" ? null : (
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
          <stop offset="0" stopColor={artwork.barkLight} />
          <stop offset="0.6" stopColor={artwork.bark} />
          <stop offset="1" stopColor={artwork.barkDark} />
        </radialGradient>
        <radialGradient id={`${id}-face`} cx="0.4" cy="0.35" r="0.75">
          <stop offset="0" stopColor={artwork.faceLight} />
          <stop offset="0.7" stopColor={artwork.face} />
          <stop offset="1" stopColor={artwork.faceDark} />
        </radialGradient>
        <linearGradient id={`${id}-leaf`} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor={artwork.leafLight} />
          <stop offset="1" stopColor={STEM} />
        </linearGradient>
        {accessory === "sunglasses" ? (
          <linearGradient id={`${id}-shades`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stopColor={artwork.glassesLight} />
            <stop offset="1" stopColor={artwork.glassesDark} />
          </linearGradient>
        ) : null}
        {accessory === "witch" ? (
          <linearGradient id={`${id}-witch`} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0" stopColor={artwork.witchLight} />
            <stop offset="1" stopColor={artwork.witchDark} />
          </linearGradient>
        ) : null}
        {accessory === "santa" ? (
          <linearGradient id={`${id}-santa`} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0" stopColor={artwork.santaLight} />
            <stop offset="1" stopColor={artwork.santaDark} />
          </linearGradient>
        ) : null}
        {accessory === "eggshell" ? (
          <radialGradient id={`${id}-egg`} cx="0.35" cy="0.3" r="0.9">
            <stop offset="0" stopColor={artwork.eggLight} />
            <stop offset="1" stopColor={artwork.eggDark} />
          </radialGradient>
        ) : null}
        {accessory === "nemes" ||
        accessory === "crown" ||
        accessory === "kabuto" ||
        accessory === "quetzal" ? (
          <linearGradient id={`${id}-gold`} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0" stopColor={artwork.goldLight} />
            <stop offset="1" stopColor={artwork.goldDark} />
          </linearGradient>
        ) : null}
        {accessory === "nemes" ? (
          // Hard stops repeating down the cloth make the blue and gold stripes.
          <linearGradient
            id={`${id}-nemes`}
            gradientUnits="userSpaceOnUse"
            x1="0"
            y1="20"
            x2="0"
            y2="28"
            spreadMethod="repeat"
          >
            <stop offset="0" stopColor={artwork.nemesBlue} />
            <stop offset="0.5" stopColor={artwork.nemesBlueDark} />
            <stop offset="0.5" stopColor={artwork.goldLight} />
            <stop offset="1" stopColor={artwork.goldDark} />
          </linearGradient>
        ) : null}
        {accessory === "laurel" || accessory === "garland" ? (
          <linearGradient id={`${id}-laurel`} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0" stopColor={artwork.leafLight} />
            <stop offset="1" stopColor={STEM} />
          </linearGradient>
        ) : null}
        {accessory === "beret" ? (
          <radialGradient id={`${id}-beret`} cx="0.4" cy="0.3" r="0.9">
            <stop offset="0" stopColor={artwork.beretLight} />
            <stop offset="1" stopColor={artwork.beretDark} />
          </radialGradient>
        ) : null}
        {accessory === "phrygian" ? (
          <linearGradient id={`${id}-phrygian`} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0" stopColor={artwork.phrygianLight} />
            <stop offset="1" stopColor={artwork.phrygianDark} />
          </linearGradient>
        ) : null}
        {accessory === "tophat" ? (
          <linearGradient id={`${id}-tophat`} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0" stopColor={artwork.topHatLight} />
            <stop offset="1" stopColor={artwork.topHatDark} />
          </linearGradient>
        ) : null}
        {accessory === "quetzal" ? (
          <>
            <linearGradient id={`${id}-quetzal`} x1="0" y1="0" x2="1" y2="1">
              <stop offset="0" stopColor={artwork.quetzalTurquoise} />
              <stop offset="1" stopColor={artwork.quetzalGreen} />
            </linearGradient>
            <linearGradient id={`${id}-jade`} x1="0" y1="0" x2="1" y2="1">
              <stop offset="0" stopColor={artwork.jade} />
              <stop offset="1" stopColor={artwork.jadeDark} />
            </linearGradient>
          </>
        ) : null}
        {accessory === "bone" ? (
          <linearGradient id={`${id}-leopard`} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0" stopColor={artwork.leopard} />
            <stop offset="1" stopColor={artwork.leopardDark} />
          </linearGradient>
        ) : null}
        {accessory === "furhood" ? (
          <linearGradient id={`${id}-fur`} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0" stopColor={artwork.peltLight} />
            <stop offset="1" stopColor={artwork.pelt} />
          </linearGradient>
        ) : null}
        {accessory === "horned" ? (
          <>
            <linearGradient id={`${id}-steel`} x1="0" y1="0" x2="1" y2="1">
              <stop offset="0" stopColor={artwork.steelLight} />
              <stop offset="1" stopColor={artwork.steelDark} />
            </linearGradient>
            <linearGradient id={`${id}-horn`} x1="0" y1="0" x2="1" y2="1">
              <stop offset="0" stopColor={artwork.hornLight} />
              <stop offset="1" stopColor={artwork.hornDark} />
            </linearGradient>
          </>
        ) : null}
        {accessory === "kabuto" ? (
          <linearGradient id={`${id}-kabuto`} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0" stopColor={artwork.kabutoLight} />
            <stop offset="1" stopColor={artwork.kabutoDark} />
          </linearGradient>
        ) : null}
        {accessory === "cap" ? (
          <linearGradient id={`${id}-cap`} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0" stopColor={artwork.capLight} />
            <stop offset="1" stopColor={artwork.capDark} />
          </linearGradient>
        ) : null}
        {accessory === "antennae" ? (
          <>
            {[
              ["glowc", artwork.neonCyan],
              ["glowv", artwork.robotViolet],
            ].map(([name, color]) => (
              <radialGradient key={name} id={`${id}-${name}`} cx="0.5" cy="0.5" r="0.5">
                <stop offset="0" stopColor={color} stopOpacity="0.85" />
                <stop offset="0.45" stopColor={color} stopOpacity="0.35" />
                <stop offset="1" stopColor={color} stopOpacity="0" />
              </radialGradient>
            ))}
            <linearGradient id={`${id}-collar`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0" stopColor={artwork.collarLight} />
              <stop offset="1" stopColor={artwork.collarDark} />
            </linearGradient>
          </>
        ) : null}
        {accessory === "helmet" || accessory === "cosmonaut" ? (
          <>
            <radialGradient id={`${id}-glass`} cx="0.5" cy="0.5" r="0.5">
              <stop offset="0" stopColor={artwork.glassTint} stopOpacity="0.04" />
              <stop offset="0.8" stopColor={artwork.glassTint} stopOpacity="0.14" />
              <stop offset="1" stopColor={artwork.glassTint} stopOpacity="0.4" />
            </radialGradient>
            <linearGradient id={`${id}-collar`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0" stopColor={artwork.collarLight} />
              <stop offset="1" stopColor={artwork.collarDark} />
            </linearGradient>
          </>
        ) : null}
        {accessory === "visor" ? (
          <>
            <linearGradient id={`${id}-visor`} x1="0" y1="0" x2="1" y2="1">
              <stop offset="0" stopColor={artwork.visorDark} />
              <stop offset="1" stopColor={artwork.glassesDark} />
            </linearGradient>
            <linearGradient
              id={`${id}-neon`}
              gradientUnits="userSpaceOnUse"
              x1="52"
              y1="0"
              x2="118"
              y2="0"
            >
              <stop offset="0" stopColor={artwork.neonCyan} />
              <stop offset="1" stopColor={artwork.neonMagenta} />
            </linearGradient>
          </>
        ) : null}
      </defs>
      {body}
    </svg>
  );
}
