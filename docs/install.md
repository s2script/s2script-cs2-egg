# Install and verify the standalone server

Linux x86-64 is the supported platform. For a panel installation, use the
[egg URL and published images](#pterodactyl-or-pelican). No Go toolchain or local
image build is needed once the selected release has passed
[public availability verification](releases.md). Use
[source builds](#build-from-source) for development.
Native CS2, SteamCMD initialization, Metamod/S2Script loading, panel import and
HUD acceptance require the checklist in [testing.md](testing.md).

**Sharing game files across servers?** Shared installs are optional and require
host setup. Follow the [node setup walkthrough](node-setup.md), including when
switching from a standalone download that has already started. Otherwise, this
page installs a complete standalone server with no node daemon.

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

This is the development/custom-image path. Panel users can skip to
[Pterodactyl or Pelican](#pterodactyl-or-pelican).

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
S2EGG_BIND_IP=0.0.0.0
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

1. Open the panel administration area, then **Eggs → Import → From URL** or
   **Nests → Import Egg** (menu names depend on the panel/version). Paste:

   ```text
   https://raw.githubusercontent.com/s2script/s2script-cs2-egg/main/pterodactyl/s2script-cs2-egg.json
   ```

   This imports **S2Script CS2 (standalone)**. If your panel only supports file
   uploads, download this JSON and upload it. The egg's `meta.update_url` uses
   the same address for panels that support updating an egg from its URL.
2. Create a server using the imported egg and select **S2Script prerelease**
   during the alpha period. Wings pulls the selected image from GHCR:

   | Panel choice | Image | Availability |
   | --- | --- | --- |
   | S2Script prerelease (default) | `ghcr.io/s2script/s2script-cs2-egg:prerelease` | After a successful release, including alphas |
   | S2Script stable | `ghcr.io/s2script/s2script-cs2-egg:stable` | After the first successful stable release |

   Confirm the selected release's **public-verification** job succeeded.
   An imported egg alone does not publish its images. If Wings reports
   `manifest unknown`, check that the channel has been published; access errors
   require checking [GHCR visibility](releases.md#publish).
3. Assign the reachable game allocation. `SERVER_IP` and `SERVER_PORT` come
   from the panel allocation and are absent from editable egg variables.
   CS2 binds to `0.0.0.0` inside the container using the allocated `SERVER_PORT`;
   the host's `SERVER_IP` is not a container bind address.
   Provide your GSLT as `STEAM_ACC`, optionally set a private webhook URL, and
   retain bundled plugin `default` controls unless overriding them.
4. The panel installer uses `ghcr.io/pterodactyl/installers:debian` and finishes
   without downloading CS2. **Start the server** to install CS2, Metamod and
   S2Script into the persistent root. Allow adequate first-install time, disk
   and network access; preparation takes longer than a warm game launch.
   The console shows the launcher version, preparation stages and SteamCMD
   progress. A message-only installer is expected; see the
   [startup troubleshooting and CS2-Egg-Go comparison](troubleshooting.md#panel-startup-and-cs2-egg-go).
5. Verify `S2EGG_READY`, `s2egg status`, client connection, native frameworks
   and plugin behavior. Panel stop is `quit`. Confirm there is one down event
   per ready run.

For an exact version, copy the `egg-s2script-cs2.json` asset link from a verified
[release](https://github.com/s2script/s2script-cs2-egg/releases) into the same
import UI. The link has the form
`https://github.com/s2script/s2script-cs2-egg/releases/download/vVERSION/egg-s2script-cs2.json`.
That egg references only the matching immutable `:vVERSION` image and has no
moving update URL. GitHub's `releases/latest` download URL excludes prereleases,
so use a specific release asset link for alphas.

For a custom/local build, make the image available on each assigned node and
generate a separate import file with Go 1.27.2:

```sh
go run ./cmd/egggen --image your-image:tag --output /tmp/s2script-custom-egg.json
```

Upload that file instead. `make egg` regenerates the checked-in channel egg;
`make egg IMAGE=your-image:tag` deliberately replaces it with a custom-image egg.

The image CMD directly runs `/usr/local/bin/s2egg run --root /home/container`; no `STARTUP` shell evaluation is needed. No Docker socket or Wings administrator credential is mounted into the game container. Panel import/start/stop remains unverified locally.

## Opt-in remote debugging

Build/select the `debug` target, then set `GDB_DEBUG_PORT` to an allocated TCP port distinct from `SERVER_PORT` (for example 2345). Default **0** disables it. The runtime removes inherited `GAME_DEBUGGER` text and only constructs `gdbserver --no-disable-randomization :2345` from the validated integer. An enabled port without `gdbserver` fails with an actionable startup error; use the debug image. The normal image is intended for normal operations.

Allocate and expose that TCP port only to administrators. For direct Docker, bind it to loopback (`-p 127.0.0.1:2345:2345/tcp`) and reach it through an authenticated SSH tunnel; for a panel, arrange a restricted allocation rather than a publicly reachable debugger. GDB remote access gives control of the game process. The debug container may need an explicitly reviewed ptrace capability/seccomp allowance appropriate to its host; keep that exception scoped to the debug container.

The Valve wrapper waits for a debugger before starting the game. Attach with `target remote HOST:PORT` and continue within the readiness budget. While waiting, no A2S readiness means no up notification or ready marker; expiration is a startup failure. The ordinary default readiness timeout is 120 seconds and is configurable in private `egg/server.json` as `readiness.timeout_seconds` (1–3600). Debugging does not automatically relax readiness. No debugger/game listener was executed during local tests; fixtures verify environment construction and no false readiness.

## Verify and roll back

Record the source commit, image ID (`docker image inspect`), generated egg, private configuration and persistent-volume backup before upgrading. Stop the server before backing up or restoring its data. Keep previous image tags/IDs and the matching source checkout; source is needed to rebuild unpublished images.

An image-only rollback replaces the container with the previous image while reusing the persistent volume. It does **not** roll back downloaded CS2/framework/plugin state. To return that state too, restore a stopped, consistent backup and its plugin inventory/lock/configuration, then use the previous image. Restore into a separate test volume first, run configuration/doctor checks, and complete native acceptance before switching production. Avoid startup `latest` updates while validating a retained generation: use exact verified framework/plugin pins and the documented update policy. `SRCDS_STOP_UPDATE=1` disables app updates but cannot eliminate bootstrap initialization for an incomplete installation.

Preserve the whole root: game/Steam manifests, `steamcmd`, `.steam`, `egg` identity/config/cache/state and installed-plugin lists/locks/config/data. Deleting the volume discards those. The optional node supports [consistent backups and stopped restores](backups.md), retained shared generations and [managed node rollback](project-updates.md). See [webhook ownership and delivery limits](webhooks.md).
