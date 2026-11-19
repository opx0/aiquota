#!/usr/bin/env python3
"""Build AIQuota and install its addon into an existing Caelestia checkout."""

import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parent
HOST_FILE = Path("shell/modules/bar/popouts/Content.qml")


def find_checkout(explicit=None):
    if explicit is None and os.environ.get("CAELESTIA_CHECKOUT"):
        explicit = Path(os.environ["CAELESTIA_CHECKOUT"])
    if explicit is not None:
        checkout = explicit.expanduser().resolve()
        if not (checkout / HOST_FILE).is_file():
            raise ValueError(f"not a Caelestia source checkout: {checkout}")
        return checkout

    home = Path.home()
    candidates = [
        Path.cwd(),
        home / "code/caelestia",
        home / "src/caelestia",
        home / "caelestia",
    ]
    matches = []
    for candidate in candidates:
        if not candidate:
            continue
        path = Path(candidate).expanduser().resolve()
        if (path / HOST_FILE).is_file() and path not in matches:
            matches.append(path)
    if len(matches) != 1:
        detail = "none found" if not matches else ", ".join(map(str, matches))
        raise ValueError(f"Caelestia checkout is ambiguous or missing ({detail}); pass its path")
    return matches[0]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("checkout", nargs="?", type=Path,
                        help="Caelestia source checkout; auto detected when omitted")
    parser.add_argument("--no-deploy", action="store_true",
                        help="install into the checkout without running its apply command")
    args = parser.parse_args()
    try:
        checkout = find_checkout(args.checkout)
    except ValueError as error:
        parser.error(str(error))
    if not args.no_deploy and not os.access(checkout / "apply", os.X_OK):
        parser.error(f"{checkout} has no apply command; use --no-deploy and deploy manually")
    if shutil.which("go") is None or shutil.which("git") is None:
        parser.error("Go and Git are required to build and install AIQuota")

    binary_dir = Path.home() / ".local/bin"
    binary_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="aiquota-build-") as temporary:
        built = Path(temporary) / "aiquota"
        subprocess.run(["go", "build", "-trimpath", "-o", str(built), "."],
                       cwd=ROOT, check=True)
        subprocess.run([sys.executable, str(ROOT / "quickshell/caelestia/install.py"),
                        str(checkout)], check=True)
        destination = binary_dir / "aiquota"
        staged = binary_dir / f".aiquota-{os.getpid()}"
        try:
            shutil.copy2(built, staged)
            os.replace(staged, destination)
        finally:
            staged.unlink(missing_ok=True)

    if not args.no_deploy:
        subprocess.run(["./apply"], cwd=checkout, check=True)
    print(f"AIQuota addon installed for {checkout}")
    print(f"Binary: {destination}")
    if args.no_deploy:
        print(f"Deploy when ready: cd {checkout} && ./apply")


if __name__ == "__main__":
    main()
