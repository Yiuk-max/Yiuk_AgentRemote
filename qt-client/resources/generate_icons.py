"""Regenerate brand assets: python qt-client/resources/generate_icons.py

Requires Pillow (generation only, not a client/build dependency).
icon.jpg is the original artwork; the tray mark is intentionally hand-simplified.
"""
from pathlib import Path
import io
import struct
from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parent
SIZES = (16, 20, 24, 32, 40, 48, 64, 128, 256)
# Filled shapes, not hairline strokes. Coordinates use a 32px grid; eyes and
# bow land on whole pixels at 16px. A light outer rim separates the dark hair
# from dark taskbars, while the dark silhouette remains visible on light ones.
# Both SVG and ICO use the same ordered layers; no SVG plugin is needed at runtime.
LAYERS = (
    ("#f5f7ff", "M 1 29 L 2 14 C 2 6 7 1 16 1 C 21 1 24 3 26 5 L 31 3 L 31 15 L 29 16 L 31 29 C 22 32 9 32 1 29 Z"),
    ("#202856", "M 3 28 L 4 14 C 4 7 8 3 16 3 C 24 3 28 8 28 15 L 29 28 C 22 30 10 30 3 28 Z"),
    ("#5366c9", "M 5 27 L 6 14 C 6 8 10 5 16 5 C 22 5 26 9 26 15 L 27 27 Z"),
    ("#fff0df", "M 6 15 C 8 12 24 12 26 15 L 26 23 C 26 27 22 28 16 28 C 10 28 6 27 6 23 Z"),
    ("#202856", "M 5 18 L 5 13 C 6 6 11 4 16 4 C 22 4 27 9 27 17 L 27 20 C 23 19 21 15 20 12 C 21 17 21 19 20 21 C 15 19 13 15 13 11 C 12 15 9 18 5 20 Z"),
    ("#6378df", "M 7 15 C 8 9 12 6 16 6 C 21 6 24 10 25 15 C 22 12 21 10 18 8 C 18 13 20 16 19 18 C 15 15 15 11 15 8 C 12 11 11 14 7 17 Z"),
    ("#202856", "M 6 20 L 12 20 L 12 23 L 6 23 Z M 20 20 L 26 20 L 26 23 L 20 23 Z"),
    ("#202856", "M 21 9 L 21 4 L 26 6 L 30 4 L 30 14 L 26 12 L 21 14 Z"),
    ("#75d9ff", "M 23 7 L 26 8 L 28 7 L 28 11 L 26 10 L 23 11 Z"),
    ("#ffffff", "M 25 8 L 27 8 L 27 10 L 25 10 Z"),
)


def raster_mark(size):
    scale = size * 4 / 32
    image = Image.new("RGBA", (size * 4, size * 4))
    draw = ImageDraw.Draw(image)
    for fill, path in LAYERS:
        tokens = iter(path.split())
        points = []
        start = None

        def flush():
            if len(points) < 2:
                return
            xy = [(x * scale, y * scale) for x, y in points]
            draw.polygon(xy, fill=fill)

        def point():
            return float(next(tokens)), float(next(tokens))

        for command in tokens:
            if command == "M":
                flush()
                points = [point()]
                start = points[0]
            elif command == "L":
                points.append(point())
            elif command == "C":
                p0 = points[-1]
                p1, p2, p3 = point(), point(), point()
                for i in range(1, 49):
                    t = i / 48
                    u = 1 - t
                    points.append(tuple(u**3 * p0[d] + 3*u*u*t * p1[d]
                                        + 3*u*t*t * p2[d] + t**3 * p3[d]
                                        for d in (0, 1)))
            elif command == "Z":
                points.append(start)
            else:
                raise ValueError(command)
        flush()
    # BOX gives clean edge coverage without the faint ringing around Lanczos
    # outlines. Never upscale/downscale a shared tray bitmap across ICO sizes.
    return image.resize((size, size), Image.Resampling.BOX)


def write_ico(path, frames):
    # Explicit PNG-backed frames: retain individually rendered small tray sizes.
    blobs = []
    for frame in frames:
        buffer = io.BytesIO()
        frame.save(buffer, format="PNG")
        blobs.append(buffer.getvalue())
    offset = 6 + 16 * len(frames)
    directory = bytearray(struct.pack("<HHH", 0, 1, len(frames)))
    for frame, blob in zip(frames, blobs):
        size = frame.width
        directory.extend(struct.pack("<BBBBHHII", size % 256, size % 256,
                                     0, 0, 1, 32, len(blob), offset))
        offset += len(blob)
    path.write_bytes(directory + b"".join(blobs))


def main():
    with Image.open(ROOT / "icon.jpg") as source:
        source = source.convert("RGBA")
        # Keep the whole original image; only soften its square corners.
        edge = min(source.size)
        left, top = (source.width - edge) // 2, (source.height - edge) // 2
        artwork = source.crop((left, top, left + edge, top + edge)).resize(
            (1024, 1024), Image.Resampling.LANCZOS)
    mask = Image.new("L", artwork.size)
    ImageDraw.Draw(mask).rounded_rectangle((0, 0, 1023, 1023), radius=180, fill=255)
    artwork.putalpha(mask)
    canvas = Image.new("RGBA", (1088, 1088))
    canvas.alpha_composite(artwork, (32, 32))
    write_ico(ROOT / "app.ico", [canvas.resize((s, s), Image.Resampling.LANCZOS)
                                 for s in SIZES])
    write_ico(ROOT / "tray.ico", [raster_mark(s) for s in SIZES])
    paths = "\n".join(f'  <path fill="{fill}" d="{p}"/>' for fill, p in LAYERS)
    (ROOT / "tray.svg").write_text(
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">\n'
        f'{paths}\n</svg>\n', encoding="utf-8")
    print("Generated app.ico, tray.ico, tray.svg")


if __name__ == "__main__":
    main()
