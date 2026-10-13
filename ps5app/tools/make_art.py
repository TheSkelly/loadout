"""Draws the PS5 home screen art for Loadout: sce_sys/icon0.png (512x512) and
sce_sys/pic0.png (1920x1080). The mark is the same manila tag as the PC app's
icon. Run with Pillow: python3 tools/make_art.py (from the ps5app folder)."""
from pathlib import Path
from PIL import Image, ImageDraw, ImageFilter

INK, INK2, MANILA, TEXT = (14, 20, 32), (19, 27, 41), (233, 196, 106), (231, 236, 245)
OUT = Path(__file__).resolve().parent.parent / "sce_sys"


def tag(draw, x, y, s, fill, hole):
    # the logo's tag: a rounded rectangle with a pointed right end and a hole
    w, h = 22 * s, 18 * s
    r = 2.5 * s
    point = 7 * s
    body = [(x + r, y), (x + w - point, y), (x + w, y + h / 2), (x + w - point, y + h), (x + r, y + h)]
    draw.polygon(body, fill=fill)
    draw.rounded_rectangle([x, y, x + 3 * r, y + h], radius=r, fill=fill)
    cx, cy, cr = x + 5.5 * s, y + h / 2, 2 * s
    draw.ellipse([cx - cr, cy - cr, cx + cr, cy + cr], fill=hole)


def gradient(w, h):
    img = Image.new("RGB", (w, h), INK)
    px = img.load()
    for yy in range(h):
        for xx in range(0, w):
            f = (xx / w + yy / h) / 2
            px[xx, yy] = tuple(int(INK[i] + (INK2[i] - INK[i]) * f + (MANILA[i] - INK[i]) * 0.05 * (1 - f)) for i in range(3))
    return img


def icon():
    s = 4
    img = gradient(512 * s // 4, 512 * s // 4).resize((512, 512))
    big = Image.new("RGBA", (512 * s, 512 * s), (0, 0, 0, 0))
    d = ImageDraw.Draw(big)
    tag(d, 96 * s, 152 * s, 14.5 * s, MANILA + (255,), INK + (255,))
    big = big.resize((512, 512), Image.LANCZOS)
    glow = big.filter(ImageFilter.GaussianBlur(28))
    img = img.convert("RGBA")
    img.alpha_composite(Image.eval(glow, lambda v: v // 3))
    img.alpha_composite(big)
    img.convert("RGB").save(OUT / "icon0.png")


def background():
    img = gradient(480, 270).resize((1920, 1080)).convert("RGBA")
    s = 3
    layer = Image.new("RGBA", (1920 * s // 2, 1080 * s // 2), (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    tag(d, 1180 * s // 2, 380 * s // 2, 24 * s // 2, MANILA + (40,), (0, 0, 0, 0))
    layer = layer.resize((1920, 1080), Image.LANCZOS)
    img.alpha_composite(layer)
    img.convert("RGB").save(OUT / "pic0.png")


if __name__ == "__main__":
    OUT.mkdir(exist_ok=True)
    icon()
    background()
    print("wrote", OUT / "icon0.png", OUT / "pic0.png")
