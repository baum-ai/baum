/** The access model as the tile reads it, made self-consistent first. The
 *  schema cannot state the model's invariants — a grant is a subset of who
 *  holds the capability, a revocation is not, and every id names a listed
 *  actor — so data that breaks them would otherwise produce contradictions
 *  such as "Gains access: Bob" beside "Still blocked: Bob". */

type ActorLike = { id: string };
type CapabilityLike = {
  id: string;
  allowed: readonly string[];
  granted?: readonly string[];
  revoked?: readonly string[];
};

const unique = <T>(items: readonly T[]) => [...new Set(items)];

export function normalizeAccess<
  A extends ActorLike,
  C extends CapabilityLike,
  M extends { actors: readonly A[]; capabilities: readonly C[] },
>(model: M): { model: M; ignored: number } {
  let ignored = 0;
  const seenActors = new Set<string>();
  const actors = model.actors.filter((actor) => {
    if (seenActors.has(actor.id)) {
      ignored++;
      return false;
    }
    seenActors.add(actor.id);
    return true;
  });
  const known = (id: string) => seenActors.has(id);
  const seenCapabilities = new Set<string>();
  const capabilities: C[] = [];
  for (const capability of model.capabilities) {
    if (seenCapabilities.has(capability.id)) {
      ignored++;
      continue;
    }
    seenCapabilities.add(capability.id);
    const allowed = unique(capability.allowed.filter(known));
    const held = new Set(allowed);
    // A grant to someone who does not hold the capability, or a revocation from
    // someone who still does, cannot both be true; neither is shown.
    const granted = unique((capability.granted ?? []).filter((id) => held.has(id)));
    const revoked = unique((capability.revoked ?? []).filter((id) => known(id) && !held.has(id)));
    ignored +=
      capability.allowed.length -
      allowed.length +
      (capability.granted?.length ?? 0) -
      granted.length +
      (capability.revoked?.length ?? 0) -
      revoked.length;
    capabilities.push({ ...capability, allowed, granted, revoked });
  }
  return { model: { ...model, actors, capabilities }, ignored };
}
