import { useId, useState } from "react";
import { ChevronRightIcon } from "@baum/viewer/components/icons";
import { SECONDARY } from "./action-classes";
import type { OrganizationAccess } from "./api";
import "./repositories.css";

/** A one-line warning that baum cannot see some of the person's organizations.
 *  Opened, it lists each one with a link to request access on GitHub. GitHub
 *  has no request page per organization: every link opens baum's page in the
 *  person's GitHub settings, which has a Request button beside each one.
 *  `recheck` asks the daemon to look again after an owner approves. */
export function OrganizationAccessNotice({
  access,
  recheck,
}: {
  access?: OrganizationAccess | null;
  recheck: () => Promise<unknown> | void;
}) {
  const [open, setOpen] = useState(false);
  const [checking, setChecking] = useState(false);
  const id = useId();
  if (!access || access.hidden.length === 0) return null;
  const count = access.hidden.length;
  const many = count > 1;
  return (
    <div className="organization-access" role="note">
      <button
        type="button"
        className="organization-access-toggle"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        onClick={() => setOpen((value) => !value)}
      >
        <ChevronRightIcon size={12} className="organization-access-chevron" />
        <span>baum cannot see repositories in {count} of your organizations.</span>
      </button>
      {open ? (
        <div id={id} className="organization-access-body">
          <p>
            {many ? "These organizations restrict" : "This organization restricts"} OAuth app
            access, so GitHub hides {many ? "their" : "its"} repositories until an owner approves
            baum. Request access, then check again once an owner approves.
          </p>
          <ul className="organization-access-list">
            {access.hidden.map((organization) => (
              <li key={organization}>
                <span className="organization-access-name">{organization}</span>
                <a
                  href={access.approval_url}
                  target="_blank"
                  rel="noreferrer"
                  aria-label={`Request access to ${organization} on GitHub`}
                >
                  Request access ↗
                </a>
              </li>
            ))}
          </ul>
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
      ) : null}
    </div>
  );
}
