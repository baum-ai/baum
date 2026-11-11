import {
  lazy,
  Suspense,
  type ComponentProps,
  type ComponentType,
  type LazyExoticComponent,
} from "react";
import { LoadingStatus } from "./nine-dots";

// Pages other than the first one shown load on demand, so starting the app
// parses only what the inbox needs. The fallback is the same block status the
// pages themselves show while their data loads, so the swap does not shift
// the layout.
function inSuspense<Props extends object>(
  Page: LazyExoticComponent<ComponentType<Props>>,
  label: string,
) {
  return function LazyPage(props: Props) {
    return (
      <Suspense fallback={<LoadingStatus block>{label}</LoadingStatus>}>
        <Page {...(props as ComponentProps<typeof Page>)} />
      </Suspense>
    );
  };
}

export const AnalyticsPage = inSuspense(
  lazy(() => import("./analytics").then((m) => ({ default: m.AnalyticsPage }))),
  "Loading analytics…",
);
export const TimelinePage = inSuspense(
  lazy(() => import("./timeline").then((m) => ({ default: m.TimelinePage }))),
  "Loading timeline…",
);
export const SessionTranscriptPage = inSuspense(
  lazy(() => import("./session-transcript").then((m) => ({ default: m.SessionTranscriptPage }))),
  "Loading session…",
);
export const JobPage = inSuspense(
  lazy(() => import("./job-transcript").then((m) => ({ default: m.JobPage }))),
  "Loading job…",
);
export const GroupPage = inSuspense(
  lazy(() => import("./group").then((m) => ({ default: m.GroupPage }))),
  "Loading review group…",
);
export const LocalReview = inSuspense(
  lazy(() => import("./review").then((m) => ({ default: m.LocalReview }))),
  "Loading review…",
);
