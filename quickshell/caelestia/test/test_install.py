import importlib.util
import io
import os
from pathlib import Path
import re
import sys
import tempfile
import unittest
from unittest import mock


HERE = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("installer", HERE / "install.py")
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


def make_host(root):
    # Construct patch preimages from the original-side hunk lines.
    patch = (HERE / "caelestia.patch").read_text()
    for section in patch.split("--- ")[1:]:
        lines = section.splitlines()
        path = root / lines[0].removeprefix("a/")
        content = []
        index = 2
        while index < len(lines):
            match = re.match(r"@@ -(\d+)(?:,\d+)? \+\d+(?:,\d+)? @@", lines[index])
            if not match:
                index += 1
                continue
            start = int(match.group(1)) - 1
            while len(content) < start:
                content.append("// filler")
            index += 1
            while index < len(lines) and not lines[index].startswith("@@ "):
                line = lines[index]
                if line.startswith((" ", "-")):
                    content.append(line[1:])
                index += 1
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("\n".join(content) + "\n")


def snapshot(root):
    return {str(path.relative_to(root)): ("dir" if path.is_dir() else path.read_bytes())
            for path in root.rglob("*")}


class InstallTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        make_host(self.root)

    def run_install(self):
        with mock.patch.object(sys, "argv", ["install.py", str(self.root)]):
            installer.main()

    def test_success_and_repeat_preserve_extra_files(self):
        extra = self.root / "shell/modules/bar/popouts/aiquota-icons/user.svg"
        extra.parent.mkdir(parents=True)
        extra.write_text("user")
        self.run_install()
        first = snapshot(self.root)
        self.assertIn("shell/modules/nexus/pages/AIQuotaSettings.qml", first)
        self.assertIn('label: Tr.tr("AIQuota")',
                      (self.root / "shell/modules/nexus/PageRegistry.qml").read_text())
        self.run_install()
        self.assertEqual(first, snapshot(self.root))
        self.assertEqual(extra.read_text(), "user")

    def test_directory_at_icon_destination_is_rejected(self):
        path = self.root / "shell/modules/bar/popouts/aiquota-icons/codex.svg"
        path.mkdir(parents=True)
        before = snapshot(self.root)
        with self.assertRaises(SystemExit):
            self.run_install()
        self.assertEqual(before, snapshot(self.root))

    def test_staging_failure_leaves_host_unchanged(self):
        before = snapshot(self.root)
        copy = installer.shutil.copy2

        def fail(source, destination, *args, **kwargs):
            if Path(source) == HERE / "AIQuota.qml":
                raise OSError("forced staging failure")
            return copy(source, destination, *args, **kwargs)

        with mock.patch.object(installer.shutil, "copy2", side_effect=fail):
            with self.assertRaises(OSError):
                self.run_install()
        self.assertEqual(before, snapshot(self.root))

    def test_commit_failure_restores_existing_and_new_files(self):
        component = self.root / "shell/modules/bar/popouts/AIQuota.qml"
        component.write_text("existing component")
        before = snapshot(self.root)
        replace = os.replace
        calls = 0

        def fail(source, destination):
            nonlocal calls
            calls += 1
            if calls == 7:
                raise OSError("forced commit failure")
            return replace(source, destination)

        with mock.patch.object(installer.os, "replace", side_effect=fail):
            with self.assertRaises(OSError):
                self.run_install()
        self.assertEqual(before, snapshot(self.root))

    def test_interrupt_after_commit_restores_host(self):
        before = snapshot(self.root)
        replace = os.replace
        calls = 0

        def interrupt(source, destination):
            nonlocal calls
            calls += 1
            result = replace(source, destination)
            if calls == 3:
                raise KeyboardInterrupt()
            return result

        with mock.patch.object(installer.os, "replace", side_effect=interrupt):
            with self.assertRaises(KeyboardInterrupt):
                self.run_install()
        self.assertEqual(before, snapshot(self.root))

    def test_change_during_staging_is_not_overwritten(self):
        target = self.root / "shell/modules/bar/popouts/Content.qml"
        copy = installer.shutil.copy2

        def change(source, destination, *args, **kwargs):
            result = copy(source, destination, *args, **kwargs)
            if Path(source) == HERE / "AIQuota.qml":
                target.write_text("concurrent edit")
            return result

        with mock.patch.object(installer.shutil, "copy2", side_effect=change):
            with self.assertRaisesRegex(OSError, "destination changed"):
                self.run_install()
        self.assertEqual(target.read_text(), "concurrent edit")

    def test_concurrent_edit_during_commit_is_not_rolled_back(self):
        replace = os.replace
        changed = None
        calls = 0

        def change(source, destination):
            nonlocal calls, changed
            calls += 1
            if calls == 2:
                changed.write_text("concurrent edit")
                raise OSError("forced commit failure")
            result = replace(source, destination)
            if calls == 1:
                changed = Path(destination)
            return result

        with mock.patch.object(installer.os, "replace", side_effect=change):
            with self.assertRaisesRegex(OSError, "forced commit failure"):
                self.run_install()
        self.assertEqual(changed.read_text(), "concurrent edit")
        recovery, = self.root.glob(".aiquota-install-*")
        self.assertTrue((recovery / "backups" / changed.relative_to(self.root)).exists())

    def test_second_interrupt_before_rollback_preserves_backups(self):
        replace = os.replace
        fingerprint = installer.fingerprint
        interrupted = False

        def fail(source, destination):
            nonlocal interrupted
            result = replace(source, destination)
            interrupted = True
            raise KeyboardInterrupt()

        def interrupt_rollback(path):
            if interrupted:
                raise KeyboardInterrupt()
            return fingerprint(path)

        with mock.patch.object(installer.os, "replace", side_effect=fail), \
                mock.patch.object(installer, "fingerprint", side_effect=interrupt_rollback):
            with self.assertRaises(KeyboardInterrupt):
                self.run_install()
        recovery, = self.root.glob(".aiquota-install-*")
        self.assertTrue(any(path.is_file() for path in (recovery / "backups").rglob("*")))

    def test_rollback_failure_attempts_remaining_restores_and_preserves_backup(self):
        before = snapshot(self.root)
        replace = os.replace
        calls = 0
        failed_target = None

        def fail(source, destination):
            nonlocal calls, failed_target
            calls += 1
            if calls == 4:
                raise OSError("forced commit failure")
            if calls == 5:
                failed_target = Path(destination)
                raise OSError("forced rollback failure")
            return replace(source, destination)

        stderr = io.StringIO()
        with mock.patch.object(installer.os, "replace", side_effect=fail), \
                mock.patch.object(sys, "stderr", stderr):
            with self.assertRaisesRegex(OSError, "forced commit failure"):
                self.run_install()
        recovery, = self.root.glob(".aiquota-install-*")
        self.assertIn(str(recovery), stderr.getvalue())
        self.assertIn("forced rollback failure", stderr.getvalue())
        relative = str(failed_target.relative_to(self.root))
        self.assertEqual((recovery / "backups" / relative).read_bytes(), before[relative])
        for path, content in before.items():
            if path != relative:
                target = self.root / path
                self.assertEqual("dir" if target.is_dir() else target.read_bytes(), content)

    def test_legacy_registration_is_replaced_once(self):
        content = self.root / "shell/modules/bar/popouts/Content.qml"
        text = content.read_text()
        marker = '        Popout {\n            name: "battery"'
        content.write_text(text.replace(marker, installer.LEGACY_POPOUT + marker))
        self.run_install()
        installed = content.read_text()
        self.assertNotIn(installer.LEGACY_POPOUT, installed)
        self.assertEqual(installed.count('name: "aiquota"'), 1)
        self.run_install()
        self.assertEqual(content.read_text(), installed)


if __name__ == "__main__":
    unittest.main()
