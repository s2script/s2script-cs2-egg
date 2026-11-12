# S2Script and native addons

| Component | Switch (default `1`) | Exact release selector |
|---|---|---|
| [Metamod](https://github.com/alliedmodders/metamod-source) | `INSTALL_METAMOD` | Node version pin |
| [S2Script](https://github.com/s2script/s2script) | `INSTALL_S2SCRIPT` | `S2SCRIPT_VERSION` |
| [GameFixes](https://github.com/SlynxCZ/GameFixes_mm) | `INSTALL_GAMEFIXES` | `GAMEFIXES_VERSION` |

S2Script and GameFixes automatically require Metamod. No other scripting framework is installed. GameFixes publishes one Linux archive. Downloads are staged and checked before installation, preserving private configs and data. S2Script requires the publisher's SHA-256 digest; native addons verify a digest when supplied.

Native installation receipts include the target runtime. The first v2 boot reinstalls selected native addons with legacy or different-runtime receipts, even at the same release tag. A newer installed addon whose runtime cannot be verified also fails rather than silently downgrading: pin its exact version to repair it. Transient outages retain only installations whose files and RT4 receipts verify. See [upgrading to v2](upgrading.md#v200-steamrt4).

Some S2Script releases, including v0.12.2, mark their shim as requiring an executable stack. Before launching the game, the egg inspects the installed ELF. When required, it adds glibc's documented `glibc.rtld.execstack=2` compatibility setting to the game launch environment and logs a warning. This permits an executable stack in the launch shell and descendants, as the affected plugin requests; publisher binaries and checksums remain intact. Other glibc tunables are preserved. Explicit conflicting execstack settings, or `glibc.rtld.enable_secure=1` which disables other tunables, stop startup with a repair instruction. A shim with a non-executable stack does not trigger this setting. See the [glibc documentation](https://sourceware.org/glibc/manual/latest/html_node/Dynamic-Linking-Tunables.html).

Automatic Metamod updates always select the newest Source 2 prerelease, regardless of `PRERELEASE`. Stable Metamod releases target Source 1 and are excluded from automatic selection.

`S2SCRIPT_CHANNEL=stable` follows stable framework releases; `prerelease` includes prereleases. This is separate from image/node channels. Exact framework tags constrain selection; registry plugin versions belong in native dependencies. Temporary service failures may retain a verified compatible install; missing/corrupt files and incompatible policy still fail visibly.

## Bundled plugins

The 21 `S2SCRIPT_PLUGIN_*` panel switches accept `default`, `1`, or `0`. `default` preserves policy and existing placement. Explicit `1` enables a bundled package; `0` disables it. When a bundled plugin is also a dependency in the native manifest, explicit switches update its `disabled` entry so the runtime keeps that choice. Updates preserve configs/data, private packages, and disabled choices. Duplicate active/disabled identities are errors.

## Moving off MultiAddonManager

The egg no longer installs or updates [MultiAddonManager](https://github.com/Source2ZE/MultiAddonManager). S2Script's bundled `addonmanager` plugin (`S2SCRIPT_PLUGIN_ADDONMANAGER`, enabled by default) mounts workshop addons on the server and sends them to clients itself. The two hook the same engine functions, so a server must not load both.

On the first boot where S2Script is installed and its `addonmanager` plugin is enabled (whether or not `INSTALL_S2SCRIPT` keeps it updated), the egg retires an existing MultiAddonManager install:

1. If `cfg/multiaddonmanager/multiaddonmanager.cfg` lists addons and `addons/s2script/configs/_s2script_addonmanager.json` does not exist yet, the settings are written there under their new names. An existing addonmanager config is never overwritten; the egg logs a warning instead.
2. `addons/metamod/multiaddonmanager.vdf`, `addons/multiaddonmanager/bin/` and the egg's install receipt are removed. MultiAddonManager's cfg directory stays for reference.

Without an enabled `addonmanager`, the egg leaves MultiAddonManager's files alone and logs a warning each boot.

| MultiAddonManager | addonmanager |
|---|---|
| `mm_extra_addons` | `addons_extra` |
| `mm_client_extra_addons` | `addons_client_extra` |
| `mm_extra_addons_timeout` | `addons_extra_timeout` |
| `mm_addon_connection_timeout` | `addons_connection_timeout` |
| `mm_addon_mount_download` | `addons_mount_download` |
| `mm_cache_clients_with_addons` | `addons_cache_clients` |
| `mm_cache_clients_duration` | `addons_cache_duration` |
| `mm_block_disconnect_messages` | `addons_block_disconnect_messages` |

Only the MultiAddonManager cfg file is read. Settings passed as `+mm_*` startup parameters or set in `server.cfg` must be moved by hand, and those `mm_*` lines can then be deleted. The `INSTALL_MULTIADDONMANAGER` and `MULTIADDONMANAGER_VERSION` panel variables and the `multiaddonmanager` node pin are removed; an old node config that still has the pin keeps working and the pin is ignored.

## Custom translations

Edit phrase catalogs under `game/csgo/addons/s2script/translations/`, relative
to the server root. Root catalogs such as `common.phrases.json` customize the
default wording; language catalogs such as `ru/common.phrases.json` customize
that language. Each file is a flat JSON object mapping phrase keys to strings.

During framework updates, the egg adds missing keys from the corresponding
release catalog while keeping all existing values, including empty strings,
custom phrases and keys removed upstream. New catalogs are installed normally.
Language catalogs are merged only with the same language's release file; English
phrases are not inserted into other languages. Custom-only files are left alone.
Catalogs with no new keys keep their existing bytes and modification time.

Merging new keys reformats that catalog as indented JSON and retains its file
permissions. Each catalog is replaced atomically after a successful write, so a
failed copy leaves the original in place. Invalid JSON, non-string phrase values,
or an unsafe matching destination stop the update before installed framework files
are changed; the error identifies the catalog to fix. Keep positional placeholders such as `{1}`
intact when customizing wording. Restart or reload the plugin after editing a
deployed catalog so S2Script reads it again.

## Native plugin registry

S2Script's native server manifest is `game/csgo/addons/s2script/plugins.json`, relative to the server root (`$HOME`, normally `/home/container`). Its companion `plugins-lock.json` belongs to S2Script. The egg preserves both files during framework updates. When a panel switch moves a native-owned bundled package, the egg relocates its lock entry only after verifying identity, version and integrity. Registry-managed bundled packages are updated by S2Script rather than overwritten from framework releases.

```json
{
  "name": "my-server",
  "dependencies": {
    "my-plugin": "^1.2.0"
  },
  "disabled": [],
  "autoUpdate": true,
  "reviewedOnly": false,
  "updateTimeoutSeconds": 60
}
```

The egg always defers registry downloads and updates to S2Script. If the manifest is absent, startup creates one with empty dependencies and autoUpdate enabled. The native runtime resolves dependencies before its first plugin scan, respects the manifest's settings, and owns the native lock. Configure reviewed-only installation using `reviewedOnly` in this file. Unknown manifest settings are preserved when bundled panel switches change `disabled`.

Use S2Script's `sm plugins` commands in the **game console** (for example, `sm plugins install my-plugin`), or its `s2s` server CLI. `cs2egg plugins` directs you to these native commands. The old egg policy and registry manager are removed.

See the [upstream server manifest design](https://github.com/s2script/s2script/blob/main/docs/superpowers/specs/2026-10-16-server-manifest-design.md) for native resolution and ownership details.

## Node cache and pins

```bash
sudo cs2node pin s2script v0.8.1
sudo cs2node pin gamefixes v1.0.14
sudo cs2node unpin s2script
```

Use real release tags. See [addon cache](modules/addon-cache.md). Registry packages use the native manifest and lock; node addon pins select framework/native releases.
