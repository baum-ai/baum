import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  copyIfChanged,
  entryPathIn,
  menuEntry,
  refresh,
  runningAsAppImage,
  starts,
  update,
} from "./menu-entry.ts";

const ENTRY_FILE = "baum-appimage.desktop";

function tempDir(): string {
  return mkdtempSync(join(tmpdir(), "baum-menu-entry-"));
}

describe("applications menu entry", () => {
  it("the entry starts the AppImage and hides once it is gone", () => {
    expect(
      menuEntry(
        "/home/user/Applications/baum.AppImage",
        "/home/user/.local/share/com.usebaum.desktop/baum.png",
      ),
    ).toBe(
      "[Desktop Entry]\nType=Application\nName=baum\n" +
        "Comment=Keep up with what your coding agents build\n" +
        "Exec=/home/user/Applications/baum.AppImage\n" +
        "TryExec=/home/user/Applications/baum.AppImage\n" +
        "Icon=/home/user/.local/share/com.usebaum.desktop/baum.png\n" +
        "Terminal=false\nCategories=Development;\nStartupWMClass=baum-desktop\n",
    );
  });

  it("paths are quoted for exec and escaped elsewhere", () => {
    const entry = menuEntry("/opt/a b/x\\y.AppImage");
    expect(entry).toContain('Exec="/opt/a b/x\\\\\\\\y.AppImage"\n');
    expect(entry).toContain("TryExec=/opt/a b/x\\\\y.AppImage\n");
    expect(entry).not.toContain("Icon=");
    expect(() => menuEntry("/tmp/a\nExec=evil")).toThrow();
  });

  it("the entry goes in the xdg data directory", () => {
    expect(entryPathIn("/data", "/home/user")).toBe("/data/applications/baum-appimage.desktop");
    // A relative XDG_DATA_HOME is invalid and ignored.
    expect(entryPathIn("data", "/home/user")).toBe(
      "/home/user/.local/share/applications/baum-appimage.desktop",
    );
    expect(entryPathIn(undefined, undefined)).toBeNull();
  });

  it("update writes and moves the entry but keeps a hidden one", () => {
    const dir = tempDir();
    const path = join(dir, "applications", ENTRY_FILE);
    update(path, "/a/baum.AppImage");
    expect(readFileSync(path, "utf8")).toContain("Exec=/a/baum.AppImage\n");

    update(path, "/b/baum.AppImage");
    expect(readFileSync(path, "utf8")).toContain("Exec=/b/baum.AppImage\n");

    for (const hide of ["NoDisplay=true", "Hidden=true"]) {
      const kept = `[Desktop Entry]\nExec=/b/baum.AppImage\n${hide}\n`;
      writeFileSync(path, kept);
      update(path, "/c/baum.AppImage");
      expect(readFileSync(path, "utf8")).toBe(kept);
    }
    rmSync(dir, { recursive: true });
  });

  it("an integration tool's entry replaces ours", () => {
    const dir = tempDir();
    const apps = join(dir, "applications");
    const path = join(apps, ENTRY_FILE);
    const image = "/home/user/.appimage-applications/baum.AppImage";
    update(path, image);
    expect(existsSync(path)).toBe(true);

    // An entry for another AppImage changes nothing.
    writeFileSync(
      join(apps, "appimagekit_1-Other.desktop"),
      "[Desktop Entry]\nExec=/home/user/other.AppImage %F\n",
    );
    update(path, image);
    expect(existsSync(path)).toBe(true);

    // AppImageLauncher names the AppImage in TryExec and Exec.
    const theirs = join(apps, "appimagekit_2-baum.desktop");
    writeFileSync(theirs, `[Desktop Entry]\nName=baum\nExec=${image} %U\nTryExec=${image}\n`);
    update(path, image);
    expect(existsSync(path)).toBe(false);
    update(path, image);
    expect(existsSync(path)).toBe(false);

    // Once it is gone, ours comes back.
    rmSync(theirs);
    update(path, image);
    expect(existsSync(path)).toBe(true);

    // A hidden entry of theirs leaves ours in place.
    const startsImage = `Exec=${image}\n`;
    writeFileSync(theirs, `[Desktop Entry]\n${startsImage}NoDisplay=true\n`);
    update(path, image);
    expect(existsSync(path)).toBe(true);

    // Ours, once the user hid it, is kept even beside a visible one.
    const oursHidden = `[Desktop Entry]\n${startsImage}Hidden=true\n`;
    writeFileSync(path, oursHidden);
    writeFileSync(theirs, `[Desktop Entry]\n${startsImage}`);
    update(path, image);
    expect(readFileSync(path, "utf8")).toBe(oursHidden);
    rmSync(dir, { recursive: true });
  });

  it("an entry starts the AppImage by TryExec or its Exec program", () => {
    const image = "/home/user/a b/x\\y.AppImage";
    const ours = menuEntry(image);
    expect(starts(ours, image)).toBe(true);
    // Our escaping round-trips through Exec alone and TryExec alone.
    const only = (key: string) =>
      ours
        .split("\n")
        .filter((l) => l === "[Desktop Entry]" || l.startsWith(key))
        .join("\n");
    expect(starts(only("Exec="), image)).toBe(true);
    expect(starts(only("TryExec="), image)).toBe(true);
    expect(starts('[Desktop Entry]\nExec="/opt/a b.AppImage" %F\n', "/opt/a b.AppImage")).toBe(
      true,
    );
    expect(starts("[Desktop Entry]\nTryExec=/opt/a\\sb\n", "/opt/a b")).toBe(true);
    expect(starts("[Desktop Entry]\nExec=/opt/50%%.AppImage\n", "/opt/50%.AppImage")).toBe(true);
    // Another program, another group, or the AppImage only as an argument.
    expect(starts("[Desktop Entry]\nExec=/opt/b.AppImage\n", "/opt/a.AppImage")).toBe(false);
    expect(starts("[Desktop Action x]\nExec=/opt/a.AppImage\n", "/opt/a.AppImage")).toBe(false);
    expect(
      starts(
        "[Desktop Entry]\nExec=/usr/lib/appimagelauncher/remove /opt/a.AppImage\n",
        "/opt/a.AppImage",
      ),
    ).toBe(false);
  });

  it("the icon is copied only when it changed", () => {
    const dir = tempDir();
    const from = join(dir, "in.png");
    const to = join(dir, "out/baum.png");
    writeFileSync(from, "one");
    copyIfChanged(from, to);
    expect(readFileSync(to, "utf8")).toBe("one");
    writeFileSync(from, "two");
    copyIfChanged(from, to);
    expect(readFileSync(to, "utf8")).toBe("two");
    expect(() => copyIfChanged(join(dir, "missing.png"), to)).toThrow();
    rmSync(dir, { recursive: true });
  });

  it("refresh runs only from an installed AppImage", () => {
    const home = tempDir();
    const appdir = join(home, "mount");
    mkdirSync(appdir);
    writeFileSync(join(appdir, "baum-desktop.png"), "icon");
    const base = {
      isPackaged: true,
      home,
      exe: join(appdir, "baum-desktop"),
      dataDir: join(home, "data"),
      env: { APPIMAGE: join(home, "baum.AppImage"), APPDIR: appdir },
    };
    const entry = join(home, ".local/share/applications", ENTRY_FILE);

    refresh({ ...base, isPackaged: false });
    refresh({ ...base, env: { ...base.env, BAUM_DESKTOP_BAUM: "/x" } });
    refresh({ ...base, exe: "/usr/bin/baum-desktop" });
    expect(existsSync(entry)).toBe(false);

    expect(runningAsAppImage(base.env, base.exe)).toBe(true);
    refresh(base);
    expect(readFileSync(entry, "utf8")).toContain(`Exec=${join(home, "baum.AppImage")}\n`);
    expect(readFileSync(entry, "utf8")).toContain(`Icon=${join(home, "data/baum.png")}\n`);
    rmSync(home, { recursive: true });
  });
});
