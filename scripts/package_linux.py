#!/usr/bin/env python3
"""Build the Linux server and maintenance images reproducibly. Publishes nothing.

Each image is written as an OCI image layout directory. Every byte is fixed by
the source: the Go programs are built with the pinned toolchain and no build
paths; the single layer is an uncompressed tar with fixed times, owners and
permissions, sorted; the image config has no creation time; JSON is written
with sorted keys and no whitespace. Layers are left uncompressed because
compressed output can differ between zlib builds on different machines, which
would change the digest. The same source therefore gives the same image
digests on any machine, and an installation can run an image by digest.

    python3 scripts/package_linux.py --output DIR --web-dist web/dist --web-sha256 SUM

Load an image locally with: skopeo copy oci:DIR/vult-server docker-daemon:vult-server:local
"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

from package_macos import ENV, GO_VERSION, interface_files, materialize, selected_files, stage_interface

ARCH = "amd64"
USER = "65532:65532"
NOTICES = ("LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md")
IMAGES = {
    "vult-server": {"program": "vult-server", "tags": "webui"},
    "vult-maintenance": {"program": "vult-server-admin", "tags": ""},
}


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def sha256(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


def layer(files):
    """An uncompressed tar of {path: (bytes, mode)} with every attribute fixed."""
    out = io.BytesIO()
    dirs = set()
    for name in files:
        parts = name.split("/")[:-1]
        for i in range(1, len(parts) + 1):
            dirs.add("/".join(parts[:i]))
    entries = [(d, None, 0o755) for d in dirs] + [(n, data, mode) for n, (data, mode) in files.items()]
    with tarfile.open(fileobj=out, mode="w", format=tarfile.PAX_FORMAT) as tar:
        for name, data, mode in sorted(entries, key=lambda e: e[0]):
            info = tarfile.TarInfo(name + ("/" if data is None else ""))
            info.mtime, info.uid, info.gid, info.uname, info.gname, info.mode = 0, 0, 0, "", "", mode
            if data is None:
                info.type = tarfile.DIRTYPE
                tar.addfile(info)
            else:
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
    return out.getvalue()


def write_blob(layout, data):
    digest = sha256(data)
    path = layout / "blobs" / "sha256" / digest.split(":", 1)[1]
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data)
    return {"digest": digest, "size": len(data)}


def image(layout, program_name, program, notices):
    """Write one OCI image layout and return its manifest digest."""
    layout.mkdir(parents=True, exist_ok=False)
    files = {program_name: (program, 0o555)}
    for name, data in notices.items():
        files[f"usr/share/licenses/vult/{name}"] = (data, 0o444)
    tar = layer(files)
    layer_desc = dict(write_blob(layout, tar), mediaType="application/vnd.oci.image.layer.v1.tar")
    config = {
        "architecture": ARCH,
        "os": "linux",
        "config": {"User": USER, "Entrypoint": ["/" + program_name]},
        "rootfs": {"type": "layers", "diff_ids": [layer_desc["digest"]]},
    }
    config_desc = dict(write_blob(layout, canonical(config)), mediaType="application/vnd.oci.image.config.v1+json")
    manifest = {"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": config_desc, "layers": [layer_desc]}
    manifest_desc = dict(write_blob(layout, canonical(manifest)), mediaType="application/vnd.oci.image.manifest.v1+json")
    (layout / "index.json").write_bytes(canonical({"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": [manifest_desc]}))
    (layout / "oci-layout").write_bytes(canonical({"imageLayoutVersion": "1.0.0"}))
    return manifest_desc["digest"]


def build_program(go, source, name, tags, output, env):
    args = [go, "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", str(output)]
    if tags:
        args.insert(2, f"-tags={tags}")
    subprocess.run(args + [f"./cmd/{name}"], cwd=source, env=env, check=True, timeout=900)
    return output.read_bytes()


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--output", type=Path, required=True, help="new directory for the image layouts")
    p.add_argument("--web-dist", type=Path, required=True, help="built interface from the same source")
    p.add_argument("--web-sha256", required=True, help="expected interface file-manifest checksum")
    p.add_argument("--go", default="go", help="Go 1.27.3 executable")
    args = p.parse_args()
    if interface_files(args.web_dist)[1] != args.web_sha256:
        p.error("interface build checksum mismatch")
    go = shutil.which(args.go)
    if not go:
        p.error("Go 1.27.3 is required")
    env = dict(ENV, HOME=str(Path.home()), CGO_ENABLED="0", GOOS="linux", GOARCH=ARCH, GOTOOLCHAIN="local", GOFLAGS="-mod=readonly", GOPROXY="https://proxy.golang.org,direct", GOSUMDB="sum.golang.org")
    version = subprocess.check_output([go, "version"], env=env, timeout=10).decode().split()[2]
    if version != GO_VERSION:
        p.error("the exact pinned Go version is required")
    source = Path(__file__).resolve().parents[1]
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="vult-images-") as temporary:
        clean = Path(temporary) / "source"
        materialize(source, clean, selected_files(source))
        stage_interface(args.web_dist, clean / "web/dist", args.web_sha256)
        notices = {name: (clean / name).read_bytes() for name in NOTICES}
        digests = {}
        for image_name, spec in IMAGES.items():
            program = build_program(go, clean, spec["program"], spec["tags"], Path(temporary) / spec["program"], env)
            digests[image_name] = image(output / image_name, spec["program"], program, notices)
    (output / "images.json").write_text(json.dumps(digests, indent=2, sort_keys=True) + "\n")
    print(json.dumps(digests, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
