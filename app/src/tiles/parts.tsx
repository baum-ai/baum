import type { ReactNode } from "react";

/** A face with nothing to say, which still says so plainly. */
export function Quiet({ children }: { children: ReactNode }) {
  return <p className="text-base text-tertiary">{children}</p>;
}
