import type { ReactNode } from "react";

/** A face with nothing to say, which still says so plainly. */
export function Quiet({ children }: { children: ReactNode }) {
  return <p className="text-base text-tertiary">{children}</p>;
}

/** What a tile on the review page says in its place when it cannot draw:
 *  one look for native tiles and frames alike. */
export function Note({ children, alert = false }: { children: ReactNode; alert?: boolean }) {
  return (
    <div
      role={alert ? "alert" : undefined}
      className="rounded-md border border-dashed border-default px-3 py-2 text-base text-secondary"
    >
      {children}
    </div>
  );
}
