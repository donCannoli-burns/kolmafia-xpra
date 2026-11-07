# KoLmafia Workspace dashboard

The root of the combined web port (`/`) redirects to `/dashboard/`. The normal KoLmafia relay is **still** at `/game.php` and all of its original root-absolute routes. Xpra remains at `/xpra/`. Use `/dashboard/` to open or keep multiple views inside the Discord-style shell; nothing here changes the native KoLmafia window or extracts Swing controls as individual web tabs.

### Components

- `dashboard/index.html`, `app.css`, `app.js`: dependency-free dark workspace. The left rail switches among game, data, and native UI trees. The central workspace holds closable live iframe tabs; a relay, native desktop, and file previews can coexist. For Xpra client/embed issues, each native view provides **Open separately**.
- `dashboard/api.py`: loopback-only Python HTTP service. `GET /_dashboard/api/tree?path=...` lists selected folders, `GET /_dashboard/api/file?path=...` previews recognized text files, and `/health` checks availability. No write endpoints, no command execution. The mount's folders `scripts/`, `data/`, `relay/`, `ccs/`, and `images/` are exposed if present. `settings/`, `sessions/`, hidden entries, symlinks, and sensitive-looking file names are blocked. File previews are limited to 512 KiB.
- **Who's online** fetches `/submitnewchat.php?j=1&graf=%2Ffriends` via the existing KoLmafia relay. No general command box or public chat post is implemented. It runs once when the dashboard opens and when Refresh is clicked, not continuously. The UI parses player-link names or shows the raw response as inert text; if the user isn't logged in or the chat response differs, it reports unavailable rather than inventing people.

### Build and run

Copy the `dashboard/` directory and the updated `Caddyfile`, `Dockerfile`, and `entrypoint.sh` into the checkout, and run:

```sh
python3 -m unittest discover -s tests -v
docker compose up -d --build
```

On the published repo, keep the project's existing host authentication/reverse-proxy arrangements. **Do not expose the dashboard directly to the public internet without access controls:** reading scripts and relay data is sensitive even though write access is disabled. The Python API listens on `127.0.0.1:18081` inside the container and is reachable only through Caddy's same-origin proxy path.

There is no direct browser API for the individual native Swing tabs. The native desktop tab shows those same tabs as part of the Xpra stream; browser-native relay views are independent workspace tabs.

### Boundaries and verification

The UI does not auto-start new KoLmafia sessions or send chat messages; `/friends` is a silent contact query, but KoLmafia may initialize chat via `/listen` if necessary. All tests use a temporary directory; **no live game command is executed by the test suite**. To validate in a running environment, first check `/dashboard/`, `/xpra/`, `/game.php`, data-tree read-only behavior, and `/friends` while logged in. An embedded Xpra view may behave differently depending on the version/browser, hence the separate-view link.
