# Downstream only: KoLmafia Xpra + WaterSheep host launcher

> **Status: NON-CANONICAL / DOWNSTREAM ONLY.** This is an optional local
> integration carried on the `downstream/kolstack-watersheep` branch. It
> is **not** a dependency of KoLmafia, the upstream Xpra image, the main
> branch, or a proposal to merge into the canonical runtime.

## What runs where

- KoLmafia and Xpra: the existing Docker Compose `kolmafia` service.
- WaterSheep: a **host** process, default executable
  `~/anaconda3/bin/watersheep`, with model
  `samratduttaofficial/WaterSheep`, CPU backend, port `8766`.
- Launch/control: `./kolstack.sh start|stop|restart|status|logs` from
  the repository root; its paths resolve relative to the checked-out repo.
- WaterSheep PID and logs: ignored `./watersheep/` directory. Model
  weights remain in the existing host cache; they are not copied here.
- Compose override: `restart: "no"` for the `kolmafia` service to
  prevent a Docker automatic restart after an intentional stop.

This **does not** connect the KoLmafia container to the WaterSheep HTTP
API. A process bound to `127.0.0.1` on the host is not reachable as
`127.0.0.1` from within Docker. Configure an appropriate restricted
host/container route separately before calling WaterSheep from KoLmafia
scripts.

## Install into the existing local checkout

The intended location is
`~/repos/es-3581100/kolmafia-xpra`. The branch is hosted in
`donCannoli-burns/kolmafia-xpra`; do not treat it as upstream.

First inspect `git status --short`. Preserve any local changes. If a
local *untracked* `compose.override.yaml` already exists, back it up
and reconcile its contents before switching branches. Do not blindly
overwrite an existing override or `.env`.

```bash
cd "$HOME/repos/es-3581100/kolmafia-xpra"
git fetch origin
git switch downstream/kolstack-watersheep \
  || git switch --track origin/downstream/kolstack-watersheep
chmod +x ./kolstack.sh
docker compose -f compose.yaml -f compose.override.yaml config --quiet
./kolstack.sh status
./kolstack.sh start
```

If `origin` points elsewhere, fetch the `donCannoli-burns` remote
explicitly instead. `kolstack.sh` needs `docker`, `flock` (util-linux),
`ss` (iproute2), Bash, and the existing host WaterSheep executable. A
missing `.env` is initialized from `.env.example`; review its
`KOLMAFIA_DIR` setting before first start if you use other data.

## Commands and behavior

```bash
./kolstack.sh start
./kolstack.sh status
./kolstack.sh logs
./kolstack.sh stop
```

- Repeated `start` requests reuse an already-managed WaterSheep process.
  If port `8766` is held by another process, the launcher refuses to
  spawn a second WaterSheep instance.
- `stop` sends SIGTERM only to a verified launcher-managed WaterSheep
  process, then brings down this Compose stack. It never forcibly kills
  an unknown process or an existing WaterSheep on port `8767`.
- A WaterSheep startup failure may leave KoLmafia running; check
  `./kolstack.sh status`, then stop explicitly as needed.
- Closing a browser tab **does not** stop either service.
- Logs and run-state stay local, ignored by Git.
- Port `8080` is the default Xpra/reverse-proxy port unless overridden
  in `.env`. See the canonical README for the relay/Xpra URL paths.
- These commands control the services for this checkout; no upstream
  repository, model weights, or KoLmafia scripts are modified.

Optional host settings:

```bash
WATERSHEEP_BIN="$HOME/anaconda3/bin/watersheep" \
WATERSHEEP_MODEL="samratduttaofficial/WaterSheep" \
WATERSHEEP_PORT=8766 ./kolstack.sh start
```

## Governance

This branch is maintained as a personal downstream experiment. Keep the
`main` branch and the canonical image/Compose behavior unmodified.
Do not open an automatic PR or merge this branch upstream without a
separate explicit decision. Any future agent or runtime API wiring
belongs in independently gated, reviewed changes.
