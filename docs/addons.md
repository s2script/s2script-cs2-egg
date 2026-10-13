# S2Script and native addons

| Component | Switch (default `1`) | Exact release selector |
|---|---|---|
| [Metamod](https://github.com/alliedmodders/metamod-source) | `INSTALL_METAMOD` | Node version pin |
| [S2Script](https://github.com/s2script/s2script) | `INSTALL_S2SCRIPT` | `S2SCRIPT_VERSION` |
| [MultiAddonManager](https://github.com/Source2ZE/MultiAddonManager) | `INSTALL_MULTIADDONMANAGER` | `MULTIADDONMANAGER_VERSION` |
| [GameFixes](https://github.com/SlynxCZ/GameFixes_mm) | `INSTALL_GAMEFIXES` | `GAMEFIXES_VERSION` |

S2Script and both native addons automatically require Metamod. No other scripting framework is installed. Native downloaders select Linux/SteamRT3 archives and preserve private configs. Downloads are staged and checked before installation. S2Script requires the publisher's SHA-256 digest; native addons verify a digest when supplied.

`S2SCRIPT_CHANNEL=stable` follows stable framework releases; `prerelease` includes prereleases. This is separate from image/node channels. Exact framework tags and bundled-plugin release pins constrain selection. Conflicting bundled pins are refused before changing S2Script. Temporary service failures may retain a verified compatible install; missing/corrupt files and incompatible policy still fail visibly.

## Bundled plugins

The 18 `S2SCRIPT_PLUGIN_*` panel switches accept `default`, `1`, or `0`. `default` preserves policy and existing placement. Explicit `1` enables a bundled package; `0` disables it. Updates preserve configs/data, private packages, and disabled choices. Duplicate active/disabled identities are errors.

## Registry plugin commands

Run these inside a **stopped** server's container/volume with the `cs2egg` binary. A runtime lock prevents mutations while the game runs. The panel game console accepts CS2 commands, not shell commands.

```bash
cs2egg plugins list
cs2egg plugins inspect my-plugin
cs2egg plugins install my-plugin
cs2egg plugins install my-plugin --version 1.2.3 --disabled
cs2egg plugins install my-plugin --with optional-provider
cs2egg plugins check
cs2egg plugins update
cs2egg plugins disable my-plugin
cs2egg plugins enable my-plugin
cs2egg plugins pin my-plugin --version 1.2.3
cs2egg plugins unpin my-plugin
cs2egg plugins remove my-plugin
cs2egg plugins reconcile
```

The root is `$HOME`, normally `/home/container`. Policy lives at `s2script-plugins.json`; `S2SCRIPT_PLUGIN_FILE` can select another safe relative path. `s2script-plugins.lock.json` records verified ownership/closure information. Active and disabled packages are checked. Private/local packages are retained, and their public-registry 404s do not prevent startup.

Policy example:

```json
{
  "plugins": {
    "my-plugin": { "source": "registry", "version": "1.2.3", "enabled": true }
  }
}
```

Sources are `registry`, `release` (bundled S2Script packages), and `local`. Exact version pins, required dependencies and selected optional providers are respected. `S2SCRIPT_REVIEWED_ONLY=1` requires reviewed registry entries. Archive identity/version, SHA-256, API/interface compatibility and dependency constraints are checked before installation. During a transient registry outage, only an unchanged verified closure satisfying current policy can be reused. Authentication, invalid origin, missing releases, tampering and incompatible changes remain errors.

## Node cache and pins

```bash
sudo cs2node pin s2script v0.8.1
sudo cs2node pin multiaddonmanager v1.6.2
sudo cs2node pin gamefixes v1.0.14
sudo cs2node unpin s2script
```

Use real release tags. See [addon cache](modules/addon-cache.md). Plugin-registry packages use separate policy/lock files; node addon pins select framework/native releases.
