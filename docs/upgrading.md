# Upgrading

Two things update on their own schedules: the image your servers run, and the daemon on the node.

## v2.0.0: SteamRT4

v2 replaces SteamRT3 with SteamRT4 for every server. Stable and beta are still release channels; both use RT4 in v2. The host node daemon and shared CS2 files retain their existing setup. Host SteamCMD remains separate from the container runtime.

Before upgrading, back up the server volume and record native addon version pins. Keep Metamod compatible with the selected addon versions.

Select `ghcr.io/s2script/s2script-cs2-egg:v2.0.0`, or `:stable` after v2.0.0 is published. Fully stop and start the server so Wings recreates its container. The egg reinstalls selected native addons when their existing receipts lack RT4 identity, including at an unchanged release version. Private configs and data are preserved. This first migration needs access to the selected release metadata and archives, directly or through the node cache; an outage cannot validate old RT3 receipts.

If an installed addon is newer than its selected channel and lacks verified RT4 evidence, startup asks you to pin that exact installed version for repair. This avoids downgrading it implicitly. The chosen version must still supply a compatible asset.

Affected S2Script shims need executable-stack loader compatibility on RT4. The egg enables it only in the game launch environment when the installed ELF requests it, preserving other glibc tunables and publisher files. It permits an executable game stack and logs that choice. An explicit conflicting `glibc.rtld.execstack` setting stops startup; update the shim or explicitly allow the documented compatibility value `2`. A future shim that no longer requests an executable stack avoids the setting automatically.

For rollback, fully stop the server, select the previous versioned RT3 image, and restore the pre-upgrade native addon files/receipts from backup or reinstall matching RT3 builds before restarting. Changing the image alone does not establish that the installed native binaries match it. New receipt format 2 prevents the old egg from treating RT4 evidence as a verified RT3 installation, but does not replace a backup or handle every older updater's retention rule.

## The image

Wings pulls the image when a server starts. So a server picks up a new image on its next full stop and start. The Restart button does not always replace the container; stop and start does.

| Tag | Gets |
|---|---|
| `:stable` | Stable releases |
| `:beta` | What is going into the next stable |

The egg defaults to `:stable`, so future stable releases require no egg-export changes. Select `:beta` to follow published releases including prereleases. Version tags such as `:v1.2.3` remain available if you deliberately want to hold one image. Keep the node/egg generations compatible. Framework/native version [pins](modules/addon-cache.md#version-locks) are separate from image tags.

To force a pull on the node:

```bash
sudo docker pull ghcr.io/s2script/s2script-cs2-egg:stable
```

Then stop and start the server.

## The daemon

```bash
sudo cs2node update      # check now
```

Otherwise it checks once a day and installs what is newer on its channel:

| Channel | Installs |
|---|---|
| `stable` | Stable releases |
| `beta` | Prereleases too |
| `dev` | Nothing. You copy the binary yourself |

A release is the binary plus `checksums.txt`. The daemon downloads both **from GitHub only**, over TLS, with no mirror in between, verifies the SHA-256, swaps the binary atomically and restarts itself through systemd. The previous binary is kept.

A release asset that points anywhere but `github.com` or its own asset hosts is refused before anything is fetched, and a redirect that would leave those hosts is refused too. Nothing inside a release can change that: the checksum is the whole policy, and there are no flags in the payload to honour.

Turn the automatic check off with `"update": {"auto": false}` in the config, or by choosing the `dev` channel.

### Rolling back

```bash
sudo mv /usr/local/bin/cs2node.prev /usr/local/bin/cs2node
sudo systemctl restart cs2node
```

Blocks survive it: they live in the kernel, not in the process.

## Config across versions

`/etc/cs2node/config.json` carries a `version`. A newer daemon migrates it in memory on load, so an old file keeps working and you never rewrite it by hand. New settings get their defaults; yours are kept.

The server-side configs under `egg/configs/` work the same way, per server, on boot. Rules you edited stay, new template rules are appended, removed ones disappear.

## Checking what is running

```bash
cs2node version           # the binary you just ran
cs2node status            # the daemon's own version, and each server's egg build
cs2node doctor            # says so loudly if they disagree
```

`doctor` compares the running daemon against the binary on disk, and each server's egg build stamp against what it should be. That is how you catch a server that never picked up the new image.

---

[Docs index](README.md)
