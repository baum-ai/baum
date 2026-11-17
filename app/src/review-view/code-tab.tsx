import { InlineMarkdown } from "@baum/viewer/components/markdown";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import pluralize from "pluralize";
import { CodeDiff, type CodeDiffFile, type CodeDiffHunk } from "@baum/viewer/components/code-diff";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { CARD_LABEL } from "@baum/viewer/components/editorial";
import { ChevronDownIcon } from "@baum/viewer/components/icons";
import { JumpLink, TierBadge } from "@baum/viewer/components/review-parts";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import { NavRow } from "./nav-row";
import { Button } from "@baum/viewer/components/ui";
import { indexedHunkFromFilePatch } from "@baum/viewer/patch";
import { hunkUrl, type IndexedHunk, type ReviewDocument } from "@baum/viewer/review-document";
import { LoadingStatus } from "../nine-dots";
import type { ReviewV2 } from "./document";
import {
  FILE_GROUPS,
  notesByHunk,
  selectionOf,
  type HunkNote,
  type ListedSection,
  type ViewFile,
} from "./model";

const basename = (path: string) => path.slice(path.lastIndexOf("/") + 1);
const folder = (path: string) => path.slice(0, Math.max(0, path.lastIndexOf("/")));

/** Code: every changed file, grouped, beside the one open, whose diff carries
 *  the notes on each hunk (the behaviour steps and risks that cite it), each
 *  linking to the section that explains it. Generated files stay folded. */
export function CodeTab({
  document,
  review,
  files,
  file,
  list,
  idPrefix,
  context,
}: {
  document: ReviewDocument;
  review: ReviewV2;
  files: ViewFile[];
  file: ViewFile | undefined;
  list: ListedSection[];
  idPrefix: string;
  context: ReviewPlaceContext;
}) {
  const notes = useMemo(() => notesByHunk(review), [review]);
  const [generatedOpen, setGeneratedOpen] = useState(file?.group === "Generated");
  const open = (path: string) => context.select(selectionOf({ tab: "code", file: path }));
  if (files.length === 0)
    return <p className="text-base text-secondary">This pull request changes no files.</p>;
  return (
    <div className="grid min-w-0 gap-6 @3xl:grid-cols-[minmax(13rem,18rem)_minmax(0,1fr)]">
      <nav aria-label="Changed files" className="min-w-0 space-y-3">
        {FILE_GROUPS.map((group) => {
          const members = files.filter((each) => each.group === group);
          if (members.length === 0) return null;
          const folded = group === "Generated" && !generatedOpen;
          return (
            <div key={group} className="min-w-0">
              {group === "Generated" ? (
                <GeneratedToggle
                  open={!folded}
                  files={members.length}
                  onToggle={() => setGeneratedOpen((value) => !value)}
                  className="px-2.5 pb-1.5"
                />
              ) : (
                <p className={cn(CARD_LABEL, "px-2.5 pb-1.5")}>{group}</p>
              )}
              {folded ? null : (
                <ul className="flex flex-col gap-0.5">
                  {members.map((member) => {
                    const count = member.hunks.reduce(
                      (total, hunk) => total + (notes.get(hunk.id)?.length ?? 0),
                      0,
                    );
                    return (
                      <li key={member.path} className="min-w-0" title={member.path}>
                        <NavRow
                          title={basename(member.path)}
                          line={folder(member.path) || "/"}
                          aside={count > 0 ? pluralize("note", count, true) : undefined}
                          mono
                          active={member.path === file?.path}
                          controls={`${idPrefix}review-view-file`}
                          onSelect={() => open(member.path)}
                        />
                      </li>
                    );
                  })}
                </ul>
              )}
            </div>
          );
        })}
      </nav>
      {file ? (
        <FileView
          key={file.path}
          document={document}
          file={file}
          notes={notes}
          list={list}
          idPrefix={idPrefix}
          context={context}
        />
      ) : null}
    </div>
  );
}

/** The folded group of generated files: "Generated · 2 files". */
export function GeneratedToggle({
  open,
  files,
  onToggle,
  className,
}: {
  open: boolean;
  files: number;
  onToggle: () => void;
  className?: string;
}) {
  return (
    <button
      type="button"
      aria-expanded={open}
      onClick={onToggle}
      className={cn(
        CARD_LABEL,
        "flex w-full cursor-pointer items-center gap-1.5 rounded-sm text-left hover:text-secondary",
        FOCUS_RING,
        className,
      )}
    >
      <ChevronDownIcon size={12} className={cn("transition-transform", !open && "-rotate-90")} />
      Generated · {pluralize("file", files, true)}
    </button>
  );
}

function FileView({
  document,
  file,
  notes,
  list,
  idPrefix,
  context,
}: {
  document: ReviewDocument;
  file: ViewFile;
  notes: Map<string, HunkNote[]>;
  list: ListedSection[];
  idPrefix: string;
  context: ReviewPlaceContext;
}) {
  const [shown, setShown] = useState(file.group !== "Generated");
  const explained = file.sections.flatMap((id) => {
    const entry = list.find((section) => section.id === id);
    return entry ? [entry] : [];
  });
  const where =
    explained.length > 0 ? (
      <p className="text-xs text-secondary">
        Explained in{" "}
        {explained.map((section, at) => (
          <span key={section.id}>
            {at > 0 ? ", " : ""}
            <JumpLink onClick={() => context.select({ kind: "section", id: section.id })}>
              <InlineMarkdown>{section.title}</InlineMarkdown>
            </JumpLink>
          </span>
        ))}
      </p>
    ) : null;
  // The diff's header names the file, with where it is explained under its
  // path; without a diff on show, a heading says both.
  const heading = (
    <div className="space-y-1">
      <h2 className="font-mono text-base font-medium text-primary [overflow-wrap:anywhere]">
        {file.path}
      </h2>
      {where}
    </div>
  );
  return (
    <section
      id={`${idPrefix}review-view-file`}
      aria-label={file.path}
      className="min-w-0 space-y-3"
    >
      {shown ? (
        <FileDiff
          document={document}
          file={file}
          notes={notes}
          context={context}
          header={{ heading, note: where }}
        />
      ) : (
        <>
          {heading}
          <div className="flex flex-wrap items-center gap-3 rounded-md border border-dashed border-default px-3 py-2">
            <p className="text-base text-secondary">This file is generated, so it's folded.</p>
            <Button variant="secondary" size="compact" onClick={() => setShown(true)}>
              Show the diff
            </Button>
          </div>
        </>
      )}
    </section>
  );
}

type Shown = { hunk: IndexedHunk; diff: CodeDiffHunk | null };

const asDiffHunk = (diff: NonNullable<IndexedHunk["diff"]>): CodeDiffHunk => ({
  header: diff.header,
  lines: diff.lines.map((line) => ({
    kind: line.kind,
    text: line.text,
    oldLine: line.kind !== "add" ? line.old_line : undefined,
    newLine: line.kind !== "remove" ? line.new_line : undefined,
  })),
});

/** The file's hunks as the review indexed them, with their notes. A hunk
 *  whose code the review left out is read from the compared version's patch
 *  (not while two versions are compared). A note links to the section that
 *  explains it, or says `noteLink` where the reader already is. */
export function FileDiff({
  document,
  file,
  notes,
  context,
  noteLink = (note) => `${note.sectionTitle} →`,
  header,
}: {
  document: ReviewDocument;
  file: ViewFile;
  notes: Map<string, HunkNote[]>;
  context: ReviewPlaceContext;
  noteLink?: (note: HunkNote) => string;
  /** The file's own heading, for when no diff is drawn, and a note for the
   *  diff's header under its path. */
  header?: { heading: ReactNode; note: ReactNode };
}) {
  const missing = file.hunks.filter((hunk) => !hunk.diff);
  const source = context.hunkPatches;
  const [patch, setPatch] = useState<string | null | undefined>(
    missing.length > 0 && source ? undefined : null,
  );
  const version = document.review.version;
  const first = missing[0]?.id;
  useEffect(() => {
    if (!first || !source) return;
    let current = true;
    setPatch(undefined);
    source(file.path, version, first).then(
      (text) => current && setPatch(text),
      () => current && setPatch(null),
    );
    return () => {
      current = false;
    };
  }, [file.path, version, first, source]);
  const shown: Shown[] = file.hunks.map((hunk) => {
    if (hunk.diff) return { hunk, diff: asDiffHunk(hunk.diff) };
    const loaded = patch ? indexedHunkFromFilePatch(patch, hunk) : null;
    return { hunk, diff: loaded ? asDiffHunk(loaded) : null };
  });
  const drawn = shown.filter((item): item is { hunk: IndexedHunk; diff: CodeDiffHunk } =>
    Boolean(item.diff),
  );
  const lost = shown.filter((item) => !item.diff);
  if (patch === undefined)
    return (
      <>
        {header?.heading}
        <LoadingStatus block>Reading {file.path}…</LoadingStatus>
      </>
    );
  const diff: CodeDiffFile | null =
    drawn.length > 0
      ? {
          sourceIndex: 0,
          path: file.path,
          href: hunkUrl(document, drawn[0]!.hunk),
          additions: drawn.reduce((total, item) => total + item.hunk.additions, 0),
          deletions: drawn.reduce((total, item) => total + item.hunk.deletions, 0),
          hunks: drawn.map((item) => item.diff),
        }
      : null;
  const go = (note: HunkNote) =>
    context.select(
      note.kind === "risk"
        ? { kind: "risk", section: note.section, id: note.id }
        : { kind: "section", id: note.section },
    );
  return (
    <div className="space-y-3">
      {diff ? null : header?.heading}
      {diff ? (
        <CodeDiff
          bare
          files={[diff]}
          className="mt-0"
          headerNote={header?.note ? () => header.note : undefined}
          afterHunk={(_file, index) => {
            const pinned = notes.get(drawn[index]?.hunk.id ?? "");
            return pinned?.length ? <HunkNotes notes={pinned} go={go} link={noteLink} /> : null;
          }}
        />
      ) : null}
      {lost.length > 0 ? (
        <p className="rounded-md border border-dashed border-default px-3 py-2 text-base text-secondary">
          {lost.length === file.hunks.length
            ? "This file's code isn't in the review."
            : `${pluralize("change", lost.length, true)} in this file ${lost.length === 1 ? "isn't" : "aren't"} in the review.`}{" "}
          <a href={hunkUrl(document, lost[0]!.hunk)} target="_blank" rel="noreferrer">
            View on GitHub
          </a>
        </p>
      ) : null}
    </div>
  );
}

function HunkNotes({
  notes,
  go,
  link,
}: {
  notes: HunkNote[];
  go: (note: HunkNote) => void;
  link: (note: HunkNote) => string;
}) {
  return (
    <ul aria-label="Notes" className="space-y-1">
      {notes.map((note) => (
        <li
          key={`${note.kind}:${note.section}:${note.id}`}
          data-note={note.kind}
          className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-base leading-snug"
        >
          {note.kind === "risk" && note.tier ? (
            <TierBadge tier={note.tier} />
          ) : (
            <span className="text-xs font-medium tracking-[0.08em] text-tertiary uppercase">
              Step
            </span>
          )}
          <InlineMarkdown className="min-w-0 text-primary">{note.text}</InlineMarkdown>
          <JumpLink onClick={() => go(note)}>{link(note)}</JumpLink>
        </li>
      ))}
    </ul>
  );
}
