#!/usr/bin/env python3
"""Compare two macOS payload directories built from the same source.

Every file must match byte for byte, except that Mach-O executables and
libraries are compared without their code signatures. A signature differs
between an independent rebuild and a signed release even when the code is
identical, so each Mach-O file is compared in a canonical form: its signature
removed with codesign, and the one field codesign leaves behind (the virtual
size of the __LINKEDIT segment) reset to the page-rounded file size. All
code, data and every other load command still have to match exactly.

Prints a JSON report and exits 1 on any difference. Changes nothing.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import stat
import struct
import subprocess
import sys
import tempfile

MH_MAGIC_64 = 0xFEEDFACF
LC_SEGMENT_64 = 0x19
LC_CODE_SIGNATURE = 0x1D
PAGE = 0x4000  # arm64 macOS page size
ENV = {"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "LANG": "C", "LC_ALL": "C"}


def is_macho(path):
    with path.open("rb") as f:
        head = f.read(4)
    return len(head) == 4 and struct.unpack("<I", head)[0] == MH_MAGIC_64


def load_commands(data):
    magic, _, _, _, ncmds, sizeofcmds, _, _ = struct.unpack_from("<IiiIIIII", data, 0)
    if magic != MH_MAGIC_64:
        raise ValueError("not a 64-bit Mach-O file")
    offset, end = 32, 32 + sizeofcmds
    if end > len(data):
        raise ValueError("load commands exceed the file")
    for _ in range(ncmds):
        cmd, size = struct.unpack_from("<II", data, offset)
        if size < 8 or offset + size > end:
            raise ValueError("malformed load command")
        yield offset, cmd, size
        offset += size


def has_signature(data):
    return any(cmd == LC_CODE_SIGNATURE for _, cmd, _ in load_commands(data))


def normalize_linkedit(data):
    """Reset __LINKEDIT's vmsize to the page-rounded filesize, in place."""
    found = False
    for offset, cmd, _ in load_commands(data):
        if cmd != LC_SEGMENT_64:
            continue
        name = bytes(data[offset + 8:offset + 24]).rstrip(b"\0")
        if name != b"__LINKEDIT":
            continue
        # segment_command_64: cmd, cmdsize, segname[16], vmaddr, vmsize, fileoff, filesize, ...
        filesize = struct.unpack_from("<Q", data, offset + 48)[0]
        struct.pack_into("<Q", data, offset + 32, (filesize + PAGE - 1) // PAGE * PAGE)
        found = True
    if not found:
        raise ValueError("Mach-O file has no __LINKEDIT segment")


def canonical_macho(path, scratch):
    """Return the SHA-256 of a Mach-O file without its signature."""
    copy = scratch / "canonical"
    shutil.copyfile(path, copy)
    if has_signature(copy.read_bytes()):
        subprocess.run(["/usr/bin/codesign", "--remove-signature", copy], check=True, env=ENV, capture_output=True, timeout=120)
    data = bytearray(copy.read_bytes())
    copy.unlink()
    if has_signature(data):
        raise ValueError("signature could not be removed")
    normalize_linkedit(data)
    return hashlib.sha256(data).hexdigest()


def inventory(root):
    """Map each relative path to its kind and permission bits. Links are refused."""
    entries = {}
    for path in sorted(root.rglob("*")):
        info = path.lstat()
        rel = path.relative_to(root).as_posix()
        if stat.S_ISLNK(info.st_mode):
            raise ValueError(f"symbolic link in payload: {rel}")
        if stat.S_ISDIR(info.st_mode):
            entries[rel] = ("directory", stat.S_IMODE(info.st_mode))
        elif stat.S_ISREG(info.st_mode):
            entries[rel] = ("file", stat.S_IMODE(info.st_mode))
        else:
            raise ValueError(f"unsupported file type in payload: {rel}")
    return entries


def file_identity(path, scratch):
    if is_macho(path):
        return "mach-o", canonical_macho(path, scratch)
    with path.open("rb") as f:
        return "bytes", hashlib.file_digest(f, "sha256").hexdigest()


def compare(left, right):
    a, b = inventory(left), inventory(right)
    report = {"only_left": sorted(a.keys() - b.keys()), "only_right": sorted(b.keys() - a.keys()), "mode": [], "content": [], "compared": 0, "mach_o": 0}
    with tempfile.TemporaryDirectory(prefix="vult-compare-") as temporary:
        scratch = Path(temporary)
        for rel in sorted(a.keys() & b.keys()):
            if a[rel] != b[rel]:
                report["mode"].append(rel)
                continue
            if a[rel][0] != "file":
                continue
            kind_a, sum_a = file_identity(left / rel, scratch)
            kind_b, sum_b = file_identity(right / rel, scratch)
            report["compared"] += 1
            report["mach_o"] += kind_a == "mach-o"
            if (kind_a, sum_a) != (kind_b, sum_b):
                report["content"].append(rel)
    report["identical"] = not (report["only_left"] or report["only_right"] or report["mode"] or report["content"])
    return report


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("left", type=Path)
    p.add_argument("right", type=Path)
    args = p.parse_args()
    if sys.platform != "darwin":
        p.error("compare on macOS; signatures are removed with codesign")
    for root in (args.left, args.right):
        if not root.is_dir() or root.is_symlink():
            p.error(f"not a payload directory: {root}")
    report = compare(args.left.resolve(), args.right.resolve())
    json.dump(report, sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")
    return 0 if report["identical"] else 1


if __name__ == "__main__":
    sys.exit(main())
