# AGENTS.md — Fboard-Node

Guidance for AI coding agents working in this repository.

## Layout

- `cmd/fboard-node` — daemon entrypoint (machine mode only)
- `cmd/fbctl` — install/config/service CLI
- `internal/machine/` — machine orchestrator (discovers nodes, shared WS)
- `internal/service/` — per-node runtime (kernel lifecycle, traffic, certs)
- `internal/controlplane/` — machine control plane + mailbox
- `internal/panel/` — panel REST + WebSocket client (V2 machine auth)
- `internal/kernel/` — kernel interface + `xray/` adapter (only kernel)
- `internal/config/` — YAML config (machine-only panel connection)
- `internal/model/` — node/user specs and validation
- `internal/cert/`, `limiter/`, `tracker/`, `monitor/`, `nlog/`

## Kernel

Only **xray-core** is embedded (via `go.mod` replace to Fearless743/Xray-core).
There is no kernel type selector, factory, or multi-kernel support matrix.

## Deployment

Panel connection is **machine mode only**:

```yaml
panel:
  url: "https://panel.example.com"
machine:
  machine_id: 1
  token: "..."
```

Static `panel.node_id` / `nodes:` / standalone configs are rejected.
Per-node runtime still uses `node_id` after discovery via `/api/v2/server/machine/nodes`.

## Commands

```bash
make build
make test
./fboard-node -c config.yml
./fbctl config init --panel-url URL --token TOKEN --machine-id 1
```
