// The app `just desktop::profile` drives: the unpackaged Electron app from
// this checkout, which Electron's own chromedriver (of the app's Electron
// version, in the desktop-isolated image) launches and drives over W3C
// WebDriver, so run.ts's scenarios work the real window from the connecting
// screen on to the daemon's pages. Each measured run records a CPU profile
// through chromedriver's Chrome DevTools Protocol endpoint; cpu.ts reads them.
import { statSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
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

/** Where isolated.Dockerfile puts it. */
export const CHROMEDRIVER = "/opt/electron-chromedriver/chromedriver";
/** chromedriver's arguments: only severe messages, so no command (a launch
 *  URL holds the daemon's capability) reaches driver.log. */
export const CHROMEDRIVER_ARGS = ["--log-level=SEVERE"];

/** Microseconds between samples. 250 µs gives a 16 ms frame some 60
 *  samples and a 100 ms interaction 400, enough to tell its stacks apart,
 *  at a few percent of overhead; 100 µs costs V8 noticeably more on long
 *  journeys and makes a review run's profile tens of megabytes. */
export const SAMPLING_INTERVAL = 250;

/** The Electron binary and version the app at `app` (its package
 *  directory, oss/desktop) installs. */
export function electron(app: string): { binary: string; version: string } {
  const require = createRequire(join(app, "package.json"));
  return {
    binary: require("electron") as string,
    version: (require("electron/package.json") as { version: string }).version,
  };
}

/** Launches the app at `app` with the configuration directory `config`
 *  (Electron's userData, where desktop.json is). A run's profile, when one
 *  is recording, starts as soon as the window can be driven, on the
 *  connecting screen. */
export async function launchElectron(
  driver: string,
  app: string,
  config: string,
  cpu: CpuRecorder,
): Promise<Session> {
  const session = await createSession(driver, {
    "goog:chromeOptions": {
      binary: electron(app).binary,
      args: [
        // Electron runs the app in this directory; chromedriver's own last
        // argument (a blank page's URL) then goes unread.
        `--app=${app}`,
        // The container has no user namespaces for Chromium's sandbox; the
        // app only opens its own screens and the daemon's pages.
        "--no-sandbox",
        // chromedriver reads the DevTools port from the user data directory
        // it names here. The app sets its own (userData and sessionData)
        // before Chromium starts, and Electron writes the port file to
        // sessionData, so this names that: chromium/ under `config`.
        `--user-data-dir=${join(config, "chromium")}`,
        "--enable-precise-memory-info",
      ],
    },
  });
  await cpu.relaunched(session);
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
      this.version = version.product?.replace(/^(Headless)?Chrome\//, "") ?? "?";
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
    if (line.times.length === 0)
      console.error(`profile: ${file} holds no samples; the run's CPU section is empty`);
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
