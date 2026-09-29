"""Write the pdf-text fixtures reproducibly: python3 scripts/make_fixtures.py

The PDFs are assembled object by object with the standard Helvetica font, so
no PDF library is needed and the bytes are identical on every run. Tests check
that the committed files match this script.
"""
from __future__ import annotations

import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

# One list of lines per page; an empty list is a page without text, like a scan.
SAMPLE_PAGES: list[list[str]] = [
    ["Field notes from a small observatory",
     "The first page describes the site, the dome and its two telescopes."],
    ["Night sky log",
     "Observers recorded a heliotrope aurora above the northern ridge.",
     "The sky cleared shortly after midnight."],
    [],
]


def _escape(text: str) -> str:
    return text.replace("\\", "\\\\").replace("(", "\\(").replace(")", "\\)")


def build_pdf(pages: list[list[str]]) -> bytes:
    """A minimal, valid PDF 1.4 document with one text line per entry."""
    count = len(pages)
    first_page = 4  # 1 catalog, 2 page tree, 3 font
    objects: list[bytes] = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [" + b" ".join(f"{first_page + 2 * i} 0 R".encode() for i in range(count)) + f"] /Count {count} >>".encode(),
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
    ]
    for i, lines in enumerate(pages):
        stream = b""
        if lines:
            ops = ["BT", "/F1 14 Tf", "72 720 Td", "18 TL"]
            ops += [f"({_escape(line)}) Tj T*" for line in lines]
            ops.append("ET")
            stream = "\n".join(ops).encode("cp1252")
        content = first_page + 2 * i + 1
        objects.append(f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents {content} 0 R >>".encode())
        objects.append(f"<< /Length {len(stream)} >>\nstream\n".encode() + stream + b"\nendstream")
    out = bytearray(b"%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
    offsets = []
    for number, body in enumerate(objects, start=1):
        offsets.append(len(out))
        out += f"{number} 0 obj\n".encode() + body + b"\nendobj\n"
    xref = len(out)
    out += f"xref\n0 {len(objects) + 1}\n0000000000 65535 f \n".encode()
    out += b"".join(f"{offset:010d} 00000 n \n".encode() for offset in offsets)
    out += f"trailer\n<< /Size {len(objects) + 1} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode()
    return bytes(out)


FILES = {"fixtures/sample.pdf": lambda: build_pdf(SAMPLE_PAGES)}


def main() -> int:
    for relative, build in FILES.items():
        path = ROOT / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(build())
        print("wrote", path.relative_to(ROOT))
    return 0


if __name__ == "__main__":
    sys.exit(main())
