import { useState } from "react";
import { SECONDARY } from "./action-classes";
import type { OrganizationAccess } from "./api";
import "./repositories.css";

/** "acme", "acme and beta", "acme, beta and gamma", then "acme, beta, gamma and 2 more". */
export function organizationNames(names: string[]) {
  if (names.length <= 3)
    return names.length < 2
      ? names.join("")
      : `${names.slice(0, -1).join(", ")} and ${names.at(-1)}`;
  return `${names.slice(0, 3).join(", ")} and ${names.length - 3} more`;
}

/** Says which organizations baum cannot see and where a member asks an owner
 *  to approve it. `recheck` asks the daemon to look again after approval. */
export function OrganizationAccessNotice({
  access,
  recheck,
}: {
  access?: OrganizationAccess | null;
  recheck: () => Promise<unknown> | void;
}) {
  const [checking, setChecking] = useState(false);
  if (!access || access.hidden.length === 0) return null;
  const many = access.hidden.length > 1;
  return (
    <div className="organization-access" role="note">
      <p>
        <strong>baum can't see repositories in {organizationNames(access.hidden)}.</strong>{" "}
        {many ? "These organizations restrict" : "This organization restricts"} OAuth app access, so
        GitHub hides {many ? "their" : "its"} repositories until an owner approves baum.
      </p>
      <p>
        <a href={access.approval_url} target="_blank" rel="noreferrer">
          Request approval on GitHub ↗
        </a>
        , then check again once an owner approves.
      </p>
      <button
        type="button"
        className={SECONDARY}
        disabled={checking}
        onClick={() => {
          setChecking(true);
          void Promise.resolve(recheck()).finally(() => setChecking(false));
        }}
      >
        {checking ? "Checking…" : "Check again"}
      </button>
    </div>
  );
}
