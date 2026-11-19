# AIQuota

AIQuota monitors coding-tool subscription quotas on Linux. It collects Claude Code status-line data, Codex subscription rate limits, and Antigravity account quotas. It does not report developer API billing or general ChatGPT message limits.

## Build and run

Use Go 1.23 or newer. From the project directory, build the executable into `~/.local/bin` and add that directory to `PATH`:

```sh
mkdir -p ~/.local/bin
go build -trimpath -o ~/.local/bin/aiquota .
```

Run `aiquota` in a terminal to open the interactive dashboard. Wide terminals show a provider rail beside the selected provider's quota windows; narrow terminals show compact provider cards. Use the arrow keys or `j` and `k` to select a provider, `enter` for details, `r` to refresh the selection, `R` to refresh all providers, `?` for help, and `q` to quit. Use `aiquota tui` to request the dashboard explicitly.

`aiquota status` prints a one-shot text view. Add `--cached` to read local state without refreshing, or add `--json` for the versioned JSON output. Other commands include `refresh`, `watch`, `doctor`, `daemon`, `ingest-claude`, and `version`. Run `aiquota help` for command syntax.

When either standard input or output is redirected, the default command prints text instead of opening the dashboard.

## Providers

- Claude Code sends quota data through its status line. Setup is opt-in. AIQuota never reads Claude credentials or sends a model request.
- Codex reads account rate limits through the supported local app-server protocol and uses the existing Codex login.
- Antigravity reads model-group quotas from the documented `agy -p /usage --output-format json` command.

Claude updates arrive only while the status line runs. The daemon refreshes Codex and Antigravity on independent schedules. Autostart is optional.

Use `aiquota provider disable claude` (or `codex` or `antigravity`) to stop collection, and `aiquota provider enable claude` to resume it. Provider enablement is managed through configuration or the CLI. Disabled providers remain visible without cached quota values; their cached data is retained for later use. Claude's existing status-line command still runs unchanged, but AIQuota does not ingest its data while disabled. Re-enabling Claude requires a new status-line observation for an update.

The toggle waits for in-flight AIQuota collection and diagnostics for that provider to finish. A running daemon polls enablement every five seconds and detects changes without restarting; a disable and re-enable that both occur between polls may not trigger an immediate refresh. This guarantee applies to cooperating AIQuota processes using the same state and config directories; manually editing configuration, older AIQuota versions, or other tools do not participate in the gate. Invalid configuration prevents new collection.

## Caelestia

For an existing Caelestia source checkout, run `python3 install-addon.py` from this repository. It finds a checkout in the current directory or common locations, builds AIQuota into `~/.local/bin`, installs the addon, and runs the checkout's `./apply` command. Pass the checkout path if it lives elsewhere: `python3 install-addon.py /path/to/caelestia`. Set `CAELESTIA_CHECKOUT` to that path for repeat installs. Use `--no-deploy` to install into the checkout without running `./apply`. Go 1.23+, Git, and Python 3 are required. The installer adds an AIQuota page to Caelestia settings with provider status and enable switches. It does not configure Claude, Codex, or Antigravity accounts; run `aiquota doctor` after installation.

The reusable Quickshell component is in `quickshell/caelestia/AIQuota.qml`; the reversible fork patch is beside it. Review the patch against your Caelestia checkout before applying it. To install or update the integration in a compatible checkout, run `python3 quickshell/caelestia/install.py /path/to/caelestia`. This lower-level installer accepts the clean patch base, the current integration, and the earlier plain `AIQuota {}` registration with the current hover changes. It checks all patch sections before writing and refuses unrecognized modifications; back up local changes first. It replaces the component and its bundled icons, but does not deploy or reload the shell. For the managed Caelestia checkout, run its `./apply` afterward; editing a deployed snapshot will not update the source checkout. The component reads cached JSON; it does not collect provider data or change provider settings.

The installer checks for concurrent destination changes before replacement and rollback, but these checks are not atomic with filesystem operations; avoid editing the checkout during installation. If rollback is incomplete, recovery files remain in the reported `.aiquota-install-*` directory. SIGTERM, a crash, or power loss can interrupt installation without rollback; inspect the checkout and staging directory before recovering manually. Backups use Python's `copy2`, which preserves file contents and attempts to preserve mode and timestamps, but does not guarantee ownership, ACLs, extended attributes, or all other metadata. Replacements also change file identity and may not retain all host metadata.

The hover popout does not request keyboard focus until a control is clicked. Caelestia's layer-shell keyboard focus is shared with the rest of the drawer window, so requesting it on hover would divert typing from the currently focused application. Once activated, the popout supports keyboard navigation.

## Local files

Configuration is optional JSON at `$XDG_CONFIG_HOME/aiquota/config.json`. State lives under `$XDG_STATE_HOME/aiquota`. The executable stores quota metadata, not prompts or credentials.
