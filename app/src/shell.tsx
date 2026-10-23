import type { ComponentType, ReactNode } from "react";
import { LogoMark, Wordmark } from "@baum/viewer/components/editorial";
import {
  type IconProps,
  PullRequestIcon,
  RepositoryIcon,
  SettingsIcon,
  SparkleIcon,
} from "@baum/viewer/components/icons";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import { appURL } from "./api";

type Page = "inbox" | "jobs" | "repositories" | "settings";

/** The application chrome, after the hosted sidebar: the mark, the sections
 *  and where the work happens, standing on the sand canvas beside the page's
 *  panel. Below `lg` it folds to its glyphs. */
export function Sidebar({
  page,
  activeJobs,
  problems,
  Link,
}: {
  page: Page;
  activeJobs: number;
  problems: number;
  Link: ComponentType<ViewerLinkProps>;
}) {
  type Item = {
    page: Page;
    label: string;
    href: string;
    icon: ComponentType<IconProps>;
    badge?: ReactNode;
  };
  const main: Item[] = [
    { page: "inbox", label: "Inbox", href: appURL(), icon: PullRequestIcon },
    {
      page: "repositories",
      label: "Repositories",
      href: appURL("?page=repositories"),
      icon: RepositoryIcon,
    },
  ];
  // Jobs and settings are looked at now and then, so they sit at the foot.
  const secondary: Item[] = [
    {
      page: "jobs",
      label: "Jobs",
      href: appURL("?page=jobs"),
      icon: SparkleIcon,
      badge: activeJobs > 0 ? activeJobs : null,
    },
    {
      page: "settings",
      label: "Settings",
      href: appURL("?page=settings"),
      icon: SettingsIcon,
      badge: problems > 0 ? problems : null,
    },
  ];
  const link = ({ page: target, label, href, icon: Icon, badge }: Item) => (
    <Link
      key={target}
      href={href}
      title={label}
      aria-current={page === target ? "page" : undefined}
      className="local-rail-link"
    >
      <Icon size={18} className="local-rail-glyph" />
      <span className="local-rail-label">{label}</span>
      {badge ? (
        <span
          aria-hidden="true"
          className={target === "settings" ? "local-rail-badge warn" : "local-rail-badge"}
        >
          {badge}
        </span>
      ) : null}
    </Link>
  );
  return (
    <aside className="local-rail">
      <Link href={appURL()} className="local-rail-mark" title="Inbox">
        <LogoMark size={24} />
        <Wordmark size="sidebar" className="local-rail-label" />
      </Link>
      <nav aria-label="Main" className="local-rail-nav">
        {main.map(link)}
      </nav>
      <nav aria-label="More" className="local-rail-nav local-rail-foot">
        {secondary.map(link)}
      </nav>
    </aside>
  );
}

/** The bar along the top of the page's panel, as the hosted `PageHeader`:
 *  the page's name with a short line beside it, its controls on the right. */
export function PageBar({
  title,
  detail,
  actions,
}: {
  title: ReactNode;
  detail?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="local-page-bar">
      <div className="local-page-title">
        <h1>{title}</h1>
        {detail ? <p>{detail}</p> : null}
      </div>
      {actions ? <div className="local-page-actions">{actions}</div> : null}
    </header>
  );
}

/** Something worth knowing that is not a failure: a pause, a refresh that
 *  will retry, a problem on the computer. Errors stay `Alert`s. */
export function Notice({ children }: { children: ReactNode }) {
  return (
    <p role="status" className="local-notice">
      {children}
    </p>
  );
}
