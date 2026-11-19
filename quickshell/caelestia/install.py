#!/usr/bin/env python3
"""Apply the integration to a Caelestia source checkout, not a deployment."""

import argparse
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


HERE = Path(__file__).resolve().parent
LEGACY_POPOUT = '''        Popout {
            name: "aiquota"
            sourceComponent: AIQuota {}
        }

'''


def fingerprint(path):
    try:
        stat = path.lstat()
    except FileNotFoundError:
        return None
    if path.is_symlink() or not path.is_file():
        raise OSError(f"invalid destination file: {path}")
    return (stat.st_dev, stat.st_ino, stat.st_mode, stat.st_size,
            stat.st_mtime_ns, hashlib.sha256(path.read_bytes()).digest())


def check_parents(checkout, relative):
    for parent in reversed(relative.parents):
        path = checkout / parent
        if path.is_symlink() or (path.exists() and not path.is_dir()):
            raise OSError(f"invalid destination directory: {path}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("checkout", type=Path, help="Caelestia checkout containing shell/")
    args = parser.parse_args()
    checkout = args.checkout.resolve()
    patch = (HERE / "caelestia.patch").read_text()
    sections = ["--- " + section for section in patch.split("--- ")[1:]]

    # Keep staged files and backups on the checkout filesystem for atomic renames.
    directory = tempfile.mkdtemp(prefix=".aiquota-install-", dir=checkout)
    preserve = False
    try:
        staged = Path(directory)
        work = staged / "work"
        work.mkdir()
        files = []
        originals = {}
        for section in sections:
            relative = Path(section.splitlines()[0].removeprefix("--- a/"))
            source = checkout / relative
            if not source.is_file():
                parser.error(f"missing host file: {source}")
            originals[relative] = fingerprint(source)
            target = work / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, target)
            files.append(relative)

            def check(*options):
                return subprocess.run(["git", "apply", *options, "-"], input=section,
                                      text=True, cwd=work, capture_output=True)

            if check("--reverse", "--check").returncode == 0:
                continue
            # The earlier integration registered the same component without
            # width or interaction properties. Remove only that exact block.
            if relative.as_posix() == "shell/modules/bar/popouts/Content.qml":
                content = target.read_text()
                if content.count(LEGACY_POPOUT) == 1:
                    target.write_text(content.replace(LEGACY_POPOUT, "", 1))
            result = check("--check")
            if result.returncode:
                parser.error(f"cannot safely apply {relative}:\n{result.stderr.strip()}")
            result = check()
            if result.returncode:
                parser.error(f"cannot apply {relative}:\n{result.stderr.strip()}")

        destination = Path("shell/modules/bar/popouts")
        component = destination / "AIQuota.qml"
        (work / component).parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(HERE / "AIQuota.qml", work / component)
        files.append(component)
        settings_page = Path("shell/modules/nexus/pages/AIQuotaSettings.qml")
        (work / settings_page).parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(HERE / "AIQuotaSettings.qml", work / settings_page)
        files.append(settings_page)
        for source in (HERE / "aiquota-icons").iterdir():
            if not source.is_file() or source.is_symlink():
                parser.error(f"invalid integration file: {source}")
            relative = destination / "aiquota-icons" / source.name
            (work / relative).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, work / relative)
            files.append(relative)

        # Reject directories and symlinks instead of following or replacing them.
        for relative in files:
            target = checkout / relative
            for parent in reversed(relative.parents):
                path = checkout / parent
                if path.is_symlink() or (path.exists() and not path.is_dir()):
                    parser.error(f"invalid destination directory: {path}")
            if target.is_symlink() or (target.exists() and not target.is_file()):
                parser.error(f"invalid destination file: {target}")
            if relative not in originals:
                originals[relative] = fingerprint(target)

        backups = staged / "backups"
        for relative in files:
            target = checkout / relative
            if target.exists():
                backup = backups / relative
                backup.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(target, backup)

        for relative in files:
            check_parents(checkout, relative)
            if fingerprint(checkout / relative) != originals[relative]:
                raise OSError(f"destination changed during installation: {checkout / relative}")

        committed = []
        created_dirs = []
        try:
            for relative in files:
                target = checkout / relative
                check_parents(checkout, relative)
                missing = []
                parent = target.parent
                while not parent.exists():
                    missing.append(parent)
                    parent = parent.parent
                for parent in reversed(missing):
                    created_dirs.append(parent)
                    parent.mkdir()
                if fingerprint(target) != originals[relative]:
                    raise OSError(f"destination changed during installation: {target}")
                # Record the target first so an interrupt immediately after the
                # rename cannot leave an untracked committed file.
                committed.append((relative, fingerprint(work / relative)))
                os.replace(work / relative, target)
        except (OSError, KeyboardInterrupt):
            preserve = True
            print(f"Rollback in progress; recovery files at {staged}", file=sys.stderr)
            failures = []
            for relative, installed in reversed(committed):
                target = checkout / relative
                backup = backups / relative
                try:
                    check_parents(checkout, relative)
                    current = fingerprint(target)
                    if current == originals[relative]:
                        continue
                    if current != installed:
                        raise OSError("destination changed after installation; not restoring")
                    if backup.exists():
                        os.replace(backup, target)
                    else:
                        target.unlink(missing_ok=True)
                except (OSError, KeyboardInterrupt) as error:
                    failures.append((target, error))
            for parent in reversed(created_dirs):
                try:
                    if parent.exists():
                        parent.rmdir()
                except (OSError, KeyboardInterrupt) as error:
                    failures.append((parent, error))
            if failures:
                print(f"Rollback incomplete; recovery files preserved at {staged}", file=sys.stderr)
                for path, error in failures:
                    print(f"  {path}: {error}", file=sys.stderr)
            else:
                preserve = False
            raise
    finally:
        if not preserve:
            shutil.rmtree(directory)
    print("AIQuota installed in checkout. Deploy or reload the shell separately.")


if __name__ == "__main__":
    main()
