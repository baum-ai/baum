import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  checkExecutable,
  createAutostart,
  DESKTOP_FILE,
  desktopEntry,
  desktopExecQuote,
  entryDisabled,
  installed,
  isDevelopmentBuild,
  launchAgent,
  linuxEntryPath,
  regAddArgs,
  regApprovedDeleteArgs,
  regApprovedQueryArgs,
  regDeleteArgs,
  regQueryArgs,
  removeEntry,
  rewriteEntry,
  runValue,
  startupDisabled,
  writeEntry,
} from "./autostart.ts";

function tempDir(): string {
  return mkdtempSync(join(tmpdir(), "baum-autostart-"));
}

describe("autostart entries", () => {
  it("the linux entry starts the app hidden", () => {
    expect(desktopEntry("/usr/bin/baum-desktop")).toBe(
      "[Desktop Entry]\nType=Application\nName=baum\n" +
        "Comment=Keep up with what your coding agents build\n" +
        "Exec=/usr/bin/baum-desktop --hidden\nTerminal=false\n" +
        "X-GNOME-Autostart-enabled=true\n",
    );
  });

  it("exec quoting follows the desktop entry specification", () => {
    expect(desktopExecQuote("/usr/bin/baum")).toBe("/usr/bin/baum");
    expect(desktopExecQuote("/opt/a b/baum.AppImage")).toBe('"/opt/a b/baum.AppImage"');
    expect(desktopExecQuote("/opt/100%/baum")).toBe("/opt/100%%/baum");
    expect(desktopExecQuote("/o/$x/b")).toBe('"/o/\\\\$x/b"');
    expect(desktopExecQuote('/o/"q"/b')).toBe('"/o/\\\\"q\\\\"/b"');
    expect(desktopExecQuote("/o/a\\b")).toBe('"/o/a\\\\\\\\b"');
  });

  it("the launch agent runs the app hidden at login", () => {
    const plist = launchAgent("/Applications/baum.app/Contents/MacOS/baum");
    expect(plist).toContain("<key>Label</key>\n\t<string>com.usebaum.desktop</string>");
    expect(plist).toContain(
      "<array>\n\t\t<string>/Applications/baum.app/Contents/MacOS/baum</string>\n\t\t<string>--hidden</string>\n\t</array>",
    );
    expect(plist).toContain("<key>RunAtLoad</key>\n\t<true/>");
    expect(plist.startsWith('<?xml version="1.0" encoding="UTF-8"?>')).toBe(true);
    expect(plist.trimEnd().endsWith("</plist>")).toBe(true);
  });

  it("the launch agent escapes the path", () => {
    expect(launchAgent("/Apps/a&b <c>/baum")).toContain(
      "<string>/Apps/a&amp;b &lt;c&gt;/baum</string>",
    );
  });

  it("the run key holds the quoted executable and flag", () => {
    const exe = String.raw`C:\Users\A B\AppData\Local\baum\baum-desktop.exe`;
    expect(runValue(exe)).toBe(`"${exe}" --hidden`);
    expect(regAddArgs(exe)).toEqual([
      "add",
      String.raw`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
      "/v",
      "baum",
      "/t",
      "REG_SZ",
      "/d",
      `"${exe}" --hidden`,
      "/f",
    ]);
    expect(regDeleteArgs()).toEqual([
      "delete",
      String.raw`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
      "/v",
      "baum",
      "/f",
    ]);
    expect(regQueryArgs()[0]).toBe("query");
  });

  it("entries are written, replaced and removed", () => {
    const dir = tempDir();
    const path = join(dir, "autostart", DESKTOP_FILE);
    writeEntry(path, "one");
    expect(readFileSync(path, "utf8")).toBe("one");
    writeEntry(path, "two");
    expect(readFileSync(path, "utf8")).toBe("two");
    expect(readdirSync(dirname(path))).toHaveLength(1);
    removeEntry(path);
    expect(readdirSync(dirname(path))).toHaveLength(0);
    removeEntry(path);
    rmSync(dir, { recursive: true });
  });

  const GNOME_DISABLED =
    "[Desktop Entry]\nType=Application\nName=baum\nExec=/old/baum --hidden\nX-GNOME-Autostart-enabled=false\nX-GNOME-Autostart-Delay=7\n\n[Desktop Action x]\nName=Other\nHidden=true\n";

  it("a desktop environment disable is seen", () => {
    expect(entryDisabled(GNOME_DISABLED)).toBe(true);
    expect(entryDisabled("[Desktop Entry]\nHidden=true\n")).toBe(true);
    expect(entryDisabled("[Desktop Entry]\nHidden = TRUE\n")).toBe(true);
    expect(entryDisabled("[Desktop Entry]\nHidden=false\nX-GNOME-Autostart-enabled=true\n")).toBe(
      false,
    );
    // Keys in other groups do not count.
    expect(entryDisabled("[Desktop Entry]\nName=baum\n[Desktop Action x]\nHidden=true\n")).toBe(
      false,
    );
    expect(entryDisabled(desktopEntry("/usr/bin/baum-desktop"))).toBe(false);
  });

  it("rewriting updates exec and keeps everything else", () => {
    const rewritten = rewriteEntry(GNOME_DISABLED, "/new/baum");
    expect(rewritten).toBe(
      "[Desktop Entry]\nType=Application\nName=baum\nExec=/new/baum --hidden\nX-GNOME-Autostart-enabled=true\nX-GNOME-Autostart-Delay=7\n\n[Desktop Action x]\nName=Other\nHidden=true\n",
    );
    expect(entryDisabled(rewritten)).toBe(false);
    // A group lacking the keys gets them; a file without the group is replaced.
    expect(rewriteEntry("[Desktop Entry]\nName=mine\n", "/b")).toBe(
      "[Desktop Entry]\nName=mine\nExec=/b --hidden\nX-GNOME-Autostart-enabled=true\n",
    );
    expect(rewriteEntry("garbage", "/b")).toBe(desktopEntry("/b"));
  });

  it("a path with a line break is refused", () => {
    expect(() => desktopEntry("/a\nExec=/evil")).toThrow();
    expect(() => rewriteEntry("[Desktop Entry]\n", "/a\rb")).toThrow();
  });

  it("task manager disables are read from StartupApproved", () => {
    const value = (hex: string) => `\n HKEY\\...\\Run\n    baum    REG_BINARY    ${hex}\n\n`;
    expect(startupDisabled(value("020000000000000000000000"))).toBe(false);
    expect(startupDisabled(value("060000000000000000000000"))).toBe(false);
    expect(startupDisabled(value("030000007C3B5D1A2E9CD801"))).toBe(true);
    expect(startupDisabled(value("070000000000000000000000"))).toBe(true);
    expect(startupDisabled("")).toBe(false);
    expect(regApprovedQueryArgs()[1]).toContain("StartupApproved");
    expect(regApprovedDeleteArgs().at(-1)).toBe("/f");
  });

  it("the entry names a real installed path", () => {
    expect(checkExecutable("/opt/baum/baum-desktop (deleted)")).toBe("/opt/baum/baum-desktop");
    expect(checkExecutable("/usr/bin/baum-desktop")).toBe("/usr/bin/baum-desktop");
    expect(() =>
      checkExecutable("/private/var/folders/x/AppTranslocation/ABC/d/baum.app/Contents/MacOS/baum"),
    ).toThrow(/Applications/);
    expect(installed("/usr/bin/baum-desktop", false)).toBe(true);
    expect(installed("/usr/bin/baum-desktop", true)).toBe(false);
    expect(installed("/oss/desktop/target/debug/baum-desktop", false)).toBe(false);
    expect(installed("/oss/desktop/target/release/baum-desktop", false)).toBe(false);
    expect(installed("/opt/target/baum-desktop", false)).toBe(true);
  });

  it("a development build is unpackaged or run with BAUM_DESKTOP_BAUM", () => {
    expect(isDevelopmentBuild(true, {})).toBe(false);
    expect(isDevelopmentBuild(false, {})).toBe(true);
    expect(isDevelopmentBuild(true, { BAUM_DESKTOP_BAUM: "/x/baum" })).toBe(true);
    expect(isDevelopmentBuild(true, { BAUM_DESKTOP_BAUM: "" })).toBe(true);
  });

  it("the linux entry lives under the xdg config directory", () => {
    expect(linuxEntryPath("/cfg", "/srv/u")).toBe("/cfg/autostart/baum.desktop");
    expect(linuxEntryPath(undefined, "/srv/u")).toBe("/srv/u/.config/autostart/baum.desktop");
    // A relative XDG_CONFIG_HOME is ignored, as the specification says.
    expect(linuxEntryPath("cfg", "/srv/u")).toBe("/srv/u/.config/autostart/baum.desktop");
    expect(linuxEntryPath(undefined, undefined)).toBeNull();
  });
});

describe("start at login", () => {
  function linux(env: Record<string, string> = {}, isPackaged = true) {
    const home = tempDir();
    const entry = join(home, ".config/autostart", DESKTOP_FILE);
    const autostart = createAutostart({
      platform: "linux",
      env,
      home,
      exe: "/opt/baum/baum-desktop",
      isPackaged,
    });
    return { autostart, entry };
  }

  it("turns on and off on linux, and an AppImage names its own path", () => {
    const { autostart, entry } = linux({ APPIMAGE: "/home/alice/baum.AppImage" });
    expect(autostart.enabled()).toBe(false);
    autostart.set(true);
    expect(readFileSync(entry, "utf8")).toContain("Exec=/home/alice/baum.AppImage --hidden\n");
    expect(autostart.enabled()).toBe(true);
    autostart.set(false);
    expect(autostart.enabled()).toBe(false);
  });

  it("an entry the desktop turned off is off, and turning it on keeps other keys", () => {
    const { autostart, entry } = linux();
    mkdirSync(dirname(entry), { recursive: true });
    writeFileSync(entry, "[Desktop Entry]\nName=baum\nExec=/old\nHidden=true\nX-Custom=keep\n");
    expect(autostart.enabled()).toBe(false);
    autostart.set(true);
    expect(autostart.enabled()).toBe(true);
    expect(readFileSync(entry, "utf8")).toBe(
      "[Desktop Entry]\nName=baum\nExec=/opt/baum/baum-desktop --hidden\nX-Custom=keep\nX-GNOME-Autostart-enabled=true\n",
    );
  });

  it("refresh rewrites an enabled entry but never an off one", () => {
    const { autostart, entry } = linux();
    mkdirSync(dirname(entry), { recursive: true });
    writeFileSync(entry, "[Desktop Entry]\nExec=/old --hidden\n");
    autostart.refresh();
    expect(readFileSync(entry, "utf8")).toContain("Exec=/opt/baum/baum-desktop --hidden\n");

    const off = "[Desktop Entry]\nExec=/old --hidden\nX-GNOME-Autostart-enabled=false\n";
    writeFileSync(entry, off);
    autostart.refresh();
    expect(readFileSync(entry, "utf8")).toBe(off);
  });

  it("refresh writes nothing for development builds", () => {
    for (const [env, isPackaged] of [
      [{}, false],
      [{ BAUM_DESKTOP_BAUM: "/x" }, true],
    ] as const) {
      const { autostart, entry } = linux(env, isPackaged);
      mkdirSync(dirname(entry), { recursive: true });
      writeFileSync(entry, "[Desktop Entry]\nExec=/old --hidden\n");
      autostart.refresh();
      expect(readFileSync(entry, "utf8")).toBe("[Desktop Entry]\nExec=/old --hidden\n");
      // The user's own toggle still writes one.
      autostart.set(true);
      expect(readFileSync(entry, "utf8")).toContain("/opt/baum/baum-desktop --hidden");
    }
  });

  it("a misplaced macOS app cannot turn it on", () => {
    const home = tempDir();
    const autostart = createAutostart({
      platform: "darwin",
      env: {},
      home,
      exe: "/Volumes/baum/baum.app/Contents/MacOS/baum",
      isPackaged: true,
      misplaced: true,
    });
    expect(() => autostart.set(true)).toThrow(/Applications/);
  });

  it("macOS writes the launch agent", () => {
    const home = tempDir();
    const autostart = createAutostart({
      platform: "darwin",
      env: {},
      home,
      exe: "/Applications/baum.app/Contents/MacOS/baum",
      isPackaged: true,
    });
    expect(autostart.enabled()).toBe(false);
    autostart.set(true);
    expect(
      readFileSync(join(home, "Library/LaunchAgents/com.usebaum.desktop.plist"), "utf8"),
    ).toContain("<string>--hidden</string>");
    expect(autostart.enabled()).toBe(true);
    autostart.set(false);
    expect(autostart.enabled()).toBe(false);
  });

  it("Windows sets the Run key and reads a StartupApproved disable as off", () => {
    const calls: string[][] = [];
    let present = false;
    let approved = "";
    const autostart = createAutostart({
      platform: "win32",
      env: {},
      home: "C:\\Users\\a",
      exe: String.raw`C:\baum\baum-desktop.exe`,
      isPackaged: true,
      reg(args) {
        calls.push(args);
        const [verb, key] = args;
        if (key?.endsWith("StartupApproved\\Run")) {
          if (verb === "delete") approved = "";
          return { ok: verb === "delete" || approved !== "", stdout: approved };
        }
        if (verb === "add") present = true;
        if (verb === "delete") present = false;
        return { ok: verb === "add" || present || verb === "delete", stdout: "" };
      },
    });
    autostart.set(true);
    expect(
      calls.some((a) => a[0] === "add" && a.includes(`"C:\\baum\\baum-desktop.exe" --hidden`)),
    ).toBe(true);
    expect(autostart.enabled()).toBe(true);
    approved = "    baum    REG_BINARY    030000000000000000000000";
    expect(autostart.enabled()).toBe(false);
    autostart.set(true);
    expect(autostart.enabled()).toBe(true);
    autostart.set(false);
    expect(present).toBe(false);
  });
});
