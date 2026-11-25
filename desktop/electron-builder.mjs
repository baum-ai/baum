// electron-builder configuration for the desktop app's bundles. Run it only
// through scripts/bundle.sh, which stages the bundled baum in
// target/electron-stage/bin, sets the version and the release file names, and
// never signs or publishes. See "Releases" in README.md.
import { fileURLToPath } from "node:url";

const here = fileURLToPath(new URL(".", import.meta.url));

/** @type {import("electron-builder").Configuration} */
export default {
  appId: "com.usebaum.desktop",
  productName: "baum",
  executableName: "baum-desktop",
  copyright: "Copyright (c) 2026 Baum AI Ltd",
  // The package's metadata as the bundles show it (the deb needs a homepage
  // and a maintainer); bundle.sh adds the version.
  extraMetadata: {
    description: "Keep up with what your coding agents build",
    homepage: "https://usebaum.com",
    author: { name: "Baum AI Ltd" },
  },
  directories: {
    output: "target/electron-builder",
    // Not build/, which holds the bundled main process.
    buildResources: "macos",
  },
  files: ["build/*.cjs", "dist/**/*", "icons/icon.png", "icons/tray/*.png"],
  extraResources: [
    // The bundled baum, outside the asar archive so it can run:
    // bin/baum (bin/baum.exe on Windows), daemon.bundledCliPath.
    { from: "target/electron-stage/bin", to: "bin" },
    { from: "../LICENSE", to: "LICENSE" },
    { from: "THIRD_PARTY_NOTICES.txt", to: "THIRD_PARTY_NOTICES.txt" },
  ],
  // No native modules to rebuild.
  npmRebuild: false,
  // electron-updater reads the update files these describe; the app sets the
  // URL per channel at run time. Nothing is ever published from here
  // (bundle.sh passes --publish never); the files are written beside the
  // bundles.
  publish: {
    provider: "generic",
    url: "https://github.com/baum-ai/baum/releases/latest/download/",
  },
  // The update files are latest*.yml for every version: a release candidate
  // (1.2.3-rc.1) reads them from its own channel's feed (updater.ts), not
  // from an rc-linux.yml electron-builder would otherwise name after the
  // pre-release.
  detectUpdateChannel: false,
  mac: {
    target: "dir",
    category: "public.app-category.developer-tools",
    icon: "icons/icon.icns",
    minimumSystemVersion: "12.0",
    // scripts/macos-finalize.sh signs the app, in a release job that builds
    // nothing.
    identity: null,
    hardenedRuntime: true,
    entitlements: "macos/entitlements.plist",
    entitlementsInherit: "macos/entitlements.plist",
    notarize: false,
    // The bundled baum is already universal (lipo), the same file in both
    // architectures' apps, so @electron/universal keeps it as it is.
    x64ArchFiles: "Contents/Resources/bin/baum",
    // On Linux and Windows Electron's licences come beside the executable;
    // on macOS electron-builder takes only Electron.app.
    extraResources: [
      { from: "node_modules/electron/dist/LICENSE", to: "LICENSE.electron.txt" },
      { from: "node_modules/electron/dist/LICENSES.chromium.html", to: "LICENSES.chromium.html" },
    ],
  },
  linux: {
    category: "Development",
    icon: "icons/icon.png",
    synopsis: "Keep up with what your coding agents build",
    description: "Keep up with what your coding agents build",
    maintainer: "Baum AI Ltd",
    vendor: "Baum AI Ltd",
  },
  deb: {
    // /usr/bin/baum is the bundled baum, as the app's setup expects of the
    // deb ("Where the command goes" in README.md). The package owns the link,
    // so removing the package removes it.
    fpm: [`${here}target/electron-stage/deb/baum=/usr/bin/baum`],
  },
  win: {
    target: "nsis",
    icon: "icons/icon.ico",
  },
  nsis: {
    oneClick: true,
    perMachine: false,
    deleteAppDataOnUninstall: false,
    include: "windows/installer.nsh",
  },
};
