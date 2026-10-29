import type { ComponentType } from "react";
import {
  ChartIcon,
  type IconProps,
  InboxIcon,
  PullRequestIcon,
  RepositoryIcon,
  SessionsIcon,
  SettingsIcon,
  SparkleIcon,
  TimelineIcon,
} from "@baum/viewer/components/icons";
import { appURL } from "./api";

export type Page =
  | "inbox"
  | "pulls"
  | "timeline"
  | "sessions"
  | "analytics"
  | "jobs"
  | "repositories"
  | "settings";

/** The sidebar's pages in its order, which is also the order of their keys
 *  (⌘1 to ⌘8 on a Mac, Ctrl+1 to Ctrl+8 elsewhere) and of the Go to list.
 *  Jobs and settings are looked at now and then, so they sit at the foot. */
export const PAGES: {
  page: Page;
  label: string;
  href: () => string;
  icon: ComponentType<IconProps>;
  foot?: boolean;
}[] = [
  { page: "inbox", label: "Inbox", href: () => appURL(), icon: InboxIcon },
  {
    page: "pulls",
    label: "Pull requests",
    href: () => appURL("?page=pulls"),
    icon: PullRequestIcon,
  },
  {
    page: "timeline",
    label: "Timeline",
    href: () => appURL("?page=timeline"),
    icon: TimelineIcon,
  },
  {
    page: "sessions",
    label: "Sessions",
    href: () => appURL("?page=sessions"),
    icon: SessionsIcon,
  },
  {
    page: "analytics",
    label: "Analytics",
    href: () => appURL("?page=analytics"),
    icon: ChartIcon,
  },
  {
    page: "repositories",
    label: "Repositories",
    href: () => appURL("?page=repositories"),
    icon: RepositoryIcon,
  },
  {
    page: "jobs",
    label: "Jobs",
    href: () => appURL("?page=jobs"),
    icon: SparkleIcon,
    foot: true,
  },
  {
    page: "settings",
    label: "Settings",
    href: () => appURL("?page=settings"),
    icon: SettingsIcon,
    foot: true,
  },
];
