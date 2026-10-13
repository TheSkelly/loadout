"""Draws the PS5 home screen art for Loadout: sce_sys/icon0.png (512x512) and
sce_sys/pic0.png (1920x1080), and assets/logo.png for the app itself. Carolina blue on royal and midnight blue and
black: a glowing hexagon with a download arrow dropping into a tray.
Run with Pillow: python3 tools/make_art.py (from the ps5app folder).
The wordmark uses Segoe UI Black when it's there (Windows), else a fallback."""
import math
from pathlib import Path
from PIL import Image, ImageDraw, ImageFilter, ImageFont, ImageChops

BLACK = (4, 6, 14)
MIDNIGHT = (16, 22, 74)
ROYAL = (40, 84, 214)
CAROLINA = (75, 156, 211)
ICE = (196, 232, 255)
OUT = Path(__file__).resolve().parent.parent / "sce_sys"
FONTS = ["C:/Windows/Fonts/seguibl.ttf", "/mnt/c/Windows/Fonts/seguibl.ttf", "C:/Windows/Fonts/impact.ttf",
         "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf"]


def font(size):
    for f in FONTS:
        if Path(f).exists():
            return ImageFont.truetype(f, size)
    return ImageFont.load_default()


def mix(a, b, f):
    return tuple(int(a[i] + (b[i] - a[i]) * f) for i in range(3))


def backdrop(w, h, cx, cy, reach):
    """Black with a midnight-to-royal glow around (cx, cy), drawn small and scaled up."""
    sw, sh = max(w // 4, 1), max(h // 4, 1)
    img = Image.new("RGB", (sw, sh))
    px = img.load()
    for y in range(sh):
        for x in range(sw):
            d = math.hypot(x * 4 - cx, y * 4 - cy) / reach
            f = max(0.0, 1 - d)
            c = mix(BLACK, MIDNIGHT, min(1, f * 1.6))
            c = mix(c, ROYAL, max(0, f - 0.55) * 1.4)
            px[x, y] = c
    return img.resize((w, h), Image.BICUBIC).convert("RGBA")


def streaks(w, h, s, count, seed):
    """Diagonal light streaks, royal and Carolina, for speed."""
    layer = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    import random
    rnd = random.Random(seed)
    for _ in range(count):
        x = rnd.uniform(-0.3, 1.1) * w
        y = rnd.uniform(0, 1) * h
        length = rnd.uniform(0.15, 0.5) * w
        width = rnd.uniform(1, 4) * s
        color = rnd.choice([ROYAL, CAROLINA, CAROLINA])
        alpha = rnd.randint(40, 120)
        d.line([(x, y), (x + length, y - length * 0.45)], fill=color + (alpha,), width=int(width))
    return layer.filter(ImageFilter.GaussianBlur(1.2 * s))


def hexagon(cx, cy, r, rot=90):
    return [(cx + r * math.cos(math.radians(rot + 60 * i)), cy + r * math.sin(math.radians(rot + 60 * i))) for i in range(6)]


def emblem(size):
    """The mark on a transparent square of `size` px: hexagon ring, arrow, tray."""
    s = size / 512
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    cx, cy = size / 2, size / 2
    # outer ring: royal, then a Carolina inner ring for depth
    d.polygon(hexagon(cx, cy, 236 * s), fill=ROYAL + (255,))
    d.polygon(hexagon(cx, cy, 214 * s), fill=BLACK + (255,))
    d.polygon(hexagon(cx, cy, 200 * s), fill=CAROLINA + (255,))
    d.polygon(hexagon(cx, cy, 186 * s), fill=MIDNIGHT + (255,))
    # inside: a midnight-to-black fill
    inner = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    grad = Image.linear_gradient("L").resize((size, size))
    fill = Image.composite(Image.new("RGBA", (size, size), BLACK + (255,)), Image.new("RGBA", (size, size), MIDNIGHT + (255,)), grad)
    mask = Image.new("L", (size, size), 0)
    ImageDraw.Draw(mask).polygon(hexagon(cx, cy, 184 * s), fill=255)
    inner.paste(fill, (0, 0), mask)
    img.alpha_composite(inner)
    # the arrow: shaft and head, in Carolina with an ice highlight down its left
    shaft = [(cx - 34 * s, cy - 130 * s), (cx + 34 * s, cy - 130 * s), (cx + 34 * s, cy + 6 * s), (cx - 34 * s, cy + 6 * s)]
    head = [(cx - 98 * s, cy - 6 * s), (cx + 98 * s, cy - 6 * s), (cx, cy + 92 * s)]
    arrow = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    ad = ImageDraw.Draw(arrow)
    ad.polygon(shaft, fill=CAROLINA + (255,))
    ad.polygon(head, fill=CAROLINA + (255,))
    shine = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    ImageDraw.Draw(shine).polygon([(cx - 34 * s, cy - 130 * s), (cx - 6 * s, cy - 130 * s), (cx - 6 * s, cy + 60 * s), (cx - 98 * s, cy - 6 * s), (cx - 34 * s, cy - 6 * s)], fill=ICE + (150,))
    arrow.alpha_composite(Image.composite(shine, Image.new("RGBA", (size, size), (0, 0, 0, 0)), arrow.split()[3]))
    img.alpha_composite(arrow)
    # the tray it drops into
    d = ImageDraw.Draw(img)
    t = 20 * s
    d.line([(cx - 104 * s, cy + 62 * s), (cx - 104 * s, cy + 112 * s), (cx + 104 * s, cy + 112 * s), (cx + 104 * s, cy + 62 * s)],
           fill=ICE + (255,), width=int(t), joint="curve")
    return img


def glow(layer, radius, strength):
    g = layer.filter(ImageFilter.GaussianBlur(radius))
    a = g.split()[3].point(lambda v: min(255, int(v * strength)))
    g.putalpha(a)
    return g


def wordmark(text, size, width_limit):
    f = font(size)
    while f.getlength(text) > width_limit and size > 10:
        size -= 2
        f = font(size)
    return f


def icon():
    S = 2048
    img = backdrop(S, S, S / 2, S * 0.42, S * 0.75)
    img.alpha_composite(streaks(S, S, 4, 26, 7))
    mark = emblem(int(S * 0.70))
    layer = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    layer.alpha_composite(mark, (int(S * 0.15), int(S * 0.06)))
    img.alpha_composite(glow(layer, 60, 1.6))
    img.alpha_composite(layer)
    # wordmark across the bottom
    d = ImageDraw.Draw(img)
    f = wordmark("LOADOUT", 300, S * 0.86)
    w = f.getlength("LOADOUT")
    text = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    ImageDraw.Draw(text).text(((S - w) / 2, S * 0.755), "LOADOUT", font=f, fill=(255, 255, 255, 255))
    tint = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    grad = Image.linear_gradient("L").resize((S, S))
    tint = Image.composite(Image.new("RGBA", (S, S), CAROLINA + (255,)), Image.new("RGBA", (S, S), ICE + (255,)), grad)
    text = Image.composite(tint, Image.new("RGBA", (S, S), (0, 0, 0, 0)), text.split()[3])
    img.alpha_composite(glow(text, 30, 1.2))
    img.alpha_composite(text)
    # a thin Carolina frame so it holds its own next to other tiles
    d = ImageDraw.Draw(img)
    d.rectangle([0, 0, S - 1, S - 1], outline=ROYAL + (255,), width=16)
    img.convert("RGB").resize((512, 512), Image.LANCZOS).save(OUT / "icon0.png")


def background():
    W, H = 1920, 1080
    img = backdrop(W, H, W * 0.70, H * 0.48, W * 0.62)
    img.alpha_composite(streaks(W, H, 2, 60, 11))
    mark = emblem(820)
    layer = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    layer.alpha_composite(mark, (int(W * 0.70 - 410), int(H * 0.48 - 410)))
    img.alpha_composite(glow(layer, 40, 1.4))
    faded = layer.copy()
    faded.putalpha(layer.split()[3].point(lambda v: v * 3 // 4))
    img.alpha_composite(faded)
    # fade the left into black so the PS5's title text reads
    shade = Image.linear_gradient("L").rotate(90, expand=True).resize((W, H))
    shade = shade.point(lambda v: int(max(0, 255 - v * 1.6) * 0.85))
    img.alpha_composite(Image.composite(Image.new("RGBA", (W, H), BLACK + (255,)), Image.new("RGBA", (W, H), (0, 0, 0, 0)), shade))
    img.convert("RGB").save(OUT / "pic0.png")


def logo():
    """The emblem alone on transparent, for the app's own screens (assets/logo.png)."""
    S = 768
    layer = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    layer.alpha_composite(emblem(int(S * 0.84)), (int(S * 0.08), int(S * 0.08)))
    img = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    img.alpha_composite(glow(layer, 18, 1.3))
    img.alpha_composite(layer)
    (OUT.parent / "assets").mkdir(exist_ok=True)
    img.resize((192, 192), Image.LANCZOS).save(OUT.parent / "assets" / "logo.png")


def pc_icon():
    """The same emblem as Loadout's Windows icon (app.ico in the project root), drawn
    for each size Windows asks for, so the small ones stay crisp."""
    frames = []
    for size in (256, 128, 64, 48, 40, 32, 24, 20, 16):
        S = size * 4
        layer = Image.new("RGBA", (S, S), (0, 0, 0, 0))
        layer.alpha_composite(emblem(int(S * 0.96)), (int(S * 0.02), int(S * 0.02)))
        img = Image.new("RGBA", (S, S), (0, 0, 0, 0))
        if size >= 48:
            img.alpha_composite(glow(layer, S // 40, 1.0))
        img.alpha_composite(layer)
        frames.append(img.resize((size, size), Image.LANCZOS))
    ico = OUT.parent.parent / "app.ico"
    frames[0].save(ico, format="ICO", sizes=[f.size for f in frames], append_images=frames[1:])
    return ico


if __name__ == "__main__":
    OUT.mkdir(exist_ok=True)
    icon()
    background()
    logo()
    print("wrote", pc_icon())
    print("wrote", OUT / "icon0.png", OUT / "pic0.png", OUT.parent / "assets" / "logo.png")
