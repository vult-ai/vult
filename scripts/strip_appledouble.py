"""Remove AppleDouble entries (._name) from a flat macOS component package.

pkgbuild records every extended attribute it can't drop, such as
com.apple.provenance, as a ._name member of the payload. Installer turns those
back into attributes on the installed files; they carry nothing Vult needs.
This rewrites the package without them, keeping the payload format pkgbuild
writes: an odc cpio archive, in a pbzx stream of xz chunks (or gzip, for
--compression legacy).
"""
import gzip
import lzma
import re
import struct
import subprocess
import tempfile
from pathlib import Path

APPLEDOUBLE = re.compile(r"(^|/)\._[^/]*$")
CHUNK = 16 << 20
TRAILER = "TRAILER!!!"


def pbzx_decode(data):
    if data[:4] != b"pbzx":
        raise ValueError("payload is not a pbzx stream")
    out, i = bytearray(), 12
    while i < len(data):
        _, size = struct.unpack(">QQ", data[i:i + 16])
        i += 16
        chunk = data[i:i + size]
        i += size
        out += lzma.decompress(chunk) if chunk[:6] == b"\xfd7zXZ\x00" else chunk
    return bytes(out)


def pbzx_encode(data):
    out = bytearray(b"pbzx" + struct.pack(">Q", CHUNK))
    for start in range(0, len(data), CHUNK):
        part = data[start:start + CHUNK]
        packed = lzma.compress(part, format=lzma.FORMAT_XZ, check=lzma.CHECK_NONE, preset=6)
        # Each chunk: its length before and after compression, then the xz stream.
        out += struct.pack(">QQ", len(part), len(packed)) + packed
    return bytes(out)


def cpio_entries(data):
    """Yield (name, header, file data) for each odc cpio member."""
    i = 0
    while i < len(data):
        header = data[i:i + 76]
        if header[:6] != b"070707":
            raise ValueError("payload is not an odc cpio archive")
        namesize, filesize = int(header[59:65], 8), int(header[65:76], 8)
        name = data[i + 76:i + 76 + namesize - 1].decode()
        body = data[i + 76 + namesize:i + 76 + namesize + filesize]
        yield name, data[i:i + 76 + namesize], body
        i += 76 + namesize + filesize
        if name == TRAILER:
            return
    raise ValueError("cpio archive has no trailer")


def strip_cpio(data):
    kept, removed, out = 0, 0, bytearray()
    for name, head, body in cpio_entries(data):
        if name != TRAILER and APPLEDOUBLE.search(name):
            removed += 1
            continue
        kept += name != TRAILER
        out += head + body
    return bytes(out), kept, removed


def strip_package(package, run):
    """Rewrite package in place without AppleDouble members; returns how many were removed."""
    with tempfile.TemporaryDirectory(prefix="vult-strip-") as temporary:
        work = Path(temporary) / "expanded"
        run(["/usr/sbin/pkgutil", "--expand", package, work])
        raw = (work / "Payload").read_bytes()
        # pkgbuild writes pbzx for --compression latest and gzip otherwise.
        legacy = raw[:2] == b"\x1f\x8b"
        payload, kept, removed = strip_cpio(gzip.decompress(raw) if legacy else pbzx_decode(raw))
        (work / "Payload").write_bytes(gzip.compress(payload, mtime=0) if legacy else pbzx_encode(payload))
        listing = subprocess.check_output(["/usr/bin/lsbom", str(work / "Bom")], timeout=60).decode()
        lines = [line for line in listing.splitlines() if not APPLEDOUBLE.search(line.split("\t", 1)[0])]
        (Path(temporary) / "bom.txt").write_text("\n".join(lines) + "\n")
        (work / "Bom").unlink()
        run(["/usr/bin/mkbom", "-i", Path(temporary) / "bom.txt", work / "Bom"])
        info = work / "PackageInfo"
        info.write_text(re.sub(r'numberOfFiles="\d+"', f'numberOfFiles="{kept}"', info.read_text(), count=1))
        rebuilt = Path(temporary) / "rebuilt.pkg"
        run(["/usr/sbin/pkgutil", "--flatten", work, rebuilt])
        Path(package).write_bytes(rebuilt.read_bytes())
    return removed
