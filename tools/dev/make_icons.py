"""生成可安装应用（PWA）的图标：深色底上一个白色方块（和顶栏左上角的标志一样）。
方块在中间 40% 以内，安卓裁成圆形、圆角时也完整（maskable 的安全区是中间 80%）。
用法：python tools/dev/make_icons.py   （要 Pillow；生成到 web/public/）
"""
import os

from PIL import Image

BG = (10, 12, 16)  # #0A0C10，和 index.html 的 theme-color 一样
FG = (255, 255, 255)
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "web", "public")


def icon(size: int, mark: float) -> Image.Image:
    im = Image.new("RGB", (size, size), BG)
    s = round(size * mark)
    o = (size - s) // 2
    im.paste(FG, (o, o, o + s, o + s))
    return im


os.makedirs(OUT, exist_ok=True)
for name, size, mark in [("icon-192.png", 192, 0.28), ("icon-512.png", 512, 0.28), ("icon-maskable-512.png", 512, 0.22), ("apple-touch-icon.png", 180, 0.28)]:
    icon(size, mark).save(os.path.join(OUT, name), optimize=True)
    print("写好", name)
