// Turns the frames video/demo.spec.ts recorded into the website's video:
// an H.264 MP4 at 30 frames a second, played SPEED times as fast as the
// browser showed it, the first frame as the poster, and where each chapter
// starts in it. Needs ffmpeg.
//
//   node video/encode.mjs <output directory> [name]
//
// The name, baum-app unless given, names the files: <name>.mp4,
// <name>-poster.jpg and <name>-chapters.ts; the dark recording is
// baum-app-dark.
import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

/** How many times faster than real time the video plays. */
const SPEED = 1.6;

const frames = join(import.meta.dirname, "frames");
const out = resolve(process.argv[2] ?? join(import.meta.dirname, "out"));
const name = process.argv[3] ?? "baum-app";
const {
  frames: list,
  chapters,
  sidebar,
  end,
} = JSON.parse(readFileSync(join(frames, "frames.json"), "utf8"));
if (list.length === 0) throw new Error("no frames were recorded");

// The concat demuxer's list: every frame with how long it stays, its time on
// screen over SPEED, the last one until the recording stopped. ffmpeg
// repeats the last entry's file.
const lines = ["ffconcat version 1.0"];
list.forEach((frame, i) => {
  const next = i + 1 < list.length ? list[i + 1].time : end;
  const duration = Math.max((next - frame.time) / SPEED, 0.001);
  lines.push(`file '${frame.file}'`, `duration ${duration.toFixed(4)}`);
});
lines.push(`file '${list.at(-1).file}'`);
writeFileSync(join(frames, "frames.ffconcat"), `${lines.join("\n")}\n`);

const ffmpeg = (...args) =>
  execFileSync("ffmpeg", ["-hide_banner", "-loglevel", "error", "-y", ...args], {
    stdio: "inherit",
  });
ffmpeg(
  "-f",
  "concat",
  "-safe",
  "0",
  "-i",
  join(frames, "frames.ffconcat"),
  "-vf",
  "fps=30,scale=trunc(iw/2)*2:trunc(ih/2)*2,format=yuv420p",
  "-c:v",
  "libx264",
  "-preset",
  "slow",
  "-crf",
  "24",
  "-tune",
  "animation",
  "-movflags",
  "+faststart",
  "-an",
  join(out, `${name}.mp4`),
);
ffmpeg("-i", join(frames, list[0].file), "-q:v", "3", join(out, `${name}-poster.jpg`));

// Each chapter's start in the video, and where its sidebar link is from each
// time on, for the website's click targets over the video's sidebar.
const at = (time) => Number(Math.max((time - list[0].time) / SPEED, 0).toFixed(2));
const starts = chapters.map(
  ({ title, time }) => `  { title: ${JSON.stringify(title)}, start: ${at(time)} },`,
);
const layouts = sidebar.map(({ time, links }) => {
  const boxes = links.map(
    ({ title, x, y, width, height }) =>
      `      { title: ${JSON.stringify(title)}, x: ${Math.round(x)}, y: ${Math.round(y)}, width: ${Math.round(width)}, height: ${Math.round(height)} },`,
  );
  return `  {\n    start: ${at(time)},\n    links: [\n${boxes.join("\n")}\n    ],\n  },`;
});
writeFileSync(
  join(out, `${name}-chapters.ts`),
  `// Written by oss/app/video/encode.mjs: where each part of ${name}.mp4
// starts, in seconds, and where in its 1056 by 800 frame the sidebar link
// that opens it is, from each time on.
export const chapters = [
${starts.join("\n")}
];

export const sidebar = [
${layouts.join("\n")}
];
`,
);
console.log(`wrote ${join(out, `${name}.mp4`)}, the poster and the chapters`);
