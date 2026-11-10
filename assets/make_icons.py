"""把生成的两张原始图整理成 Windows 图标资源（透明底、统一尺寸、多尺寸 ICO）。"""
from PIL import Image, ImageFilter
import os

BASE = os.path.dirname(os.path.abspath(__file__))


def load_alpha(path):
    img = Image.open(path).convert("RGBA")
    bbox = img.getbbox()
    img = img.crop(bbox)
    side = max(img.size)
    canvas = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    canvas.paste(img, ((side - img.width) // 2, (side - img.height) // 2), img)
    return canvas


def load_from_black(path):
    img = Image.open(path).convert("RGB")
    gray = img.convert("L")
    mask = gray.point(lambda v: 255 if v > 45 else 0)
    mask = mask.filter(ImageFilter.GaussianBlur(1))
    rgba = img.convert("RGBA")
    rgba.putalpha(mask)
    bbox = mask.getbbox()
    rgba = rgba.crop(bbox)
    side = max(rgba.size)
    canvas = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    canvas.paste(rgba, ((side - rgba.width) // 2, (side - rgba.height) // 2))
    return canvas


on = load_alpha(os.path.join(BASE, "shield_on_raw.png")).resize((1024, 1024), Image.LANCZOS)
off = load_from_black(os.path.join(BASE, "shield_off_raw.png")).resize((1024, 1024), Image.LANCZOS)

on.save(os.path.join(BASE, "shield_on.png"))
off.save(os.path.join(BASE, "shield_off.png"))

sizes = [(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)]
for name, im in (("shield_on", on), ("shield_off", off), ("app", on)):
    im.save(os.path.join(BASE, name + ".ico"), format="ICO", sizes=sizes)

on.resize((256, 256), Image.LANCZOS).save(os.path.join(BASE, "logo.png"))
print("icons ready", [f for f in os.listdir(BASE) if f.endswith((".png", ".ico"))])
