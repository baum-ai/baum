"""Writes the Finder layout of the macOS disk image into macos/:

    dmg.DS_Store         the window: size, icon size, icon positions and the
                         background picture
    dmg-background.tiff  the background picture, an arrow from baum.app to
                         the Applications link, at 1x and 2x for Retina

scripts/macos-finalize.sh copies them into the image root as .DS_Store and
.background.tiff, so the image opens as a small window with large icons,
baum.app on the left, the Applications link on the right, the arrow between
them, and no toolbar, sidebar or status bar. Rerun this when the layout, the
app's name or the link's name changes:

    just desktop::dmg-layout

The recipe installs the pinned packages it needs in a container.

Finder finds the background through an alias. Aliases are normally made on
macOS from the mounted file; this one is built by hand from what is fixed
about the image (the volume name, the root folder and the file's path), so
Finder resolves it by path whenever the volume's creation date and the
file's catalog ID differ from the build's, which is every build.
"""

import datetime
import os
import sys

import numpy
import tifffile
from ds_store import DSStore
from mac_alias import ALIAS_FIXED_DISK, ALIAS_KIND_FILE, Alias, TargetInfo, VolumeInfo
from PIL import Image, ImageDraw

VOLUME = "baum"
BACKGROUND = ".background.tiff"

# The window's position on screen and the size of its content, which the
# background fills, and the icon centres inside it. Finder puts each label
# under its icon.
WINDOW = {"x": 200, "y": 120, "width": 540, "height": 360}
ICON_SIZE = 128
ICONS = {
    "baum.app": (140, 160),
    "Applications": (400, 160),
}
BACKGROUND_COLOUR = (255, 255, 255)
ARROW_COLOUR = (150, 150, 150)
# HFS+ gives the root folder catalog ID 2.
ROOT_CNID = 2


def arrow(scale: int) -> Image.Image:
    """The background at scale × the window size, drawn 4× larger and
    reduced so its edges are smooth."""
    s = 4 * scale
    width, height = WINDOW["width"] * s, WINDOW["height"] * s
    image = Image.new("RGB", (width, height), BACKGROUND_COLOUR)
    draw = ImageDraw.Draw(image)
    (left_x, y), (right_x, _) = ICONS["baum.app"], ICONS["Applications"]
    gap = ICON_SIZE / 2 + 22
    start, end = left_x + gap, right_x - gap
    head = 22
    thickness = 6
    draw.rounded_rectangle(
        [start * s, (y - thickness / 2) * s, (end - head / 2) * s, (y + thickness / 2) * s],
        radius=thickness / 2 * s,
        fill=ARROW_COLOUR,
    )
    draw.polygon(
        [
            ((end - head) * s, (y - head * 0.7) * s),
            (end * s, y * s),
            ((end - head) * s, (y + head * 0.7) * s),
        ],
        fill=ARROW_COLOUR,
    )
    return image.resize((WINDOW["width"] * scale, WINDOW["height"] * scale), Image.LANCZOS)


def write_background(path: str) -> None:
    # Two images in one TIFF, as tiffutil -cathidpicheck makes it: Finder
    # shows the 144 dpi one on Retina displays.
    with tifffile.TiffWriter(path) as tiff:
        for scale in (1, 2):
            tiff.write(
                numpy.asarray(arrow(scale)),
                photometric="rgb",
                compression="zlib",
                resolution=(72 * scale, 72 * scale),
                resolutionunit="INCH",
            )


def background_alias() -> bytes:
    epoch = datetime.datetime(2001, 1, 1, tzinfo=datetime.timezone.utc)
    alias = Alias()
    alias.volume = VolumeInfo(VOLUME, epoch, b"H+", ALIAS_FIXED_DISK, 0, b"\0\0")
    alias.volume.posix_path = "/Volumes/" + VOLUME
    alias.target = TargetInfo(
        ALIAS_KIND_FILE, BACKGROUND, ROOT_CNID, 0, epoch, b"\0\0\0\0", b"\0\0\0\0"
    )
    alias.target.folder_name = VOLUME
    alias.target.posix_path = "/" + BACKGROUND
    alias.target.carbon_path = VOLUME + ":" + BACKGROUND
    alias.target.cnid_path = []
    return alias.to_bytes()


def write_store(path: str) -> None:
    if os.path.exists(path):
        os.remove(path)
    with DSStore.open(path, "w+") as store:
        root = store["."]
        root["vSrn"] = ("long", 1)
        root["icvl"] = ("type", b"icnv")
        root["bwsp"] = {
            "WindowBounds": "{{%d, %d}, {%d, %d}}"
            % (WINDOW["x"], WINDOW["y"], WINDOW["width"], WINDOW["height"]),
            "ContainerShowSidebar": False,
            "PreviewPaneVisibility": False,
            "ShowPathbar": False,
            "ShowSidebar": False,
            "ShowStatusBar": False,
            "ShowTabView": False,
            "ShowToolbar": False,
            "SidebarWidth": 0,
        }
        root["icvp"] = {
            "viewOptionsVersion": 1,
            "arrangeBy": "none",
            "backgroundType": 2,
            "backgroundImageAlias": background_alias(),
            "backgroundColorRed": 1.0,
            "backgroundColorGreen": 1.0,
            "backgroundColorBlue": 1.0,
            "gridOffsetX": 0.0,
            "gridOffsetY": 0.0,
            "gridSpacing": 100.0,
            "iconSize": float(ICON_SIZE),
            "labelOnBottom": True,
            "scrollPositionX": 0.0,
            "scrollPositionY": 0.0,
            "showIconPreview": True,
            "showItemInfo": False,
            "textSize": 13.0,
        }
        for name, position in ICONS.items():
            store[name]["Iloc"] = position
        # Keep the hidden files off screen should someone show hidden files.
        for name in (BACKGROUND, ".VolumeIcon.icns"):
            store[name]["Iloc"] = (1000, 1000)


def main() -> None:
    out = sys.argv[1] if len(sys.argv) > 1 else "macos"
    write_background(os.path.join(out, "dmg-background.tiff"))
    write_store(os.path.join(out, "dmg.DS_Store"))
    print(f"dmg-layout: wrote {out}/dmg.DS_Store and {out}/dmg-background.tiff")


if __name__ == "__main__":
    main()
