import type { ReactNode } from "react";

/** The application's icon set, as inline SVG.
 *
 *  Hand-drawn rather than a dependency: the whole need is a dozen glyphs, GitHub's
 *  mark is the one filled exception (`GitHubIcon`), and an
 *  off-the-shelf set on a 24-unit grid at 2px reads visibly heavier than the
 *  rest of this interface. `public/icons.svg` cannot be reused — its fills are
 *  hard-coded, so it has no way to express "tertiary when inactive".
 *
 *  One grid (16), one stroke weight (1.5), and `currentColor` throughout.
 *  Colour is deliberately not a prop: an icon inherits from the row it sits in,
 *  which is what keeps a nav item's inactive colour a single class on the
 *  parent rather than a value threaded through two components. */

export type IconProps = { className?: string; size?: number };

function Icon({ size = 16, className, children }: IconProps & { children: ReactNode }) {
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

export function CheckIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="m3 8.4 3.2 3.2L13 5" />
    </Icon>
  );
}

export function FilterIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M2.5 4h11M4.5 8h7M6.5 12h3" />
    </Icon>
  );
}

export function ChevronDownIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="m4 6.5 4 4 4-4" />
    </Icon>
  );
}

export function ChevronLeftIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="m9.5 4-4 4 4 4" />
    </Icon>
  );
}

export function PlusIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M8 3.5v9M3.5 8h9" />
    </Icon>
  );
}

export function MenuIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M2.5 4.5h11M2.5 8h11M2.5 11.5h11" />
    </Icon>
  );
}

/** More: three dots in a row, for a menu of what can be done with a thing. */
export function MoreIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M3.5 8h.01M8 8h.01M12.5 8h.01" strokeWidth={2.5} />
    </Icon>
  );
}

export function CloseIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="m4 4 8 8M12 4l-8 8" />
    </Icon>
  );
}

/** A panel with its left column ruled off: the application rail, for the
 *  control that keeps it open. */
export function SidebarIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <rect x="2" y="2.75" width="12" height="10.5" rx="1.5" />
      <path d="M6 2.75v10.5" />
    </Icon>
  );
}

export function SearchIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="7.25" cy="7.25" r="4.25" />
      <path d="m10.5 10.5 2.5 2.5" />
    </Icon>
  );
}

export function SignOutIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M6.5 13.5h-3v-11h3" />
      <path d="M9.5 10.5 12.5 8 9.5 5.5" />
      <path d="M12.5 8h-7" />
    </Icon>
  );
}

export function ExternalLinkIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M8.5 3.5H3.5v9h9v-5" />
      <path d="M10 2.5h3.5V6" />
      <path d="M13.5 2.5 8 8" />
    </Icon>
  );
}

/** A terminal window with a prompt: a device signed in from the command line. */
export function TerminalIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <rect x="1.75" y="2.75" width="12.5" height="10.5" rx="1.5" />
      <path d="m4.5 6.25 2 1.75-2 1.75" />
      <path d="M8.5 10h3" />
    </Icon>
  );
}

/** GitHub's mark, filled rather than stroked: it is their logo, not a glyph
 *  drawn on this set's grid. */
export function GitHubIcon({ size = 16, className }: IconProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="currentColor"
      aria-hidden="true"
      focusable="false"
      className={className}
    >
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
    </svg>
  );
}

/* Nav glyphs. */

export function RepositoryIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M3.5 3a1.5 1.5 0 0 1 1.5-1.5h7.5v10H5A1.5 1.5 0 0 0 3.5 13z" />
      <path d="M3.5 13a1.5 1.5 0 0 0 1.5 1.5h7.5v-3" />
    </Icon>
  );
}

/** A commit: one node on the line of history. */
export function CommitIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="8" cy="8" r="2.75" />
      <path d="M2 8h3.25M10.75 8H14" />
    </Icon>
  );
}

/** A session: a transcript of turns. */
export function SessionsIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <rect x="2" y="2.75" width="12" height="10.5" rx="2" />
      <path d="M5 6.5h6M5 9.5h3.5" />
    </Icon>
  );
}

/** A pull request: a branch merging back. */
export function PullRequestIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="4" cy="3.75" r="1.75" />
      <circle cx="4" cy="12.25" r="1.75" />
      <circle cx="12" cy="12.25" r="1.75" />
      <path d="M4 5.5v5" />
      <path d="M12 10.5V7a2 2 0 0 0-2-2H7.5" />
      <path d="m9 3.25-1.5 1.75L9 6.75" />
    </Icon>
  );
}

/* Review preview glyphs. */

/** The whole review: a page of prose, before it is cut into sections. */
export function SummaryIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M3.5 2.5h9v11h-9z" />
      <path d="M6 5.5h4M6 8h4M6 10.5h2.5" />
    </Icon>
  );
}

/** What might bite: the warning triangle, which nothing else in the set uses. */
export function RiskIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M8 2.4 14.6 13.6H1.4z" />
      <path d="M8 6.4v3.1M8 11.7v.1" />
    </Icon>
  );
}

/** One section of a review: a heading with its paragraph under it. */
export function SectionIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M3.5 4.5h9M3.5 8h6.5M3.5 11.5h4" />
    </Icon>
  );
}

/** A toothed wheel, not a disc with rays: the teeth meet the body with no gap,
 *  which is the whole difference between a gear and a sun. */
export function SettingsIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M6.55 3.27 L6.77 1.67 L9.23 1.67 L9.45 3.27 L10.32 3.63 L11.61 2.65 L13.35 4.39 L12.37 5.68 L12.73 6.55 L14.33 6.77 L14.33 9.23 L12.73 9.45 L12.37 10.32 L13.35 11.61 L11.61 13.35 L10.32 12.37 L9.45 12.73 L9.23 14.33 L6.77 14.33 L6.55 12.73 L5.68 12.37 L4.39 13.35 L2.65 11.61 L3.63 10.32 L3.27 9.45 L1.67 9.23 L1.67 6.77 L3.27 6.55 L3.63 5.68 L2.65 4.39 L4.39 2.65 L5.68 3.63 Z" />
      <circle cx="8" cy="8" r="2.2" />
    </Icon>
  );
}

/** The dark theme: a crescent. */
export function MoonIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M13.5 9.6A5.5 5.5 0 0 1 6.4 2.5a5.5 5.5 0 1 0 7.1 7.1Z" />
    </Icon>
  );
}

/** The light theme: the sun. */
export function SunIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="8" cy="8" r="3" />
      <path d="M8 1.5v1.5M8 13v1.5M1.5 8H3M13 8h1.5M3.4 3.4l1.06 1.06M11.54 11.54l1.06 1.06M3.4 12.6l1.06-1.06M11.54 4.46l1.06-1.06" />
    </Icon>
  );
}

/** Follow the system's theme: a screen on its stand. */
export function MonitorIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <rect x="1.75" y="2.5" width="12.5" height="8.5" rx="1" />
      <path d="M5.5 14h5M8 11v3" />
    </Icon>
  );
}

/** Reload what the server holds, without leaving the page. */
export function RefreshIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M13.5 8a5.5 5.5 0 1 1-1.9-4.16" />
      <path d="M13.5 2v3.2h-3.2" />
    </Icon>
  );
}

/** People talking: the team side of the conversation. */
export function TeamChatIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M2.5 4.5a2 2 0 0 1 2-2h7a2 2 0 0 1 2 2v4a2 2 0 0 1-2 2H6.5l-3 3z" />
    </Icon>
  );
}

/** The model's side of the conversation. */
export function SparkleIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M8 2.5 9.3 6.2 13 7.5l-3.7 1.3L8 12.5 6.7 8.8 3 7.5l3.7-1.3z" />
    </Icon>
  );
}

/** CI checks: a check mark inside a ring, so it is not the bare check of a
 *  ticked row. */
export function ChecksIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="8" cy="8" r="5.75" />
      <path d="m5.5 8.2 1.8 1.8L10.8 6.4" />
    </Icon>
  );
}

/** A git branch: a line with a fork off it. */
export function BranchIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="4.5" cy="3.5" r="1.75" />
      <circle cx="4.5" cy="12.5" r="1.75" />
      <circle cx="11.5" cy="5" r="1.75" />
      <path d="M4.5 5.25v5.5M11.5 6.75c0 2.5-7 2-7 4" />
    </Icon>
  );
}

/** An idea: a lightbulb on its screw base. */
export function LightbulbIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M6 11.5c0-1.4-2.25-2.6-2.25-5a4.25 4.25 0 0 1 8.5 0c0 2.4-2.25 3.6-2.25 5Z" />
      <path d="M6.25 13.5h3.5" />
    </Icon>
  );
}

/** A timeline: three events on a spine, each with its entry beside it. */
export function TimelineIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="4" cy="3.5" r="1.25" />
      <circle cx="4" cy="8" r="1.25" />
      <circle cx="4" cy="12.5" r="1.25" />
      <path d="M4 4.75v2M4 9.25v2" />
      <path d="M7.5 3.5h6M7.5 8h4M7.5 12.5h5" />
    </Icon>
  );
}

/** A column chart: three bars standing on a baseline, for analytics. */
export function ChartIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M2.5 13.5h11" />
      <path d="M4.75 11V8M8 11V3.5M11.25 11V6" />
    </Icon>
  );
}

/** A tray: what waits on the person. */
export function InboxIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M2.5 9 4.25 3.5h7.5L13.5 9v3.5a1 1 0 0 1-1 1h-9a1 1 0 0 1-1-1Z" />
      <path d="M2.5 9h3l1 1.5h3l1-1.5h3" />
    </Icon>
  );
}

/** A house: the organisation's home page. */
export function HomeIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M2.5 7 8 2.5 13.5 7" />
      <path d="M4 6v7.5h3V10h2v3.5h3V6" />
    </Icon>
  );
}

/** Changed files: two sheets, one behind the other. */
export function FilesIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M5.5 2.5h4.5l3 3v7a1 1 0 0 1-1 1h-6.5a1 1 0 0 1-1-1v-9a1 1 0 0 1 1-1Z" />
      <path d="M10 2.5v3h3M3 5v8.5a1 1 0 0 0 1 1h6" />
    </Icon>
  );
}

/** Two speech bubbles: activity on GitHub. */
export function CommentsIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M2.5 3.5h7a1 1 0 0 1 1 1v3.5a1 1 0 0 1-1 1H6L3.5 11V9h-1a1 1 0 0 1-1-1V4.5a1 1 0 0 1 1-1Z" />
      <path d="M12 6.5h1.5a1 1 0 0 1 1 1V11a1 1 0 0 1-1 1h-1v2l-2.5-2H8" />
    </Icon>
  );
}

/** A chevron pointing right, for folding a pane away to the right edge. */
export function ChevronRightIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="m6 3.5 4.5 4.5L6 12.5" />
    </Icon>
  );
}

/** Passed: a tick in a ring, for a check that went green. */
export function CircleCheckIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="8" cy="8" r="6" />
      <path d="m5.6 8.2 1.7 1.7 3.2-3.4" />
    </Icon>
  );
}

/** Failed: a cross in a ring. */
export function CircleCrossIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="8" cy="8" r="6" />
      <path d="m6 6 4 4M10 6l-4 4" />
    </Icon>
  );
}

/** Not yet: a broken ring, for a review nobody has given or a check still
 *  running. */
export function CircleDashedIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="8" cy="8" r="6" strokeDasharray="2.4 2.4" />
    </Icon>
  );
}

/** A warning triangle, for something that blocks the work: a conflict. */
export function WarningIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M8 2.5 14 13H2Z" />
      <path d="M8 6.5v3M8 11.2v.1" />
    </Icon>
  );
}

/** A merge: two branches joining, for a pull request ready to go in. */
export function MergeIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="4.5" cy="3.5" r="1.5" />
      <circle cx="4.5" cy="12.5" r="1.5" />
      <circle cx="11.5" cy="8" r="1.5" />
      <path d="M4.5 5v6M4.5 5c0 2.2 2 3 5.5 3" />
    </Icon>
  );
}

/** A stack: two layers, one over the other. */
export function StackIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M8 2.5 14 6 8 9.5 2 6Z" />
      <path d="m2 9.5 6 3.5 6-3.5" />
    </Icon>
  );
}

/** Sliders: settings that tune what a view shows, rather than the gear's
 *  account-wide ones. */
export function SlidersIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M2.5 4h6M12.5 4h1M2.5 8h2M8.5 8h5M2.5 12h8M13.5 12h0" />
      <circle cx="10.5" cy="4" r="1.5" />
      <circle cx="6.5" cy="8" r="1.5" />
      <circle cx="12" cy="12" r="1.5" />
    </Icon>
  );
}

/** A paper plane, for sending a message. */
export function PaperPlaneIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M14.5 1.5 10 14.5 7.3 8.7 1.5 6 14.5 1.5ZM14.5 1.5 7.3 8.7" />
    </Icon>
  );
}

/** Up, for a figure that rose. */
export function ArrowUpIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M8 13V3M4 7l4-4 4 4" />
    </Icon>
  );
}

/** Down, for a figure that fell. */
export function ArrowDownIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M8 3v10M4 9l4 4 4-4" />
    </Icon>
  );
}
