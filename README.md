# AIQuota

AIQuota reads Claude Code, Codex, and Antigravity subscription quotas on Linux. It does not report API billing or general ChatGPT message limits.

## Build

Use Go 1.23 or newer:

```sh
mkdir -p ~/.local/bin
go build -trimpath -o ~/.local/bin/aiquota .
```

Run `aiquota` in a terminal for the dashboard. Use arrow keys or `j`/`k` to choose a provider, Enter for details, `r` to refresh, `R` to refresh all, `?` for help, and `q` to quit. Without a terminal, it prints a text status. Run `aiquota help` for commands.

## Data collection

- Claude Code sends quota data through its status line. AIQuota does not read Claude credentials or send model requests.
- Codex uses its local app-server and existing login.
- Antigravity reads `agy -p /usage --output-format json`.

Claude data updates when its status line runs. The daemon refreshes Codex and Antigravity. Start it with `aiquota daemon`; autostart is optional.

Enable or disable a provider with `aiquota provider enable ID` or `aiquota provider disable ID`. Disabled providers do not collect data, and their cached data remains on disk. Invalid configuration stops collection. A running daemon checks for configuration changes every five seconds.

## Caelestia addon

Run `python3 install-addon.py /path/to/caelestia` to build AIQuota, install the addon, and deploy it. Add `--no-deploy` to skip deployment. The installer requires Go 1.23+, Git, and Python 3.

The reusable Quickshell component and installer are in `quickshell/caelestia/`. The lower-level installer, `python3 quickshell/caelestia/install.py /path/to/caelestia`, changes the source checkout but does not deploy it. Review local changes first. If installation fails, check the reported recovery directory before retrying.

## Local files

Optional configuration: `$XDG_CONFIG_HOME/aiquota/config.json`. Cached quota data: `$XDG_STATE_HOME/aiquota`. AIQuota stores quota metadata, not prompts or credentials.
