# kolmafia-xpra

KoLmafia running under [xpra](https://xpra.org), usable from a browser or the native Xpra client.

```sh
cp .env.example .env
docker compose up -d --build
```

Or skip the build and pull `ghcr.io/gausie/kolmafia-xpra:latest` (amd64 and arm64, rebuilt on every push to `main`). In Portainer, create a stack from `compose.yaml` with the `build:` block removed, and set the variables from `.env.example` in the stack's environment.

- `8080`: everything on one port. The KoLmafia relay is at `/` and the xpra HTML5 client at `/xpra/`.
- `14500`: xpra directly, for native clients on a trusted network (`xpra attach tcp://host:14500`).

Both bind to `BIND_ADDRESS` (default `127.0.0.1`). Put `8080` behind your reverse proxy and auth on a single hostname. Native clients can also go through it with `xpra attach wss://kol.example.com/xpra/`.

KoLmafia's data directory (settings, scripts, relay, images) is bind-mounted from `KOLMAFIA_DIR`, so an existing one can be used as-is. Downloaded KoLmafia jars go in the separate `jars` volume. Set `PUID`/`PGID` to the owner of the data directory.

## Local Linux desktop: Go play plane (opt-in)

This fork adds a single-instance desktop launcher and a **localhost-only Go reverse proxy** for a customizable play window. It does not remove the original Caddy gateway. The Go proxy serves editable HTML/CSS/JS from `playplane/web/` at `/play/`, and transparently forwards other paths, including Xpra WebSockets and KoL relay requests, to Caddy. It does not cache or retry gameplay requests.

One-time setup from the repository directory:

```sh
# Keep your existing .env; create one only if missing.
test -f .env || cp .env.example .env
# Build the container once so the KoLmafia clean-exit fix is included.
docker compose build kolmafia
# Installs ~/Desktop/KoLmafia Play.desktop and an applications-menu entry.
bash contrib/kol-playctl install
```

Double-click **KoLmafia Play**, or run `bash contrib/kol-playctl start`. Requirements: Docker Compose, Go 1.22+, `flock`, `curl`, and one locally installed browser: GNOME Web/Epiphany (preferred when available), Chromium/Chrome/Brave/Edge, or Firefox/Firefox ESR. The browser uses a dedicated profile at `~/.local/share/kolmafia-play/browser-profile`, without affecting your main browser profile. Set `KOL_PLAY_BROWSER=/absolute/path` to select an executable, optionally with `KOL_PLAY_BROWSER_MODE=epiphany|firefox|chromium` when the executable name cannot be inferred. Firefox opens an independent window but may retain its browser toolbar; Epiphany and Chromium use application mode. Browser executables installed only through Flatpak or Snap may require additional wrapper/portal handling and are not automatically detected.

- The launcher acquires an exclusive lock and will **not** start a second copy. If `wmctrl` is installed, it also attempts to focus the existing window.
- Closing the app-mode browser window stops the owned Go proxy and calls `docker compose stop -t 20 kolmafia`. Data volumes are preserved. For an intentionally persistent Docker session set `KOL_PLAY_STOP_ON_CLOSE=0`.
- `bash contrib/kol-playctl stop` requests a clean shutdown of the owned session; `bash contrib/kol-playctl status` shows status.
- Browser UI defaults to `http://127.0.0.1:18080/play/`; container Caddy remains `http://127.0.0.1:8080`. `KOL_PLAY_PORT` and `WEB_PORT` can change these local ports. Desktop mode requires `BIND_ADDRESS=127.0.0.1`.
- Edit `playplane/web/index.html`, `style.css`, or `app.js` while running and refresh to see changes. Desktop, Relay, and Split views are provided. If a relay or Xpra page forbids iframe embedding, use the provided full-page links.
- `KOLMAFIA_RESTART_POLICY=no` is the local default. Always-on server owners can explicitly set `KOLMAFIA_RESTART_POLICY=unless-stopped`, but that is intentionally **not** the desktop behavior.
- Explicit KoLmafia version changes still restart Java. Closing KoLmafia normally does not. No login or game-state automation has been added.

`go-webui` is a browser/WebView **controller**, not a small independent web rendering engine; the dedicated Chromium app window here gives a predictable close event without introducing a new GUI dependency. The Go reverse proxy uses only the standard library rather than adding another caching/load-balancing proxy to a stateful game relay.

### Development checks

```sh
go test ./...
go vet ./...
bash -n contrib/kol-playctl
```

**Caution:** The launcher owns the local `kolmafia` Compose service and stops it on close, even if it was already running before launch. Do not use this desktop mode against a shared/remote KoLmafia service. Validate shutdown with your actual Docker/Xpra browser stack before relying on it for a live session.

## Versions

The KoLmafia version is chosen from inside the session: the "KoLmafia version" button in the browser, Start → KoLmafia → KoLmafia Version in Linux/Windows Xpra clients, or Server → Run Command → `kolmafia-version` on macOS. It defaults to tracking the latest release, and offers to update when a new one comes out.

## Options

- `XPRA_CHANNEL=beta` uses the xpra beta repo, both when building and for the published image tag (amd64 only)
- `KOLMAFIA_UI_SCALE=2` renders KoLmafia and its dialogs at 2x for sharp text on HiDPI screens. The HTML5 client scales it back down by itself; native clients need `--desktop-scaling=0.5`
- `KOLMAFIA_PUBLIC_URL=https://kolmafia.example.com` is where the relay browser opens when KoLmafia asks for it. Without it the HTML5 client opens it on whatever address you reached xpra on, but the native client can't

## Kmail over IMAP

Set `KMAIL_PASSWORD` to read and send kmail from a mail client. It goes through the relay, so it only syncs while KoLmafia is logged in, and only for whoever that is. Mail already synced stays readable when it isn't.

- IMAP on `993` (`IMAP_PORT`) and SMTP on `465` (`SMTP_PORT`), both TLS only. Log in with your character name and `KMAIL_PASSWORD`
- Players are `playerid@kingdomofloathing.internal`, and KoL's own characters like the Postal Service are `noreply@`. Replies work as normal; outgoing mail to anything else is refused. Your subject leads the kmail unless it's a reply, and quoted text and signatures are dropped
- Deleting from INBOX deletes the kmail in KoL. Sent, Drafts, Trash and Archive only exist in the bridge
- INBOX polls every minute and holds the newest 100 kmails, which is as many as KoL's API returns
- Items and Meat you're sent show up as attachments with their icons. They can't be sent this way, and mail with attachments is refused so forwarding a kmail can't look like it passed them on
- Without `KMAIL_TLS_CERT`/`KMAIL_TLS_KEY` (paths inside the container), a self-signed certificate for `KMAIL_HOSTNAME` is made in `kmail-bridge/` in the data directory. Your mail client will ask you to trust it once

## Native client behind Authelia

The native client can't follow Authelia's login redirect. `contrib/xpra-attach-authelia` logs in via Authelia's API and passes the session cookie to `xpra attach`:

```sh
contrib/xpra-attach-authelia wss://kol.example.com/xpra/
```

It finds the Authelia portal from the redirect it gets for that URL (set `AUTHELIA_URL` to override), asks for your login in a dialog, and with "Remember me" keeps it in the macOS keychain or Secret Service. Saved logins that stop working are forgotten and you're asked again. On macOS it uses `/Applications/Xpra.app` when `xpra` isn't on your `PATH`. Needs `curl` and `jq`.

`contrib/make-macos-app wss://kol.example.com/xpra/` downloads Xpra from xpra.org and builds `~/Applications/KoLmafia.app`, a renamed copy that runs the above, so it shows up as KoLmafia in Spotlight, the dock and the app switcher. Set `XPRA_CHANNEL=beta` to match a beta server, and add `--desktop-scaling=0.5` after the URL if the server runs `KOLMAFIA_UI_SCALE=2`. `--open-url=yes` opens the relay browser without the client asking first each time.
