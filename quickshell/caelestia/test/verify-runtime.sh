#!/bin/sh
# Exercise overview/detail behavior against real Quickshell and a local fake CLI.
set -eu
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
shell_dir=${1:-"$HOME/code/caelestia/shell"}
for required in components services utils; do
    test -d "$shell_dir/$required" || { printf 'Missing %s in %s\n' "$required" "$shell_dir" >&2; exit 1; }
done
command -v xvfb-run >/dev/null
command -v xdotool >/dev/null
command -v quickshell >/dev/null
work_dir=$(mktemp -d "${TMPDIR:-/tmp/opencode}/aiquota-runtime.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM
for required in components services utils; do ln -s "$shell_dir/$required" "$work_dir/$required"; done
cp "$project_dir/quickshell/caelestia/AIQuota.qml" "$work_dir/AIQuota.qml"
cp -R "$project_dir/quickshell/caelestia/aiquota-icons" "$work_dir/aiquota-icons"
cat > "$work_dir/fake-aiquota" <<'FIXTURE'
#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$AIQUOTA_TEST_DIR/commands"
test "$*" = 'status --cached --json' || exit 2
case "$AIQUOTA_TEST_CASE" in
  valid|local|path)
    if [ -e "$AIQUOTA_TEST_DIR/invalid-refresh" ]; then
      printf '%s\n' '{invalid'
    elif [ -e "$AIQUOTA_TEST_DIR/removed" ]; then
      printf '%s\n' '{"schema_version":1,"providers":[]}'
    elif [ -e "$AIQUOTA_TEST_DIR/refresh" ]; then
      printf '%s\n' '{"schema_version":1,"providers":[{"id":"antigravity","name":"Antigravity updated","enabled":false,"status":"disabled","windows":[{"id":"new","duration_minutes":60,"remaining_percent":22}]}]}'
    else
      printf '%s\n' '{"schema_version":1,"providers":[{"id":"claude","name":"Claude Code","enabled":false,"status":"disabled","message":"Collection disabled","windows":[]},{"id":"codex","name":"Codex","enabled":true,"status":"stale","message":"Old data","windows":[{"id":"unknown","label":"Unknown quota","remaining_percent":null},{"id":"known","duration_minutes":300,"remaining_percent":40,"resets_at":null},{"id":"q0","duration_minutes":301,"remaining_percent":0},{"id":"q29","duration_minutes":302,"remaining_percent":29},{"id":"q30","duration_minutes":303,"remaining_percent":30},{"id":"q59","duration_minutes":304,"remaining_percent":59},{"id":"q60","duration_minutes":305,"remaining_percent":60},{"id":"q100","duration_minutes":306,"remaining_percent":100}]},{"id":"antigravity","name":"Antigravity","enabled":true,"status":"healthy","plan":"Pro","credits":{"balance":0},"last_success":null,"windows":[{"id":"gemini-5h","label":"Five Hour Limit Remaining","duration_minutes":300,"remaining_percent":100,"resets_at":"2099-11-16T01:30:00Z","model":"Gemini Models","used_amount":0,"remaining_amount":2,"allocation_limit":2,"unit":"requests"},{"id":"gemini-week","label":"Weekly Limit Remaining","duration_minutes":10080,"remaining_percent":80,"resets_at":null,"model":"Gemini Models"},{"id":"other","label":"Mystery quota","remaining_percent":null,"model":"Other Model"},{"id":"spend-a","bucket_id":"api","duration_minutes":1440,"remaining_percent":null,"used_amount":0,"allocation_limit":25,"unit":"USD","model":"Spend"},{"id":"spend-b","bucket_id":"web","duration_minutes":1440,"remaining_percent":null,"used_amount":4,"allocation_limit":10,"unit":"USD","model":"Spend"}]}]}'
    fi ;;
  malformed) printf '%s\n' '{invalid' ;;
  wrong_schema) printf '%s\n' '{"schema_version":999,"providers":[]}' ;;
  failed) exit 1 ;;
  output_fail|delayed|hang)
    if [ -e "$AIQUOTA_TEST_DIR/retry" ]; then
      printf '%s\n' '{"schema_version":1,"providers":[{"id":"fresh"}]}'
    else
      exec python3 -c 'import os, signal, sys, time
case = os.environ["AIQUOTA_TEST_CASE"]
if case == "hang": signal.signal(signal.SIGTERM, signal.SIG_IGN)
print("{\"schema_version\":1,\"providers\":[{\"id\":\"stale\"}]}", flush=True)
sys.stdout.close()
os.close(1)
time.sleep(20 if case == "hang" else 1)
sys.exit(1 if case == "output_fail" else 0)'
    fi ;;
esac
FIXTURE
chmod 700 "$work_dir/fake-aiquota"
cat > "$work_dir/shell.qml" <<'QML'
import QtQuick
import QtQuick.Controls
import Quickshell
ShellRoot {
    function find(item, name) {
        if (item.objectName === name) return item;
        if (item.children) for (const child of item.children) {
            const found = find(child, name);
            if (found) return found;
        }
        return null;
    }
    function hasText(item, expected) {
        if (item.text === expected && item.visible) return true;
        if (item.children) for (const child of item.children) if (hasText(child, expected)) return true;
        return false;
    }
    function findText(item, expected) {
        if (item.text === expected && item.visible) return item;
        if (item.children) for (const child of item.children) {
            const found = findText(child, expected);
            if (found) return found;
        }
        return null;
    }
    function countText(item, expected) {
        let n = item.text === expected && item.visible ? 1 : 0;
        if (item.children) for (const child of item.children) n += countText(child, expected);
        return n;
    }
    function hasCheckable(item) {
        if (item.checkable === true) return true;
        if (item.children) for (const child of item.children) if (hasCheckable(child)) return true;
        return false;
    }
    function check(ok, what) {
        if (!ok) { console.error("RUNTIME_FAIL", what); Qt.exit(1); return false; }
        console.log("RUNTIME_CHECK", what); return true;
    }
    function contrast(a, b) {
        function luminance(color) {
            const channels = [1, 3, 5].map(i => parseInt(String(color).slice(i, i + 2), 16) / 255)
                .map(c => c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4));
            return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
        }
        const x = luminance(a), y = luminance(b);
        return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
    }
    function readyLogo(id) { const item = find(quota, "logo-" + id); return item && item.status === Image.Ready; }
    function marker(name, item) {
        const p = item.mapToItem(null, item.width / 2, item.height / 2);
        console.log(name, Math.round(p.x), Math.round(p.y));
    }
    Window {
        id: win
        title: "AIQuota Runtime Test"
        width: 800; height: 750; visible: true
        AIQuota { id: quota; width: 480; height: 700; executable: Quickshell.env("AIQUOTA_FAKE") || "aiquota"; focus: true; Keys.onPressed: event => { if (event.key === Qt.Key_F5) { readCache(); event.accepted = true; } } }
        property int stage: 0
        property var originalProviders: []
        Timer {
            interval: 100; repeat: true; running: true
            onTriggered: {
                const tc = Quickshell.env("AIQUOTA_TEST_CASE");
                if (["output_fail", "delayed", "hang"].includes(tc)) {
                    if (win.stage === 0 && quota.readerStarted && !quota.readerExited) {
                        if (!check(quota.providers.length === 0 && quota.error.startsWith("Loading"), tc + " output staged")) return;
                        if (tc === "hang") {
                            quota.readCache();
                            console.log("RUNTIME_QUEUED");
                        }
                        win.stage = 1;
                    } else if (win.stage === 1 && tc === "delayed" && !quota.readingCache) {
                        if (!check(quota.providers.length === 1 && quota.providers[0].id === "stale" && quota.error === "", "delayed success published after exit")) return;
                        console.log("RUNTIME_PASS", tc); Qt.quit();
                    } else if (win.stage === 1 && tc === "output_fail" && !quota.readingCache) {
                        if (!check(quota.providers.length === 0 && quota.error.startsWith("AIQuota is unavailable"), "failed output discarded")) return;
                        console.log("RUNTIME_PASS", tc); Qt.quit();
                    } else if (win.stage === 1 && tc === "hang" && quota.readerTimedOut) {
                        if (!check(quota.readingCache && quota.providers.length === 0 && quota.error.includes("timed out"), "timeout waits for termination")) return;
                        console.log("RUNTIME_TIMED_OUT"); win.stage = 2;
                    } else if (win.stage === 2 && quota.providers.length === 1) {
                        if (!check(quota.providers[0].id === "fresh" && quota.error === "", "queued retry has no stale output")) return;
                        console.log("RUNTIME_PASS", tc); Qt.quit();
                    }
                    return;
                }
                if (tc === "local" || tc === "path") {
                    if (quota.providers.length === 3 && quota.error === "") {
                        console.log("RUNTIME_PASS", tc); Qt.quit();
                    }
                    return;
                }
                if (tc !== "valid") {
                    if (!quota.error.startsWith("Loading") && quota.error.length > 0) {
                        if (!check(quota.providers.length === 0, tc + " empty providers")) return;
                        const expected = tc === "failed" || tc === "missing" || tc === "missing_path" ? "AIQuota is unavailable. Check that it is installed in ~/.local/bin or on PATH." : "Cannot read AIQuota cached status.";
                        if (!check(quota.error === expected, tc + " error")) return;
                        console.log("RUNTIME_PASS", tc); Qt.quit();
                    }
                    return;
                }
                const overview = find(quota, "provider-overview");
                const detail = find(quota, "provider-insights");
                const back = find(quota, "back-to-providers");
                const card = find(quota, "card-antigravity");
                if (win.stage === 0 && quota.providers.length === 3 && card && readyLogo("claude") && readyLogo("codex") && readyLogo("antigravity")) {
                    for (const [id, bg, accent, text] of [["codex", "#363636", "#10a37f", "#ffffff"], ["antigravity", "#292c33", "#1a73e8", "#e8e8e8"], ["claude", "#383835", "#c96442", "#f5f4ed"]]) {
                        const c = find(quota, "provider-card-" + id);
                        const icon = find(quota, "provider-" + id);
                        if (!check(String(c.color) === bg && String(c.border.color) === accent && String(icon.border.color) === accent && c.palette.text === text, id + " card palette and badge")) return;
                    }
                    for (const [value, state] of [[0,"low"],[29,"low"],[29.9,"moderate"],[30,"moderate"],[59,"moderate"],[59.9,"high"],[60,"high"],[100,"high"],[-0.1,"unknown"],[100.1,"unknown"],[null,"unknown"]]) {
                        if (!check(quota.quotaState(value) === state, "threshold " + value)) return;
                    }
                    if (!check(find(quota, "card-codex").Accessible.name.includes("40 percent remaining, moderate") && find(quota, "card-codex").Accessible.name.includes("Stale"), "overview accessibility")) return;
                    const testCard = find(quota, "provider-card-codex");
                    const originalData = testCard.modelData;
                    for (const [value, color, state] of [[0,"#f87171","low"],[29,"#f87171","low"],[29.9,"#fbbf24","moderate"],[30,"#fbbf24","moderate"],[59,"#fbbf24","moderate"],[59.9,"#4ade80","high"],[60,"#4ade80","high"],[100,"#4ade80","high"],[-0.1,"#00000000","unknown"],[100.1,"#00000000","unknown"],[null,"#00000000","unknown"]]) {
                        testCard.modelData = {id:"codex", name:"Codex", status:"stale", windows:[{id:"test", remaining_percent:value}]};
                        const bar = find(quota, "overview-progress-codex");
                        if (!check(String(bar.color) === color && (state === "unknown" ? !bar.parent.visible && hasText(testCard, "N/A") : Math.abs(bar.width / bar.parent.width - value / 100) < 0.001 && hasText(testCard, Math.round(value) + "%")) && find(quota, "card-codex").Accessible.name.includes(state === "unknown" ? "remaining unknown" : state + " remaining quota"), "overview threshold " + value)) return;
                    }
                    testCard.modelData = originalData;
                    if (!check(quota.providerPalette("other").accent === "#ffffff", "unknown palette neutral")) return;
                    if (!check(Math.abs(find(quota, "overview-progress-codex").width / find(quota, "overview-progress-codex").parent.width - 0.4) < 0.001, "progress shows remaining quota")) return;
                    if (!check(String(find(quota, "overview-progress-codex").color) === "#fbbf24" && String(find(quota, "overview-progress-antigravity").color) === "#4ade80", "overview semantic progress colors")) return;
                    const googleProgress = find(quota, "overview-progress-antigravity");
                    const claudePalette = quota.providerPalette("claude");
                    if (!check(["#4ade80", "#fbbf24", "#f87171"].every(c => [googleProgress.parent.color, claudePalette.track, "#3e3e42"].every(track => contrast(c, track) >= 3)), "semantic progress track contrast")) return;
                    if (!check(String(findText(quota, "100%").color) === "#e8e8e8" && String(findText(quota, "40%").color) === "#ffffff", "provider quota text colors")) return;
                    if (!check(quota.error === "" && overview.visible && !detail.visible && !back.visible, "overview initial")) return;
                    if (!check(hasText(quota, "AI Quota") && quota.implicitWidth === 480 && overview.columns === 3, "compact overview heading and width")) return;
                    if (!check(hasText(quota, "Stale") && hasText(quota, "Disabled") && !hasText(quota, "Old data") && !hasText(quota, "Collection disabled"), "concise stale and disabled status")) return;
                    if (!check(hasText(quota, "100%") && hasText(quota, "40%") && hasText(quota, "N/A"), "overview values and unknown")) return;
                    if (!check(hasText(quota, "5H · Gemini Models") && hasText(quota, "5H"), "overview selected window labels")) return;
                    if (!check(!hasText(quota, "80%") && !hasText(quota, "Other Model") && !hasCheckable(quota), "overview primary only without checkable controls")) return;
                    if (!check(quota.orderedProviders([{id:"claude"},{id:"codex"},{id:"antigravity"}]).map(p => p.id).join(",") === "codex,antigravity,claude", "provider ordering")) return;
                    if (!check(quota.primaryWindow([{id:"unknown",duration_minutes:1,remaining_percent:null},{id:"long",duration_minutes:100,remaining_percent:2},{id:"short",duration_minutes:5,remaining_percent:0}]).id === "short", "primary skips unknown and preserves zero")) return;
                    if (!check(quota.primaryWindow([{id:"negative",duration_minutes:1,remaining_percent:-0.1},{id:"over",duration_minutes:2,remaining_percent:100.1},{id:"valid",duration_minutes:3,remaining_percent:40}]).id === "valid" && !quota.primaryWindow([{remaining_percent:-1},{remaining_percent:101}]), "primary skips out-of-range percentages")) return;
                    if (!check(quota.groups([{bucket_id:"one"},{bucket_id:"two"}]).length === 2 && quota.groups([{duration_minutes:10080},{duration_minutes:300}])[0].windows[0].duration_minutes === 300, "groups and window ordering")) return;
                    const sameBucket = [{id:"a",bucket_id:"api",label:"First limit",duration_minutes:60},{id:"b",bucket_id:"api",label:"Second limit",duration_minutes:60}];
                    if (!check(quota.detailWindow(sameBucket[0], sameBucket) === "1 hour · First limit" && quota.detailWindow(sameBucket[1], sameBucket) === "1 hour · Second limit", "same-bucket windows retain distinct labels")) return;
                    if (!check(quota.displayTime(null) === "Reset unknown" && quota.displayTime("invalid") === "Reset unknown" && quota.displayTime("2000-01-01T00:00:00Z") === "Reset passed", "reset unknown invalid passed")) return;
                    if (!check(quota.timeUntil(null) === "" && quota.timeUntil("2000-01-01T00:00:00Z") === "", "elapsed reset helper")) return;
                    quota.openProvider("claude"); win.stage = 20;
                } else if (win.stage === 20 && detail.visible && quota.selectedProviderId === "claude") {
                    if (!check(detail.palette.card === "#383835" && detail.palette.accent === "#c96442", "claude detail palette")) return;
                    if (!check(!hasCheckable(quota) && hasText(quota,"Disabled") && hasText(quota,"No quota windows available."), "disabled detail has no controls or windows")) return;
                    quota.openProvider("codex"); win.stage = 21;
                } else if (win.stage === 21 && detail.visible && quota.selectedProviderId === "codex") {
                    if (!check(String(find(quota, "detail-tile-known").color) === "#363636" && String(find(quota, "detail-progress-known").color) === "#fbbf24", "codex detail palette")) return;
                    for (const [value, color, state] of [[0,"#f87171","low"],[29,"#f87171","low"],[30,"#fbbf24","moderate"],[59,"#fbbf24","moderate"],[60,"#4ade80","high"],[100,"#4ade80","high"]]) {
                        const bar = find(quota, "detail-progress-q" + value);
                        const tile = find(quota, "detail-tile-q" + value);
                        if (!check(String(bar.color) === color && Math.abs(bar.width / bar.parent.width - value / 100) < 0.001 && tile.Accessible.name.includes(state + " remaining quota") && tile.Accessible.name.includes("Stale"), "detail threshold " + value)) return;
                    }
                    if (!check(!find(quota, "detail-progress-unknown").parent.visible && quota.quotaState(null) === "unknown" && quota.quotaDescription(null) === "remaining unknown", "unknown has no fill")) return;
                    const testTile = find(quota, "detail-tile-known");
                    const originalTileData = testTile.modelData;
                    for (const [value, color, state] of [[29.9,"#fbbf24","moderate"],[59.9,"#4ade80","high"],[-0.1,"#00000000","unknown"],[100.1,"#00000000","unknown"]]) {
                        testTile.modelData = {id:"test", remaining_percent:value};
                        const bar = find(quota, "detail-progress-test");
                        if (!check(String(bar.color) === color && (state === "unknown" ? !bar.parent.visible && hasText(testTile, "N/A") : bar.parent.visible && hasText(testTile, Math.round(value) + "%")) && testTile.Accessible.name.includes(state === "unknown" ? "remaining unknown" : state + " remaining quota"), "detail fractional or invalid " + value)) return;
                    }
                    testTile.modelData = originalTileData;
                    if (!check(!hasCheckable(quota) && hasText(quota,"Stale") && hasText(quota,"40%") && hasText(quota,"N/A"), "stale detail has no controls and shows quota states")) return;
                    quota.closeProvider(); win.stage = 22;
                } else if (win.stage === 22 && overview.visible) {
                    marker("RUNTIME_CARD_READY", card); win.stage = 1;
                } else if (win.stage === 1 && quota.selectedProviderId === "antigravity" && detail.visible) {
                    if (!check(String(find(quota, "detail-tile-gemini-5h").color) === "#292c33" && String(find(quota, "detail-progress-gemini-5h").color) === "#4ade80", "google detail palette")) return;
                    if (!check(contrast(find(quota, "detail-progress-gemini-5h").color, find(quota, "detail-progress-gemini-5h").parent.color) >= 3, "detail progress track contrast")) return;
                    if (!check(!overview.visible && back.visible && !hasText(quota,"Antigravity") && !hasText(quota,"AI Quota") && !hasText(quota,"Remaining quota") && !hasCheckable(quota), "mouse selection and compact detail header without checkable controls")) return;
                    if (!check(hasText(quota,"Gemini Models") && hasText(quota,"Other Model") && hasText(quota,"5 hours") && hasText(quota,"7 days") && hasText(quota,"Mystery quota"), "all model groups and windows")) return;
                    if (!check(hasText(quota,"100%") && hasText(quota,"80%") && countText(quota,"N/A") === 1 && hasText(quota,"Reset unknown"), "detail percentages and unknown")) return;
                    if (!check(!hasText(quota, "Remaining quota") && find(quota, "detail-tile-gemini-5h").Accessible.name.includes("100 percent remaining"), "detail quota meaning remains accessible without repeated label")) return;
                    if (!check(!hasText(quota,"Five Hour Limit Remaining") && quota.amountText(quota.selectedProvider.windows[0]).includes("Used 0 requests"), "compact labels and preserved amounts")) return;
                    if (!check(hasText(quota,"2 / 2 requests left") && hasText(quota,"1 day · api") && hasText(quota,"1 day · web") && hasText(quota,"0 / 25 USD used") && hasText(quota,"4 / 10 USD used"), "distinct buckets and labeled amounts")) return;
                    if (!check(quota.windowIdentity(quota.selectedProvider.windows[3]).includes("Spend · api · 1 day"), "full bucket tooltip identity")) return;
                    if (!check(hasText(quota,"Pro  ·  Credits 0") && !hasText(quota,"Last successful update"), "compact account summary")) return;
                    const switcher = find(quota, "provider-switcher");
                    if (!check(switcher.visible && find(quota, "switch-provider-antigravity").Accessible.name === "Antigravity, selected" && find(quota, "switch-provider-antigravity").ToolTip.text === "Antigravity" && back.Accessible.name === "Back to providers" && !hasCheckable(switcher), "switcher selection accessibility without toggles")) return;
                    const first = find(quota, "detail-tile-gemini-5h");
                    const second = find(quota, "detail-tile-gemini-week");
                    if (!check(first.parent.columns === 2 && first.y === second.y && first.x + first.width <= second.x && first.width > 0, "wide detail tiles form two non-overlapping columns")) return;
                    quota.width = 180; win.stage = 10;
                } else if (win.stage === 10) {
                    const first = find(quota, "detail-tile-gemini-5h");
                    const second = find(quota, "detail-tile-gemini-week");
                    if (!check(first.parent.columns === 1 && first.x === second.x && first.y + first.height <= second.y && first.width > 0 && first.width <= first.parent.width, "narrow detail tiles stack without overlap")) return;
                    if (!check(find(quota, "provider-switcher").width > 0 && !hasText(quota, "Antigravity"), "narrow header retains icon switcher without provider name")) return;
                    first.forceActiveFocus();
                    win.stage = 12;
                } else if (win.stage === 12) {
                    const first = find(quota, "detail-tile-gemini-5h");
                    if (!check(first.activeFocus && first.Accessible.name.includes("Five Hour Limit Remaining"), "detail information available on keyboard focus")) return;
                    quota.width = 480; win.stage = 11;
                } else if (win.stage === 11) {
                    const first = find(quota, "detail-tile-gemini-5h");
                    const second = find(quota, "detail-tile-gemini-week");
                    if (!check(first.parent.columns === 2 && first.y === second.y && first.x + first.width <= second.x, "detail tiles return to two columns after resize")) return;
                    marker("RUNTIME_SWITCH_READY", find(quota, "switch-provider-codex")); win.stage = 30;
                } else if (win.stage === 30 && quota.selectedProviderId === "codex") {
                    if (!check(detail.visible && hasText(quota, "Stale") && hasText(quota, "40%") && !hasText(quota, "Gemini Models"), "mouse switch updates details directly")) return;
                    const button = find(quota, "switch-provider-claude");
                    button.forceActiveFocus();
                    if (!check(button.activeFocus && button.Accessible.name.includes("Claude Code"), "switcher keyboard focus and label")) return;
                    console.log("RUNTIME_SWITCH_FOCUSED"); win.stage = 31;
                } else if (win.stage === 31 && quota.selectedProviderId === "claude") {
                    if (!check(hasText(quota, "Disabled") && hasText(quota, "No quota windows available.") && find(quota, "switch-provider-claude").activeFocus, "keyboard switch retains focus and updates detail")) return;
                    win.originalProviders = quota.providers;
                    quota.providers = win.originalProviders.concat(Array.from({length: 12}, (_, i) => ({id: "unknown" + i, name: "Unknown " + i, status: "unavailable", windows: []})));
                    quota.width = 180;
                    win.stage = 33;
                } else if (win.stage === 33) {
                    const switcher = find(quota, "provider-switcher");
                    const unknown = find(quota, "switch-provider-unknown11");
                    unknown.forceActiveFocus();
                    if (!check(switcher.width > 0 && switcher.contentWidth > switcher.width && switcher.contentX > 0 && unknown.activeFocus && hasText(unknown, "UN"), "narrow switcher scrolls to focused unknown provider")) return;
                    quota.providers = quota.providers.slice().reverse();
                    win.stage = 35;
                } else if (win.stage === 35) {
                    const switcher = find(quota, "provider-switcher");
                    const unknown = find(quota, "switch-provider-unknown11");
                    if (!check(unknown.activeFocus && switcher.contentX < unknown.x + 4 + unknown.width && switcher.contentX + switcher.width > unknown.x + 4, "refresh restores focused provider after same-count reorder")) return;
                    quota.width = 480;
                    win.stage = 36;
                } else if (win.stage === 36) {
                    quota.width = 180;
                    win.stage = 37;
                } else if (win.stage === 37) {
                    const switcher = find(quota, "provider-switcher");
                    const unknown = find(quota, "switch-provider-unknown11");
                    if (!check(unknown.activeFocus && switcher.contentX < unknown.x + 4 + unknown.width && switcher.contentX + switcher.width > unknown.x + 4, "resize reveals focused provider")) return;
                    back.forceActiveFocus();
                    quota.selectedProviderId = "unknown11";
                    quota.providers = quota.providers.slice().reverse();
                    win.stage = 38;
                } else if (win.stage === 38) {
                    const switcher = find(quota, "provider-switcher");
                    const unknown = find(quota, "switch-provider-unknown11");
                    if (!check(back.activeFocus && switcher.contentX < unknown.x + 4 + unknown.width && switcher.contentX + switcher.width > unknown.x + 4, "same-count reorder reveals selection without stealing focus")) return;
                    quota.selectedProviderId = "claude";
                    quota.providers = win.originalProviders;
                    quota.width = 480;
                    win.stage = 34;
                } else if (win.stage === 34) {
                    find(quota, "switch-provider-antigravity").forceActiveFocus();
                    marker("RUNTIME_SWITCH_BACK", find(quota, "switch-provider-antigravity")); win.stage = 32;
                } else if (win.stage === 32 && quota.selectedProviderId === "antigravity") {
                    if (!check(hasText(quota, "Gemini Models") && find(quota, "switch-provider-antigravity").Accessible.name === "Antigravity, selected" && find(quota, "switch-provider-antigravity").ToolTip.text === "Antigravity" && back.Accessible.name === "Back to providers", "switch back restores detail and selection")) return;
                    marker("RUNTIME_BACK_READY", back); win.stage = 2;
                } else if (win.stage === 2 && quota.selectedProviderId === "" && overview.visible) {
                    if (!check(!detail.visible && !back.visible, "mouse back to overview")) return;
                    const keyboardCard = find(quota,"card-antigravity");
                    if (!check(keyboardCard.activeFocus, "back restores focus to last selected provider")) return;
                    keyboardCard.forceActiveFocus();
                    if (!check(keyboardCard.activeFocus, "card keyboard focus")) return;
                    if (!check(String(find(quota, "provider-card-antigravity").border.color) === "#e8e8e8", "focused card border")) return;
                    console.log("RUNTIME_CARD_FOCUSED"); win.stage = 3;
                } else if (win.stage === 3 && quota.selectedProviderId === "antigravity" && detail.visible) {
                    if (!check(true, "keyboard card activation")) return;
                    back.forceActiveFocus();
                    if (!check(back.activeFocus, "back keyboard focus")) return;
                    console.log("RUNTIME_BACK_FOCUSED"); win.stage = 4;
                } else if (win.stage === 4 && quota.selectedProviderId === "" && overview.visible) {
                    if (!check(true, "keyboard back activation")) return;
                    quota.openProvider("antigravity");
                    console.log("RUNTIME_REFRESH_READY"); win.stage = 5;
                } else if (win.stage === 5 && quota.providers.length === 1 && quota.selectedProvider && quota.selectedProvider.name === "Antigravity updated") {
                    if (!check(quota.selectedProviderId === "antigravity" && detail.visible && !overview.visible && !quota.selectedProvider.enabled && hasText(quota,"Disabled") && !hasCheckable(quota) && hasText(quota,"22%") && !hasText(quota,"Antigravity updated") && !hasText(quota,"80%"), "external config refresh retains selection and replaces status and windows")) return;
                    if (!check(find(quota, "switch-provider-antigravity").Accessible.name === "Antigravity updated, selected" && find(quota, "switch-provider-antigravity").ToolTip.text === "Antigravity updated" && back.Accessible.name === "Back to providers" && !find(quota, "switch-provider-codex"), "switcher reflects refreshed name, selection and removed providers")) return;
                    console.log("RUNTIME_REMOVE_READY"); win.stage = 6;
                } else if (win.stage === 6 && quota.providers.length === 0 && quota.error === "") {
                    if (!check(quota.selectedProviderId === "" && overview.visible && !detail.visible, "removed selection returns to overview")) return;
                    quota.openProvider("missing");
                    console.log("RUNTIME_INVALID_READY"); win.stage = 7;
                } else if (win.stage === 7 && quota.error === "Cannot read AIQuota cached status.") {
                    if (!check(quota.selectedProviderId === "" && quota.providers.length === 0 && overview.visible && !detail.visible, "invalid refresh clears selection")) return;
                    console.log("RUNTIME_PASS valid"); Qt.quit(); win.stage = 8;
                }
            }
        }
    }
}
QML
cat > "$work_dir/driver.sh" <<'DRIVER'
#!/bin/sh
set -eu
quickshell --no-color -p "$AIQUOTA_TEST_DIR/shell.qml" > "$AIQUOTA_TEST_DIR/run.log" 2>&1 &
pid=$!
trap 'kill "$pid" 2>/dev/null || true' EXIT
wait_for() {
    n=0
    while ! grep -q "$1" "$AIQUOTA_TEST_DIR/run.log"; do
        if ! kill -0 "$pid" 2>/dev/null; then cat "$AIQUOTA_TEST_DIR/run.log" >&2; exit 1; fi
        n=$((n+1)); if [ "$n" -gt 100 ]; then cat "$AIQUOTA_TEST_DIR/run.log" >&2; exit 1; fi
        sleep 0.1
    done
}
click_marker() {
    set -- $(grep "$1" "$AIQUOTA_TEST_DIR/run.log" | tail -1)
    x=$(echo "$*" | awk '{print $(NF-1)}'); y=$(echo "$*" | awk '{print $NF}')
    window=$(xdotool search --name '^AIQuota Runtime Test$' | head -1)
    xdotool mousemove --window "$window" "$x" "$y" click 1
}
if [ "$AIQUOTA_TEST_CASE" = valid ]; then
    wait_for RUNTIME_CARD_READY; click_marker RUNTIME_CARD_READY
    wait_for RUNTIME_SWITCH_READY; click_marker RUNTIME_SWITCH_READY
    wait_for RUNTIME_SWITCH_FOCUSED; window=$(xdotool search --name '^AIQuota Runtime Test$' | head -1); xdotool windowfocus "$window"; xdotool key --window "$window" space
    wait_for RUNTIME_SWITCH_BACK; click_marker RUNTIME_SWITCH_BACK
    wait_for RUNTIME_BACK_READY; click_marker RUNTIME_BACK_READY
    wait_for RUNTIME_CARD_FOCUSED; window=$(xdotool search --name '^AIQuota Runtime Test$' | head -1); xdotool windowfocus "$window"; xdotool key --window "$window" space
    wait_for RUNTIME_BACK_FOCUSED; xdotool key --window "$window" space
    wait_for RUNTIME_REFRESH_READY; touch "$AIQUOTA_TEST_DIR/refresh"; xdotool key --window "$window" F5
    wait_for RUNTIME_REMOVE_READY; touch "$AIQUOTA_TEST_DIR/removed"; xdotool key --window "$window" F5
    wait_for RUNTIME_INVALID_READY; touch "$AIQUOTA_TEST_DIR/invalid-refresh"; xdotool key --window "$window" F5
fi
if [ "$AIQUOTA_TEST_CASE" = hang ]; then
    wait_for RUNTIME_QUEUED
    touch "$AIQUOTA_TEST_DIR/retry"
    wait_for RUNTIME_TIMED_OUT
    test "$(wc -l < "$AIQUOTA_TEST_DIR/commands")" -eq 1
fi
wait_for "RUNTIME_PASS $AIQUOTA_TEST_CASE"
wait "$pid"
DRIVER
chmod 700 "$work_dir/driver.sh"
mkdir -p "$work_dir/home/.local/bin" "$work_dir/path-bin"
for test_case in valid malformed wrong_schema failed local path missing missing_path output_fail delayed hang; do
    rm -f "$work_dir/home/.local/bin/aiquota" "$work_dir/path-bin/aiquota"
    rm -f "$work_dir/invalid-refresh" "$work_dir/removed" "$work_dir/refresh" "$work_dir/retry" "$work_dir/commands"
    fake="$work_dir/fake-aiquota"
    case "$test_case" in
        local) ln -s "$work_dir/fake-aiquota" "$work_dir/home/.local/bin/aiquota"; fake= ;;
        path) ln -s "$work_dir/fake-aiquota" "$work_dir/path-bin/aiquota"; fake= ;;
        missing) fake="$work_dir/does-not-exist" ;;
        missing_path) fake= ;;
    esac
    if ! HOME="$work_dir/home" PATH="$work_dir/path-bin:/usr/bin:/bin" AIQUOTA_TEST_DIR="$work_dir" AIQUOTA_TEST_CASE="$test_case" AIQUOTA_FAKE="$fake" XDG_CONFIG_HOME="$work_dir/config" QT_QPA_PLATFORM=xcb timeout 20s xvfb-run -a "$work_dir/driver.sh"; then
        cat "$work_dir/run.log" >&2
        exit 1
    fi
    if [ -f "$work_dir/commands" ] && grep -vx 'status --cached --json' "$work_dir/commands"; then
        echo 'FAIL unexpected UI command' >&2
        exit 1
    fi
    printf 'PASS %s\n' "$test_case"
done
echo 'PASS UI issued cached status commands only'
