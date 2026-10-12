# Install and verify the standalone server

Linux x86-64 is the supported platform. Use source builds until the selected release has passed [public availability verification](releases.md). The Dockerfile has shared `runtime` and `debug` targets; its default target is `runtime`. Local process/HTTP fixtures pass, but native CS2, SteamCMD initialization, Metamod/S2Script loading, panel import and HUD acceptance require the checklist in [testing.md](testing.md). Local Docker access was unavailable during implementation.

## Host preparation

Use Linux amd64 with a working Docker/SteamRT environment and persistent storage
writable by container UID 1000. Measure the current CS2 installation size before
setting panel disk limits: allow space for downloads, updates and rollback copies,
plus plugin data, workshop items and backups. Shared generations coexist during
updates; the shared store defaults to a 64 GiB free-space reserve. Backup storage
has separate quotas and reserves. A small panel disk limit can block a safe update.

Allocate CPU/RAM for the intended player count and plugins, then load-test that
configuration; fixtures do not establish a production capacity guarantee. Give
first installation enough time to download app 730 before applying operational
startup timeouts.

Allow the assigned game port for UDP query/client traffic and required TCP
traffic. Use a reachable public allocation/NAT mapping. Keep optional debug TCP
and host administrator sockets private. Permit outbound DNS/HTTPS and the Steam
services required for download/authentication. Obtain a GSLT from Valve's
[game server account page](https://steamcommunity.com/dev/managegameservers);
supply it privately as STEAM_ACC. Do not reuse example credentials.

## Build from source

Build on a Docker host with sufficient disk for CS2 and its persistent updates:

```sh
docker build --platform linux/amd64 --target runtime -f docker/Dockerfile -t s2script-cs2-egg:dev .
docker build --platform linux/amd64 --target debug -f docker/Dockerfile -t s2script-cs2-egg:debug .
docker run --rm --entrypoint /bin/sh s2script-cs2-egg:dev -ec 'test "$(id -u)" -ne 0; test -s /opt/s2egg/steamcmd/steamcmd.sh; ALLOW_TOKENLESS=1 s2egg config check'
```

Both targets build from repository source. Neither depends on a previously published or locally loaded runtime tag. The bases are architecture-specific immutable manifest digests: Go 1.27.2 Alpine and Valve SteamRT3 sniper. SteamCMD's official bootstrap archive is checked against the observed SHA-256 recorded in [runtime.md](runtime.md). A changed upstream archive fails the build and requires a reviewed pin update. This is integrity pinning, not a publisher signature. SteamCMD later updates its persistent copy. The debug target redirects the inherited retired Debian Bullseye security/security-debug endpoints to their official HTTPS archives, retaining suites, keyrings and APT signature/hash checks. These archives contain the final Debian package state; this is an availability correction, not a claim of continuing Debian LTS support. Other debug distribution package versions follow the base's configured repositories; the complete debug image is not claimed byte-for-byte reproducible.

The image runs as `container`, UID 1000, with `/home/container` as its writable HOME and server root. Its immutable seed is `/opt/s2egg/steamcmd`. Runtime safely copies missing files into persistent `steamcmd`, initializes SDK libraries and installs app **730** on first start. The seed supplies the 32-bit bootstrap; SteamRT supplies its loader/libraries and the amd64 game environment. Framework and plugin preparation completes before `game/cs2.sh` starts. Every installed plugin is checked, including disabled entries; inherited bundled enablement remains the default.

For direct Docker, create a persistent named volume and a private environment file. Populate `STEAM_ACC` with your GSLT locally; keep the optional webhook URL blank unless configuring delivery. Do not commit either secret. `ALLOW_TOKENLESS=1` is available for an explicitly chosen tokenless test, not the default.

```sh
umask 077
cat > server.env <<'ENV'
STEAM_ACC=
DISCORD_WEBHOOK_URL=
SERVER_IP=0.0.0.0
SERVER_PORT=27015
ENV
docker volume create s2script-server
docker run -d -i --name s2script-cs2 --stop-timeout 30 \
  --env-file server.env -p 27015:27015/udp -p 27015:27015/tcp \
  -v s2script-server:/home/container s2script-cs2-egg:dev
docker logs -f s2script-cs2
docker exec s2script-cs2 s2egg status --root /home/container
```

Fill the private environment file before starting. A fresh named volume inherits the image directory's ownership; a bind mount must already be writable by the container UID. The supervisor prints `S2EGG_READY` only after a successful local CS2 A2S query. A running process or banner does not prove readiness or native plugin loading. Use client gameplay plus `meta list` and S2Script/plugin evidence for acceptance. `doctor` inspects prerequisites without executing the game or contacting a webhook.

Use `docker stop --time 30 s2script-cs2` for direct Docker shutdown. Budget: game stop up to 10 seconds, raw-event final drain up to 2 seconds, then webhook flush up to 2 seconds, plus process/reader cleanup. Kernel-uninterruptible tasks remain outside userspace deadlines. The 30-second grace leaves headroom; Docker's shorter default is unsuitable. The inspected Wings revisions send configured `quit` when attached and pass a ten-minute caller timeout to `WaitForStop`; their Docker-stop fallback uses timeout -1. A stale source comment about 30 seconds is not that effective budget. Actual panel stop behavior still needs acceptance.

## Pterodactyl or Pelican

1. Build the image on each assigned node or arrange your own registry distribution; do not configure the local tag on a node where it does not exist.
2. Regenerate the import file for that image: `make egg IMAGE=your-image:tag`. Import [the generated PTDL_v2 egg](../pterodactyl/s2script-cs2-egg.json) in the panel's egg import UI. Menu names vary by panel/version.
3. Assign the reachable game allocation. `SERVER_IP` and `SERVER_PORT` are panel-reserved allocation values and intentionally absent from editable egg variables. Select the locally built runtime image, provide GSLT and optional private webhook URL, and retain bundled plugin `default` controls unless overriding them.
4. The installer records that first startup installs into the persistent root. It neither downloads a game in the installer nor reports the game ready. Start with adequate first-install time/disk/network allowance; preparation can take much longer than a warm game launch.
5. Verify the readiness marker, `s2egg status`, client connection, native frameworks and plugin behavior. Panel stop is `quit`. Confirm there is one down event per ready run.

The image CMD directly runs `/usr/local/bin/s2egg run --root /home/container`; no `STARTUP` shell evaluation is needed. No Docker socket or Wings administrator credential is mounted into the game container. Panel import/start/stop remains unverified locally.

## Opt-in remote debugging

Build/select the `debug` target, then set `GDB_DEBUG_PORT` to an allocated TCP port distinct from `SERVER_PORT` (for example 2345). Default **0** disables it. The runtime removes inherited `GAME_DEBUGGER` text and only constructs `gdbserver --no-disable-randomization :2345` from the validated integer. An enabled port without `gdbserver` fails with an actionable startup error; use the debug image. The normal image is intended for normal operations.

Allocate and expose that TCP port only to administrators. For direct Docker, bind it to loopback (`-p 127.0.0.1:2345:2345/tcp`) and reach it through an authenticated SSH tunnel; for a panel, arrange a restricted allocation rather than a publicly reachable debugger. GDB remote access gives control of the game process. The debug container may need an explicitly reviewed ptrace capability/seccomp allowance appropriate to its host; keep that exception scoped to the debug container.

The Valve wrapper waits for a debugger before starting the game. Attach with `target remote HOST:PORT` and continue within the readiness budget. While waiting, no A2S readiness means no up notification or ready marker; expiration is a startup failure. The ordinary default readiness timeout is 120 seconds and is configurable in private `egg/server.json` as `readiness.timeout_seconds` (1–3600). Debugging does not automatically relax readiness. No debugger/game listener was executed during local tests; fixtures verify environment construction and no false readiness.

## Verify and roll back

Record the source commit, image ID (`docker image inspect`), generated egg, private configuration and persistent-volume backup before upgrading. Stop the server before backing up or restoring its data. Keep previous image tags/IDs and the matching source checkout; source is needed to rebuild unpublished images.

An image-only rollback replaces the container with the previous image while reusing the persistent volume. It does **not** roll back downloaded CS2/framework/plugin state. To return that state too, restore a stopped, consistent backup and its plugin inventory/lock/configuration, then use the previous image. Restore into a separate test volume first, run configuration/doctor checks, and complete native acceptance before switching production. Avoid startup `latest` updates while validating a retained generation: use exact verified framework/plugin pins and the documented update policy. `SRCDS_STOP_UPDATE=1` disables app updates but cannot eliminate bootstrap initialization for an incomplete installation.

Preserve the whole root: game/Steam manifests, `steamcmd`, `.steam`, `egg` identity/config/cache/state and installed-plugin lists/locks/config/data. Deleting the volume discards those. The optional node supports [consistent backups and stopped restores](backups.md), retained shared generations and [managed node rollback](project-updates.md). See [webhook ownership and delivery limits](webhooks.md).
