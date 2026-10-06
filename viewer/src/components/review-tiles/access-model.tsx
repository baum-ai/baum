import { cn } from "../classnames";
import { Badge } from "../ui";

import { normalizeAccess } from "./access-model-data";
import { accessModelDefinition } from "./catalogue";
import { TileDisclosure, TileGlance, TileShell } from "./tile-frame";
import type { CodeDiffFile } from "./code-diff";
import type { TileSourceFile } from "./tile-code-links";

export type AccessActor = {
  id: string;
  label: string;
  /** The state the actor must also be in, when the role alone does not decide. */
  context?: string;
};

export type AccessCapability = {
  id: string;
  label: string;
  /** A shorter form for the per-actor rollup. Defaults to `label`. */
  short?: string;
  detail?: string;
  /** Who holds this after the change, most privileged first. */
  allowed: readonly string[];
  /** Who gained it here. A subset of `allowed`. */
  granted?: readonly string[];
  /** Who lost it here. None of these appear in `allowed`. */
  revoked?: readonly string[];
};

export type AccessModelChange = {
  overview?: {
    headline: string;
    description: string;
  };
  /** Every actor the tile reasons about, most privileged first. */
  actors: readonly AccessActor[];
  capabilities: readonly AccessCapability[];
  code: readonly TileSourceFile[];
  codeDiff: readonly CodeDiffFile[];
};

function actorName(actor: AccessActor): string {
  return actor.context ? `${actor.label} · ${actor.context}` : actor.label;
}

function grantedCount(change: AccessModelChange): number {
  return change.capabilities.reduce(
    (total, capability) => total + (capability.granted?.length ?? 0),
    0,
  );
}

function revokedCount(change: AccessModelChange): number {
  return change.capabilities.reduce(
    (total, capability) => total + (capability.revoked?.length ?? 0),
    0,
  );
}

function hasChange(capability: AccessCapability): boolean {
  return (capability.granted?.length ?? 0) > 0 || (capability.revoked?.length ?? 0) > 0;
}

/** Who held a capability before this change: everyone who holds it now and
 *  did not just gain it, plus everyone who just lost it, in actor order. */
function heldBefore(capability: AccessCapability, order: readonly AccessActor[]): AccessActor[] {
  const granted = new Set(capability.granted ?? []);
  const before = new Set([
    ...capability.allowed.filter((id) => !granted.has(id)),
    ...(capability.revoked ?? []),
  ]);
  return order.filter((actor) => before.has(actor.id));
}

function names(actors: readonly AccessActor[]): string {
  return actors.map(actorName).join(", ");
}

/** The tile answers one question first — did this pull request change who can
 *  do what? — and only goes into detail when the answer is yes. An unchanged
 *  model is said so plainly with no access listing or per-actor rollup beneath
 *  it; its code and sources still show, as on every tile, so the claim that
 *  nothing changed can be checked.
 *  A changed one shows each changed capability as current access beside new
 *  access. */
export function AccessModelTile({ change: raw }: { change: AccessModelChange }) {
  const { model: change, ignored } = normalizeAccess(raw);
  const actors = new Map(change.actors.map((actor) => [actor.id, actor]));
  const moved = change.capabilities.filter(hasChange);
  const untouched = change.capabilities.filter((capability) => !hasChange(capability));
  const changed = moved.length > 0;

  return (
    <TileShell
      title={accessModelDefinition.title}
      source={{ code: change.code, codeDiff: change.codeDiff }}
    >
      <AccessAtAGlance change={change} />
      {ignored > 0 ? (
        <p className="mt-2 text-xs text-(--text-tertiary)">
          {ignored === 1 ? "One entry" : `${ignored} entries`} in the review contradicted its own
          access list (a grant to someone without the capability, a revocation from someone who
          keeps it, or an unlisted actor) and {ignored === 1 ? "is" : "are"} not shown.
        </p>
      ) : null}

      {changed ? (
        <>
          <section aria-label="Access changes" className="mt-3">
            <h5 className="text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
              Access changes
            </h5>
            <ul className="mt-2 grid gap-2">
              {moved.map((capability) => (
                <li key={capability.id}>
                  <ChangedCapability
                    capability={capability}
                    actors={actors}
                    order={change.actors}
                  />
                </li>
              ))}
            </ul>
          </section>

          {untouched.length > 0 ? (
            <TileDisclosure
              label="Unchanged capabilities"
              summary={`${untouched.length} ${untouched.length === 1 ? "capability" : "capabilities"} this pull request leaves as they were`}
            >
              <CurrentAccess capabilities={untouched} actors={actors} order={change.actors} />
            </TileDisclosure>
          ) : null}

          <EffectiveAccess change={change} />
        </>
      ) : null}
    </TileShell>
  );
}

function AccessAtAGlance({ change }: { change: AccessModelChange }) {
  const granted = grantedCount(change);
  const revoked = revokedCount(change);
  const unchanged = granted === 0 && revoked === 0;

  if (unchanged) {
    return (
      <TileGlance
        label="Access at a glance"
        headline="Access is unchanged by this pull request"
        description={change.overview?.description ?? "No one gains or loses any capability."}
        badges={[
          <Badge key="unchanged" size="compact">
            No access change
          </Badge>,
        ]}
      />
    );
  }

  const headline =
    change.overview?.headline ??
    (granted > 0 && revoked > 0
      ? `${granted} new ${granted === 1 ? "grant" : "grants"} and ${revoked} ${revoked === 1 ? "revocation" : "revocations"}`
      : granted > 0
        ? `${granted} new ${granted === 1 ? "grant" : "grants"}`
        : `${revoked} ${revoked === 1 ? "revocation" : "revocations"}`);

  return (
    <TileGlance
      label="Access at a glance"
      headline={headline}
      description={
        change.overview?.description ??
        "Each change below shows current access on the left and new access on the right."
      }
      badges={[
        <Badge key="changed" tone={revoked > 0 ? "critical" : "positive"} size="compact">
          Access changes
        </Badge>,
        granted > 0 && (
          <Badge key="granted" tone="positive" size="compact">
            {granted} new {granted === 1 ? "grant" : "grants"}
          </Badge>
        ),
        revoked > 0 && (
          <Badge key="revoked" tone="critical" size="compact">
            {revoked} {revoked === 1 ? "revocation" : "revocations"}
          </Badge>
        ),
        <Badge key="capabilities" size="compact">
          {change.capabilities.length}{" "}
          {change.capabilities.length === 1 ? "capability" : "capabilities"}
        </Badge>,
        <Badge key="actors" size="compact">
          {change.actors.length} {change.actors.length === 1 ? "actor" : "actors"}
        </Badge>,
      ]}
    />
  );
}

/** A changed capability as a reviewer checks it: current access on the left,
 *  new access on the right, and in words exactly who gained and who lost it. */
function ChangedCapability({
  capability,
  actors,
  order,
}: {
  capability: AccessCapability;
  actors: Map<string, AccessActor>;
  order: readonly AccessActor[];
}) {
  const granted = new Set(capability.granted ?? []);
  const revoked = new Set(capability.revoked ?? []);
  const allowed = new Set(capability.allowed);
  const before = heldBefore(capability, order);
  const after = capability.allowed
    .map((id) => actors.get(id))
    .filter((actor): actor is AccessActor => actor !== undefined);
  const gained = order.filter((actor) => granted.has(actor.id));
  const lost = order.filter((actor) => revoked.has(actor.id));
  const stillBlocked = order.filter((actor) => !allowed.has(actor.id) && !revoked.has(actor.id));

  return (
    <article
      aria-label={capability.label}
      className="overflow-hidden rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)"
    >
      <div className="flex flex-wrap items-center gap-2 border-b border-(--border-subtle) bg-(--surface-wash) px-3 py-2">
        <h6 className="min-w-0 text-base font-semibold text-(--text-primary)">
          {capability.label}
        </h6>
        <Badge tone={revoked.size > 0 ? "critical" : "positive"} size="compact">
          {revoked.size > 0 && granted.size > 0
            ? "Granted and revoked"
            : revoked.size > 0
              ? "Revoked"
              : "Newly granted"}
        </Badge>
      </div>

      {capability.detail && (
        <p className="border-b border-(--border-subtle) px-3 py-2 text-xs leading-[1.45] text-(--text-secondary)">
          {capability.detail}
        </p>
      )}

      <div className="grid gap-px bg-(--border-subtle) sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)]">
        <BeforeAfter
          label="Current access"
          entries={before.map((actor) => ({
            actor,
            mark: revoked.has(actor.id) ? ("lost" as const) : undefined,
          }))}
        />
        <span
          aria-hidden
          className="hidden items-center bg-(--surface-card) px-1 text-(--text-tertiary) sm:flex"
        >
          →
        </span>
        <BeforeAfter
          label="New access"
          entries={after.map((actor) => ({
            actor,
            mark: granted.has(actor.id) ? ("gained" as const) : undefined,
          }))}
        />
      </div>

      <dl className="grid gap-1 border-t border-(--border-subtle) px-3 py-2 text-xs leading-[1.45]">
        {gained.length > 0 && (
          <div className="flex flex-wrap gap-x-2">
            <dt className="font-semibold text-(--text-primary)">Gains access:</dt>
            <dd className="text-(--text-secondary)">{names(gained)}</dd>
          </div>
        )}
        {lost.length > 0 && (
          <div className="flex flex-wrap gap-x-2">
            <dt className="font-semibold text-(--text-primary)">Loses access:</dt>
            <dd className="text-(--text-secondary)">{names(lost)}</dd>
          </div>
        )}
        {stillBlocked.length > 0 && (
          <div className="flex flex-wrap gap-x-2">
            <dt className="font-semibold text-(--text-primary)">Still blocked:</dt>
            <dd className="text-(--text-secondary)">{names(stillBlocked)}</dd>
          </div>
        )}
      </dl>
    </article>
  );
}

function BeforeAfter({
  label,
  entries,
}: {
  label: string;
  entries: readonly { actor: AccessActor; mark?: ActorMark }[];
}) {
  return (
    <div className="min-w-0 bg-(--surface-card) px-3 py-2">
      <span className="text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
        {label}
      </span>
      {entries.length === 0 ? (
        <p className="mt-1.5 text-xs text-(--text-tertiary)">Nobody</p>
      ) : (
        <ul className="mt-1.5 flex flex-wrap gap-1.5">
          {entries.map((entry) => (
            <ActorChip key={entry.actor.id} actor={entry.actor} mark={entry.mark} />
          ))}
        </ul>
      )}
    </div>
  );
}

/** Capabilities left as they were: one line each, who can and who cannot. */
function CurrentAccess({
  capabilities,
  actors,
  order,
}: {
  capabilities: readonly AccessCapability[];
  actors: Map<string, AccessActor>;
  order: readonly AccessActor[];
}) {
  return (
    <ul className="divide-y divide-(--border-subtle) pt-1">
      {capabilities.map((capability) => {
        const allowed = new Set(capability.allowed);
        return (
          <li key={capability.id} className="grid gap-1 px-3 py-2.5">
            <span className="text-base font-medium text-(--text-primary)">{capability.label}</span>
            {capability.detail ? (
              <span className="text-xs text-(--text-secondary)">{capability.detail}</span>
            ) : null}
            <ActorRow
              bare
              label="Can"
              empty="Nobody"
              entries={capability.allowed
                .map((id) => actors.get(id))
                .filter((actor): actor is AccessActor => actor !== undefined)
                .map((actor) => ({ actor }))}
            />
            <ActorRow
              bare
              label="Cannot"
              empty="Nobody is blocked"
              entries={order.filter((actor) => !allowed.has(actor.id)).map((actor) => ({ actor }))}
            />
          </li>
        );
      })}
    </ul>
  );
}

type ActorMark = "gained" | "lost";

function ActorRow({
  label,
  empty,
  entries,
  bare = false,
}: {
  label: string;
  empty: string;
  entries: readonly { actor: AccessActor; mark?: ActorMark }[];
  /** Inside a list row that already has its own padding and dividers. */
  bare?: boolean;
}) {
  return (
    <div
      className={cn(
        "grid gap-1 sm:grid-cols-[4.5rem_minmax(0,1fr)] sm:items-baseline sm:gap-3",
        bare ? "py-0.5" : "border-b border-(--border-subtle) px-3 py-2 last:border-b-0",
      )}
    >
      <span className="text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
        {label}
      </span>
      {entries.length === 0 ? (
        <span className="text-xs text-(--text-tertiary)">{empty}</span>
      ) : (
        <ul className="flex flex-wrap gap-1.5">
          {entries.map((entry) => (
            <ActorChip key={entry.actor.id} actor={entry.actor} mark={entry.mark} />
          ))}
        </ul>
      )}
    </div>
  );
}

/** The mark carries the delta in text, so colour is never the only signal. */
function ActorChip({ actor, mark }: { actor: AccessActor; mark?: ActorMark }) {
  return (
    <li
      className={cn(
        "inline-flex items-center gap-1.5 rounded-(--radius-sm) border border-(--border-default) bg-(--surface-wash) px-2 py-0.5 text-xs",
        mark ? "text-(--text-primary)" : "text-(--text-secondary)",
      )}
    >
      <span className="min-w-0">{actorName(actor)}</span>
      {mark && (
        <span
          className={cn(
            "flex-none text-3xs font-semibold tracking-[0.08em]",
            mark === "gained" ? "text-(--status-positive)" : "text-(--status-critical)",
          )}
        >
          {mark === "gained" ? "+ NEW" : "− LOST"}
        </span>
      )}
    </li>
  );
}

/** Derived from the same capabilities, so the two readings cannot disagree. */
function EffectiveAccess({ change }: { change: AccessModelChange }) {
  return (
    <TileDisclosure
      label="Effective access by actor"
      summary="Everything each actor can do after this change"
    >
      <ul aria-label="Effective access by actor" className="divide-y divide-(--border-subtle) pt-1">
        {change.actors.map((actor) => {
          const held = change.capabilities.filter((capability) =>
            capability.allowed.includes(actor.id),
          );
          return (
            <li
              key={actor.id}
              className="grid gap-1 px-3 py-2.5 sm:grid-cols-[11rem_minmax(0,1fr)] sm:items-baseline sm:gap-3"
            >
              <span className="text-base font-medium text-(--text-primary)">
                {actorName(actor)}
              </span>
              {held.length === 0 ? (
                <span className="text-xs text-(--text-tertiary)">No access</span>
              ) : (
                <span className="flex flex-wrap gap-1.5">
                  {held.map((capability) => (
                    <span
                      key={capability.id}
                      className={cn(
                        "inline-flex items-center gap-1.5 rounded-(--radius-sm) border border-(--border-default) bg-(--surface-wash) px-2 py-0.5 text-xs",
                        capability.granted?.includes(actor.id)
                          ? "text-(--text-primary)"
                          : "text-(--text-secondary)",
                      )}
                    >
                      {capability.short ?? capability.label}
                      {capability.granted?.includes(actor.id) && (
                        <span className="flex-none text-3xs font-semibold tracking-[0.08em] text-(--status-positive)">
                          + NEW
                        </span>
                      )}
                    </span>
                  ))}
                </span>
              )}
            </li>
          );
        })}
      </ul>
    </TileDisclosure>
  );
}
