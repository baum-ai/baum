// The Chromium engine of `just desktop::profile --engine chromium`: instead of
// the desktop app, Playwright's Chromium (Chrome for Testing, in the
// desktop-isolated image) opens the daemon's pages through chromedriver,
// which speaks the same WebDriver as WebKitWebDriver, so run.ts drives the
// same scenarios. Each measured run records a CPU profile through
// chromedriver's Chrome DevTools Protocol endpoint; cpu.ts reads them.
import { execFileSync } from "node:child_process";
import { readFileSync, statSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { Snapshot } from "../../src/perf-collector.ts";
import {
  clockOffset,
  clockScript,
  cpuSection,
  Symbolizer,
  timeline,
  type CpuProfile,
  type CpuRun,
} from "./cpu.ts";
import { createSession, type Session } from "./webdriver.ts";

/** Where isolated.Dockerfile puts them. */
export const CHROMEDRIVER = "/opt/chromium/chromedriver";
const CHROME = "/opt/chromium/chrome";

/** Microseconds between samples. 250 µs gives a 16 ms frame some 60
 *  samples and a 100 ms interaction 400, enough to tell its stacks apart,
 *  at a few percent of overhead; 100 µs costs V8 noticeably more on long
 *  journeys and makes a review run's profile tens of megabytes. */
export const SAMPLING_INTERVAL = 250;

/** The launch URL the app's window would load: `baum daemon ensure` starts
 *  the instance's daemon if none runs (as the app does) and names its state
 *  directory, whose credential.json holds the URL (the desktop README's
 *  "Attaching to the daemon"). */
export function launchUrl(baum: string): string {
  const out = execFileSync(baum, ["daemon", "ensure", "--json"], {
    encoding: "utf8",
    timeout: 45_000,
    stdio: ["ignore", "pipe", "pipe"],
  });
  const line = out.trim().split("\n").at(-1) ?? "";
  const status = JSON.parse(line) as { state?: string };
  if (!status.state) throw new Error(`baum daemon ensure named no state directory: ${line}`);
  const { url } = JSON.parse(readFileSync(join(status.state, "credential.json"), "utf8")) as {
    url?: string;
  };
  if (!url) throw new Error(`${status.state}/credential.json holds no launch URL`);
  return url;
}

/** A browser session on the daemon's launch URL. A run's profile, when
 *  one is recording, starts before the page loads. */
export async function launchChromium(
  driver: string,
  baum: string,
  cpu: CpuRecorder,
): Promise<Session> {
  const session = await createSession(driver, {
    browserName: "chrome",
    "goog:chromeOptions": {
      binary: CHROME,
      args: [
        "--headless=new",
        // The container has no user namespaces for Chrome's sandbox; it
        // only ever opens the daemon's own pages.
        "--no-sandbox",
        "--disable-dev-shm-usage",
        // The headless instance's screen (BAUM_ISOLATED_WIDTH × HEIGHT).
        `--window-size=${process.env.BAUM_ISOLATED_WIDTH ?? 1440},${process.env.BAUM_ISOLATED_HEIGHT ?? 900}`,
        "--enable-precise-memory-info",
        // Its cache stays in the instance's home between launches, as
        // WebKit's does.
        `--user-data-dir=${join(homedir(), ".cache/baum-profile/chromium")}`,
      ],
    },
  });
  await cpu.relaunched(session);
  await session.navigate(launchUrl(baum));
  return session;
}

/** The session sampled, and whether it was launched during the run. */
type Recording = { session: Session; launched: boolean };

/** Records a CPU profile per measured run and writes the CPU section. */
export class CpuRecorder {
  private readonly runs: CpuRun[] = [];
  private readonly symbolize: Symbolizer;
  private readonly out: string;
  private recording: Recording | undefined;
  version = "";

  constructor(out: string, dist: string | undefined) {
    this.out = out;
    this.symbolize = Symbolizer.forDist(dist);
  }

  /** Starts sampling on `session`'s page. */
  async start(session: Session, launched = false) {
    if (!this.version) {
      const version = (await session.cdp("Browser.getVersion")) as { product?: string };
      this.version = version.product?.replace(/^HeadlessChrome\//, "") ?? "?";
    }
    await session.cdp("Profiler.enable");
    await session.cdp("Profiler.setSamplingInterval", { interval: SAMPLING_INTERVAL });
    await session.cdp("Profiler.start");
    this.recording = { session, launched };
  }

  /** A relaunch (cold start) loses the browser's profile: sampling starts
   *  again on the new one, before it loads the page, and the run counts
   *  from the page's start rather than the collector's. */
  async relaunched(session: Session) {
    if (this.recording) await this.start(session, true);
  }

  /** Ends run `index`'s profile, after its snapshot: lines the clocks up
   *  in the snapshot's page, keeps the profile as run-N.cpuprofile and its
   *  analysis for the summary. */
  async stop(
    index: number,
    snapshot: Snapshot,
    execute: <T>(body: string) => Promise<T>,
  ): Promise<void> {
    const recording = this.recording;
    if (!recording) return;
    this.recording = undefined;
    const [start, end] = await execute<[number, number]>(clockScript());
    const { profile } = (await recording.session.cdp("Profiler.stop")) as { profile: CpuProfile };
    const line = timeline(profile, this.symbolize);
    const offset = clockOffset(line, start, end);
    const file = `run-${index}.cpuprofile`;
    writeFileSync(
      join(this.out, file),
      JSON.stringify(offset === undefined ? profile : { ...profile, baumClockOffset: offset }),
    );
    const size = statSync(join(this.out, file)).size;
    console.log(
      `profile: ${file}, ${(size / 1024 / 1024).toFixed(1)} MiB, ${line.times.length} samples` +
        (offset === undefined ? "; its clock could not be lined up with the page's" : ""),
    );
    this.runs.push({
      index,
      line,
      offset,
      snapshot,
      file,
      ...(recording.launched ? { from: 0 } : {}),
    });
  }

  section(): string {
    return cpuSection(this.runs, SAMPLING_INTERVAL);
  }
}
