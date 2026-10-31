# S2Script and native addons

| Component | Switch (default `1`) | Exact release selector |
|---|---|---|
| [Metamod](https://github.com/alliedmodders/metamod-source) | `INSTALL_METAMOD` | Node version pin |
| [S2Script](https://github.com/s2script/s2script) | `INSTALL_S2SCRIPT` | `S2SCRIPT_VERSION` |
| [MultiAddonManager](https://github.com/Source2ZE/MultiAddonManager) | `INSTALL_MULTIADDONMANAGER` | `MULTIADDONMANAGER_VERSION` |
| [GameFixes](https://github.com/SlynxCZ/GameFixes_mm) | `INSTALL_GAMEFIXES` | `GAMEFIXES_VERSION` |

S2Script and both native addons automatically require Metamod. No other scripting framework is installed. Native downloaders select Linux/SteamRT3 archives and preserve private configs. Downloads are staged and checked before installation. S2Script requires the publisher's SHA-256 digest; native addons verify a digest when supplied.

`S2SCRIPT_CHANNEL=stable` follows stable framework releases; `prerelease` includes prereleases. This is separate from image/node channels. Exact framework tags constrain selection; registry plugin versions belong in native dependencies. Temporary service failures may retain a verified compatible install; missing/corrupt files and incompatible policy still fail visibly.

## Bundled plugins

The 18 `S2SCRIPT_PLUGIN_*` panel switches accept `default`, `1`, or `0`. `default` preserves policy and existing placement. Explicit `1` enables a bundled package; `0` disables it. When a bundled plugin is also a dependency in the native manifest, explicit switches update its `disabled` entry so the runtime keeps that choice. Updates preserve configs/data, private packages, and disabled choices. Duplicate active/disabled identities are errors.

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
sudo cs2node pin multiaddonmanager v1.6.2
sudo cs2node pin gamefixes v1.0.14
sudo cs2node unpin s2script
```

Use real release tags. See [addon cache](modules/addon-cache.md). Registry packages use the native manifest and lock; node addon pins select framework/native releases.
