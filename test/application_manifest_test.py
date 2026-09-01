import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


REPOSITORY = Path(__file__).resolve().parent.parent
TEMPLATE = REPOSITORY / "template"
REQUIRED = {
    "index.html",
    "assets/site.css",
    "assets/app.js",
    "assets/image-1.svg",
    "assets/image-2.svg",
    "assets/image-3.svg",
    "assets/image-4.svg",
    "runtime/read-cell.js",
    "runtime/lifecycle.js",
}


class ApplicationManifestGeneratorTests(unittest.TestCase):
    def run_generator(self, root):
        subprocess.run(
            [sys.executable, str(root / "update-manifest.py"), str(root)],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )

    def assert_manifest_matches(self, root):
        manifest = json.loads((root / "application.json").read_text())
        self.assertEqual(manifest["format"], "naivefox-application-v1")
        self.assertEqual(manifest["wire_graph"], "navigation-assets-api-v1")
        self.assertEqual(set(manifest["files"]), REQUIRED)
        for relative in REQUIRED:
            body = (root / relative).read_bytes()
            self.assertEqual(manifest["files"][relative], {
                "size": len(body),
                "sha256": hashlib.sha256(body).hexdigest(),
            })

    def test_atomic_idempotent_generation_ignores_predictable_symlink_trap(self):
        with tempfile.TemporaryDirectory() as temporary:
            temporary = Path(temporary)
            root = temporary / "application"
            shutil.copytree(TEMPLATE, root)
            trap = temporary / "privileged-target"
            trap.write_bytes(b"must remain unchanged")
            (root / ".application.json.tmp").symlink_to(trap)
            with (root / "assets/site.css").open("ab") as css:
                css.write(b"\n/* customized */\n")

            self.run_generator(root)
            self.assertEqual(trap.read_bytes(), b"must remain unchanged")
            self.assertTrue((root / ".application.json.tmp").is_symlink())
            self.assert_manifest_matches(root)
            first = (root / "application.json").read_bytes()
            self.run_generator(root)
            self.assertEqual((root / "application.json").read_bytes(), first)
            self.assertEqual((root / "application.json").stat().st_mode & 0o777, 0o640)
            leftovers = [
                path.name for path in root.glob(".application.*.tmp")
                if path.name != ".application.json.tmp"
            ]
            self.assertEqual(leftovers, [])

    def test_required_source_symlink_is_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            temporary = Path(temporary)
            root = temporary / "application"
            shutil.copytree(TEMPLATE, root)
            target = temporary / "outside.css"
            target.write_text("outside")
            (root / "assets/site.css").unlink()
            (root / "assets/site.css").symlink_to(target)
            result = subprocess.run(
                [sys.executable, str(root / "update-manifest.py"), str(root)],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("regular non-symlink", result.stderr)


if __name__ == "__main__":
    unittest.main()
