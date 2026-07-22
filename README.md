# systray2 — consolidated (kept as backup)

**The Windows compute node has moved to
[`agent-grid-node/platforms/windows/`](https://github.com/ReEnvisionAi/agent-grid-node/tree/main/platforms/windows),**
which is now the single home for node code (macOS / Linux / Windows) and the
active build + release source for the Windows installer.

This repo is **kept as a backup / reference**, not archived. To avoid two repos
releasing the same installer, its `windows-installer.yml` workflow is
**manual-dispatch-only** here — cut real Windows releases by tagging
`agent-grid-node`.

- **Active development:** `agent-grid-node/platforms/windows/` (vendored from here
  with full history, including the updater fail-closed security fix).
- **Contract it implements:** [grid-node-contract](https://github.com/ReEnvisionAi/Grid-node-contract).

> The security fix (auto-updater fails closed on an unverifiable update) is on
> branch `claude/agent-grid-2-review-fzo8vs` here and already vendored into
> `agent-grid-node`. New work should go to `agent-grid-node`, not this repo.
