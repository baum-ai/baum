import pluralize from "pluralize";
import { LiveDot } from "@baum/viewer/components/ui";

/** An aggregate tile's note that it is not complete yet: changes still being
 *  written add their risks, evidence and the rest once they are. */
export function StillWriting({ count }: { count: number }) {
  if (count === 0) return null;
  return (
    <p className="flex items-center gap-2 text-xs text-tertiary">
      <LiveDot />
      {pluralize("change", count, true)} still being written
    </p>
  );
}
