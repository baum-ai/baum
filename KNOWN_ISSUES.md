# Known issues

Problems we know about and have not fixed yet, with workarounds where there
are any. If yours is not here, [open an issue](https://github.com/baum-ai/baum/issues/new/choose).

## Linux: the AppImage does not start with AppImageLauncher 2.x

**Affects:** systems with AppImageLauncher 2.x installed.

**What happens:** opening the AppImage fails with
`Can't open squashfs image: Bad address`.

**Why:** AppImageLauncher 2.x registers a handler for AppImages that cannot
start the AppImage runtime baum uses. Other AppImages built on it, such as
Cursor's, fail the same way.

**Workaround:** upgrade to AppImageLauncher 3.x, which fixed this, or install
the `.deb` instead.
