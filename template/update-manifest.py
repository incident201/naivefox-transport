#!/usr/bin/env python3
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile

FILES = (
    "index.html",
    "assets/site.css",
    "assets/app.js",
    "assets/image-1.svg",
    "assets/image-2.svg",
    "assets/image-3.svg",
    "assets/image-4.svg",
    "runtime/read-cell.js",
    "runtime/lifecycle.js",
)


def main():
    if len(sys.argv) > 2:
        raise SystemExit("usage: update-manifest.py [APPLICATION_ROOT]")
    root = Path(sys.argv[1] if len(sys.argv) == 2 else Path(__file__).parent).resolve()
    entries = {}
    for relative in FILES:
        path = root / relative
        if path.is_symlink() or not path.is_file():
            raise SystemExit(f"{relative}: expected a regular non-symlink file")
        body = path.read_bytes()
        if not body:
            raise SystemExit(f"{relative}: file is empty")
        entries[relative] = {
            "size": len(body),
            "sha256": hashlib.sha256(body).hexdigest(),
        }
    manifest = {
        "format": "naivefox-application-v1",
        "wire_graph": "navigation-assets-api-v1",
        "files": entries,
    }
    body = (json.dumps(manifest, indent=2, sort_keys=True) + "\n").encode()
    descriptor, temporary_name = tempfile.mkstemp(
        dir=root, prefix=".application.", suffix=".tmp"
    )
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "wb") as output:
            os.fchmod(output.fileno(), 0o640)
            output.write(body)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, root / "application.json")
        directory = os.open(root, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        temporary.unlink(missing_ok=True)
    print(f"wrote {root / 'application.json'}")


if __name__ == "__main__":
    main()
