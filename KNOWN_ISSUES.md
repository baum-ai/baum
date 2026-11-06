# Known issues

Problems we know about and have not fixed yet, with workarounds where there
are any. If yours is not here, [open an issue](https://github.com/baum-ai/baum/issues/new/choose).

## Linux: the desktop app is slow on NVIDIA graphics with Wayland

**Affects:** the AppImage on any distribution, and the `.deb` on systems whose
WebKitGTK is older than 2.54 (every Ubuntu LTS release so far), when the
machine uses an NVIDIA GPU under Wayland.

**What happens:** scrolling, navigation and animations feel slow and sluggish.

**Why:** the desktop app renders its pages with WebKitGTK. On NVIDIA with
Wayland, only WebKitGTK 2.54 and newer can use the GPU renderer there; older
versions fall back to a much slower one. The AppImage carries its own
WebKitGTK (2.50), and the `.deb` uses the system's.

**Workaround:** use the `.deb` on a distribution that ships WebKitGTK 2.54 or
newer. To check the version on a Debian or Ubuntu system, run
`apt-cache policy libwebkit2gtk-4.1-0`. Setting
`WEBKIT_DISABLE_DMABUF_RENDERER=1` does not help: it turns off the renderer
that makes 2.54 fast.

We have only seen this on NVIDIA. If it happens to you on Intel or AMD
graphics, please [open an issue](https://github.com/baum-ai/baum/issues/new/choose).

## Linux: the AppImage does not start with AppImageLauncher 2.x

**Affects:** systems with AppImageLauncher 2.x installed.

**What happens:** opening the AppImage fails with
`Can't open squashfs image: Bad address`.

**Why:** AppImageLauncher 2.x registers a handler for AppImages that cannot
start the AppImage runtime baum uses. Other AppImages built on it, such as
Cursor's, fail the same way.

**Workaround:** upgrade to AppImageLauncher 3.x, which fixed this, or install
the `.deb` instead.
