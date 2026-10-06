import { Component, type ReactNode } from "react";
import type { ReviewDocument } from "../../review-document";
import { anchorFile, resolveAnchor } from "../../review-document";
import {
  tileAnchors,
  tileCodeUrl,
  tileRecord,
  validReviewTile,
  withoutRetiredTileFields,
  type ReviewTile,
} from "../../review-tiles";
import { CodeFileLink } from "../code-file-link";
import { AccessModelTile } from "./access-model";
import { ApiContractTile } from "./api-contract";
import { ArchitectureTile } from "./architecture";
import { ContractSchemaDiffTile } from "./contract-schema-diff";
import { WorkflowTile } from "./workflow";
import {
  AlternativeApproachTile,
  BugFixTile,
  PerformanceTile,
  RefactorTile,
} from "./change-kind-tiles";
import { DataMigrationTile, ExternalIntegrationTile, SearchSchemaTile } from "./specialist-tiles";
import type { TileSource } from "./tile-frame";

function sourceFor(tile: ReviewTile, document: ReviewDocument): TileSource {
  return {
    code: tile.code.map((file) => ({
      label: file.path,
      href: tileCodeUrl(document.review.repository, file),
      why: file.why,
      reviewVersion: file.review_version,
      older: file.review_version < document.review.version,
    })),
    codeDiff: tile.code.flatMap((file, sourceIndex) => {
      if (!file.diff) return [];
      const lines = file.diff.hunks.flatMap((hunk) => hunk.lines);
      return [
        {
          sourceIndex,
          path: file.path,
          href: tileCodeUrl(document.review.repository, file),
          additions: lines.filter((line) => line.kind === "add").length,
          deletions: lines.filter((line) => line.kind === "remove").length,
          hunks: file.diff.hunks.map((hunk) => ({
            ...hunk,
            lines: hunk.lines.map((line) => ({
              kind: line.kind,
              text: line.text,
              oldLine: line.kind !== "add" ? line.old_line : undefined,
              newLine: line.kind !== "remove" ? line.new_line : undefined,
            })),
          })),
        },
      ];
    }),
  };
}

function TileContent({ tile, document }: { tile: ReviewTile; document: ReviewDocument }) {
  const source = sourceFor(tile, document);
  switch (tile.kind) {
    case "database-schema":
      return <ContractSchemaDiffTile diff={{ ...tile.data, ...source }} />;
    case "api-contract":
      return (
        <ApiContractTile
          operation={{
            ...tile.data,
            ...source,
            responseStatus: tile.data.response_status,
            breakingChange: tile.data.breaking_change,
          }}
        />
      );
    case "access-model":
      return <AccessModelTile change={{ ...tile.data, ...source }} />;
    case "architecture": {
      // The first entry for an id wins, as it does in the component diagram.
      const names = new Map(tile.data.containers.toReversed().map((node) => [node.id, node.label]));
      return (
        <ArchitectureTile
          change={{
            ...tile.data,
            ...source,
            failurePath: tile.data.failure_path,
            sequences: tile.data.sequences?.map((sequence) => ({
              ...sequence,
              steps: sequence.steps.map((step) => ({
                ...step,
                // An id the tile does not list is shown as itself, not dropped.
                from: names.get(step.from) ?? step.from,
                to: names.get(step.to) ?? step.to,
              })),
            })),
          }}
        />
      );
    }
    case "workflow-state-machine":
      return (
        <WorkflowTile
          change={{
            ...source,
            primaryPath: tile.data.primary_path,
            failurePath: tile.data.failure_path,
            newTransition: tile.data.new_transition,
            retry: tile.data.retry,
            terminalState: tile.data.terminal_state,
          }}
        />
      );
    case "data-migration":
      return (
        <DataMigrationTile
          change={{
            ...tile.data,
            ...source,
            beforeFields: tile.data.before_fields,
            facts: tile.data.facts.map((fact) => ({ ...fact, tone: "neutral" })),
          }}
        />
      );
    case "search-schema":
      return (
        <SearchSchemaTile
          change={{
            ...tile.data,
            ...source,
            queryBefore: tile.data.query_before,
            queryAfter: tile.data.query_after,
            facts: tile.data.facts.map((fact) => ({ ...fact, tone: "neutral" })),
          }}
        />
      );
    case "external-integration":
      return (
        <ExternalIntegrationTile
          change={{
            ...tile.data,
            ...source,
            fallbackTarget: tile.data.fallback_target,
            facts: tile.data.facts.map((fact) => ({ ...fact, tone: "neutral" })),
          }}
        />
      );
    case "bug-fix":
      return <BugFixTile change={{ ...tile.data, ...source }} />;
    case "refactor":
      return <RefactorTile change={{ ...tile.data, ...source }} />;
    case "performance":
      return (
        <PerformanceTile
          change={{ ...tile.data, ...source, raisedOnReview: tile.data.raised_on_review }}
        />
      );
    case "alternative-approach":
      return (
        <AlternativeApproachTile
          change={{ ...tile.data, ...source, whyItMatters: tile.data.why_it_matters }}
        />
      );
  }
}

class TileBoundary extends Component<
  { children: ReactNode; fallback: ReactNode },
  { failed: boolean }
> {
  override state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  override render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

function UnavailableTile({ kind }: { kind?: string }) {
  return (
    <details className="rounded-(--radius-md) border border-(--border-default) p-3">
      <summary className="cursor-pointer text-base">
        Tile unavailable{kind ? ` (${kind})` : ""}
      </summary>
      <p className="mt-2 text-base text-(--text-secondary)">
        This tile type or its data is not supported. The rest of the review is still available.
      </p>
    </details>
  );
}

/** Only the owner selects these entries; rendering uses its exact document snapshot. */
export function ReviewTiles({ document, tiles }: { document: ReviewDocument; tiles?: unknown }) {
  if (tiles === undefined || (Array.isArray(tiles) && tiles.length === 0)) return null;
  if (!Array.isArray(tiles)) return <UnavailableTile />;
  const ids = tiles.map((tile) => tileRecord(tile)?.id);
  const duplicates = new Set(ids.filter((id, index) => ids.indexOf(id) !== index));
  return (
    <div className="min-w-0 space-y-3 [overflow-wrap:anywhere]" aria-label="Review tiles">
      {tiles.map((stored, position) => {
        const value = withoutRetiredTileFields(stored);
        const item = tileRecord(value);
        const kind = typeof item?.kind === "string" ? item.kind : undefined;
        const fallback = <UnavailableTile kind={kind} />;
        const anchors = tileAnchors(value);
        const unresolved = anchors.some((anchor) =>
          resolveAnchor(document, anchor).kind.startsWith("Unresolved"),
        );
        return (
          <div
            key={`${document.review.version}:${position}:${String(item?.id ?? "invalid")}`}
            className="min-w-0 space-y-2"
          >
            {validReviewTile(value, document) && !duplicates.has(value.id) ? (
              <TileBoundary key={JSON.stringify(value)} fallback={fallback}>
                <TileContent tile={value} document={document} />
              </TileBoundary>
            ) : (
              fallback
            )}

            <ul
              aria-label="Tile sources"
              className="flex flex-wrap gap-2 text-xs text-(--text-tertiary)"
            >
              {anchors.map((anchor, index) => {
                const resolved = resolveAnchor(document, anchor);
                const href =
                  resolved.href && /^https?:\/\//i.test(resolved.href) ? resolved.href : undefined;
                const file = anchorFile(document, anchor);
                return (
                  <li key={index} title={resolved.kind} className="min-w-0">
                    {file ? (
                      <CodeFileLink
                        path={file.path}
                        line={file.line}
                        href={href}
                        className="text-(--text-link) hover:underline"
                      >
                        {resolved.label}
                      </CodeFileLink>
                    ) : href ? (
                      <a
                        href={href}
                        target="_blank"
                        rel="noreferrer"
                        className="text-(--text-link)"
                      >
                        {resolved.label}
                      </a>
                    ) : (
                      <span>{resolved.label}</span>
                    )}
                  </li>
                );
              })}
            </ul>

            {unresolved ? (
              <p className="text-base text-(--status-caution)">
                Some tile sources could not be resolved.
              </p>
            ) : null}
          </div>
        );
      })}
    </div>
  );
}
