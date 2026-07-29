from __future__ import annotations

import argparse
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont


def crop_scaled(source: Path, destination: Path, *, kind: str) -> None:
    with Image.open(source) as image:
        image = image.convert("RGBA")
        width, height = image.size
        if kind == "disc":
            size = min(round(height * 0.415), width, height)
            left = round(width * 0.014)
            top = round(height * 0.165)
            output_size = 128
        else:
            size = min(round(height * 0.274), width, height)
            left = round(width * 0.121)
            top = round(height * 0.158)
            output_size = 96
        left = max(0, min(left, width - size))
        top = max(0, min(top, height - size))
        cropped = image.crop((left, top, left + size, top + size))
        cropped.thumbnail((output_size, output_size), Image.Resampling.LANCZOS)
        destination.parent.mkdir(parents=True, exist_ok=True)
        cropped.save(destination, format="PNG", optimize=True)


def build_contact_sheet(files: list[Path], destination: Path, *, cell: int, columns: int) -> None:
    label_height = 20
    rows = max(1, (len(files) + columns - 1) // columns)
    canvas = Image.new("RGB", (columns * cell, rows * (cell + label_height)), "#171a22")
    draw = ImageDraw.Draw(canvas)
    font = ImageFont.load_default()
    for index, path in enumerate(files):
        x = (index % columns) * cell
        y = (index // columns) * (cell + label_height)
        with Image.open(path) as image:
            image = image.convert("RGBA")
            image.thumbnail((cell, cell), Image.Resampling.LANCZOS)
            canvas.paste(image, (x + (cell - image.width) // 2, y), image)
        draw.text((x + 3, y + cell + 2), path.stem, fill="#eef2ff", font=font)
    destination.parent.mkdir(parents=True, exist_ok=True)
    canvas.save(destination, format="PNG", optimize=True)


def main() -> None:
    parser = argparse.ArgumentParser(description="Crop downloaded ZZZ atlas sheets into small offline UI assets.")
    parser.add_argument("--staging", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--contact-output", type=Path, required=True)
    args = parser.parse_args()

    for source in sorted((args.staging / "drive-discs").glob("drive-disc-*.png")):
        crop_scaled(source, args.output / "drive-discs" / source.name, kind="disc")
    for source in sorted((args.staging / "agents").glob("agent-*.png")):
        crop_scaled(source, args.output / "agents" / source.name, kind="agent")

    build_contact_sheet(
        sorted((args.output / "drive-discs").glob("drive-disc-*.png")),
        args.contact_output / "drive-discs-contact-sheet.png",
        cell=128,
        columns=8,
    )
    build_contact_sheet(
        sorted((args.output / "agents").glob("agent-*.png")),
        args.contact_output / "agents-contact-sheet.png",
        cell=96,
        columns=10,
    )


if __name__ == "__main__":
    main()
