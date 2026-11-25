# Makes the app's icons from what scripts/rasterize-icons.mjs drew in
# target/: icons/icon.png (512 × 512, Linux and the window), icons/icon.icns
# (macOS, every size from 16 to 1024), icons/icon.ico (Windows, 16 to 256)
# and the tray icons in icons/tray. Run from oss/desktop with Pillow
# (`just desktop::icons`).
import shutil

from PIL import Image

source = Image.open("target/icon-source.png").convert("RGBA")
source.resize((512, 512), Image.LANCZOS).save("icons/icon.png")
source.save("icons/icon.icns")
source.save("icons/icon.ico", sizes=[(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (256, 256)])
for state in ("idle", "running", "attention"):
    shutil.copyfile(f"target/tray-{state}.png", f"icons/tray/{state}.png")
