#!/bin/sh
set -eu
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
shell_dir=${1:-"$HOME/code/caelestia/shell"}
test_dir="$project_dir/quickshell/caelestia/test"
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM
mkdir -p "$work_dir/shell/modules/bar/popouts" "$work_dir/shell/modules/bar/components" "$work_dir/shell/modules/drawers"
for f in modules/bar/BarWrapper.qml modules/bar/Bar.qml modules/bar/popouts/Content.qml modules/bar/popouts/Wrapper.qml modules/bar/components/StatusIcons.qml modules/drawers/ContentWindow.qml modules/drawers/Interactions.qml; do cp "$shell_dir/$f" "$work_dir/shell/$f"; done
# Apply each hunk independently: a local checkout can contain a mixture of
# patched and unpatched files (or even hunks). Only the temporary copies change.
python3 - "$work_dir" "$project_dir/quickshell/caelestia/caelestia.patch" <<'PY'
from pathlib import Path
import subprocess
import sys

work_dir, patch_file = sys.argv[1:]
# An older local installation used a shorter AIQuota popout declaration.
# Remove that exact legacy block so the current hunk can replace it.
content = Path(work_dir) / 'shell/modules/bar/popouts/Content.qml'
legacy = '        Popout {\n            name: "aiquota"\n            sourceComponent: AIQuota {}\n        }\n\n'
text = content.read_text()
if text.count(legacy) == 1:
    content.write_text(text.replace(legacy, '', 1))
lines = Path(patch_file).read_text().splitlines(keepends=True)
header = None
hunks = []
for line in lines:
    if line.startswith('--- '):
        header = [line]
    elif line.startswith('+++ ') and header is not None and len(header) == 1:
        header.append(line)
    elif line.startswith('@@ '):
        if header is None or len(header) != 2:
            raise SystemExit('Invalid Caelestia patch header')
        hunks.append(header.copy() + [line])
    elif hunks and header is not None:
        hunks[-1].append(line)

if not hunks:
    raise SystemExit('Caelestia patch contains no hunks')
for hunk in hunks:
    data = ''.join(hunk)
    command = ['patch', '--batch', '--silent', '--fuzz=0', '-p1']
    def run(*options):
        return subprocess.run(command + list(options), input=data, text=True,
                              cwd=work_dir, stdout=subprocess.DEVNULL,
                              stderr=subprocess.DEVNULL).returncode == 0
    if run('--dry-run', '--forward'):
        if not run('--forward'):
            raise SystemExit(f'Could not apply Caelestia patch hunk: {hunk[0].strip()} {hunk[2].strip()}')
    elif not run('--dry-run', '--reverse'):
        raise SystemExit(f'Could not apply or recognize Caelestia patch hunk: {hunk[0].strip()} {hunk[2].strip()}')
PY
python3 - "$work_dir" <<'PY'
from pathlib import Path
import sys
root = Path(sys.argv[1])
p = root/'shell/modules/drawers/Interactions.qml'
s = p.read_text().replace('import qs.modules.bar as Bar','').replace('import qs.modules.bar.popouts as BarPopouts','')
s = s.replace('required property ShellScreen screen','required property var screen').replace('required property BarPopouts.Wrapper popouts','required property var popouts').replace('required property ScreenState screenState','required property var screenState').replace('required property Panels panels','required property var panels').replace('required property Bar.BarWrapper bar','required property var bar')
s = s.replace('const x = event.x;', 'const x = event.x;\n        console.log("MOUSE_POS", event.x, event.y, popouts.hasCurrent);')
(root/'Interactions.qml').write_text(s)
PY
for n in components services utils; do ln -s "$shell_dir/$n" "$work_dir/$n"; done
cp "$test_dir/hover-shell.qml" "$work_dir/shell.qml"
cp "$test_dir/hover-driver.sh" "$work_dir/run.sh"
chmod +x "$work_dir/run.sh"
if ! TESTDIR="$work_dir" QT_QPA_PLATFORM=xcb XDG_CONFIG_HOME="$work_dir/config" timeout 15s xvfb-run -a "$work_dir/run.sh"; then cat "$work_dir/log" >&2; exit 1; fi
if ! grep -q 'EARLY_STATE false' "$work_dir/log" || ! grep -q 'MOUSE_POS 390 30 true' "$work_dir/log" || ! grep -q 'MOUSE_POS 350 100 true' "$work_dir/log" || ! grep -q 'FINAL_STATE false' "$work_dir/log"; then cat "$work_dir/log" >&2; exit 1; fi
printf 'PASS corner dwell, transition into popup, and close\n'
