# Preserve custom translations while adding new phrases

The user selected merging new phrases during framework updates. Existing
operator wording and custom keys must survive; missing keys and new files come
from the matching file in the verified S2Script release.

## Approaches

- Preserve complete existing files: simple, but new phrases need manual work.
- Merge missing phrase keys: selected by the user; existing values always win.

## Behavior and constraints

Apply to `addons/s2script/translations/**/*.phrases.json`, including root and
language catalogs. Each catalog is a flat JSON object mapping keys to strings.
Merge only the corresponding release file; do not insert English into language
catalogs or scan unrelated custom-only files. Retain obsolete and custom keys.
Install missing files, and leave existing bytes alone if no keys were added.
Preserve existing permissions and publish each changed catalog atomically so
failed writes leave the operator's original file intact.

Validate and prepare all merges in staging before copying live framework files.
Invalid release or matching installed catalogs, symlinks, and non-regular targets
fail visibly without overwriting translations. Keep the existing confined path
checks. No panel variable or dependency is needed. Plugin registry packages and
runtime behavior remain owned by S2Script.

## Verification

Exercise the public framework updater with signed-release fixtures: custom text,
new and empty-string keys, language catalogs, missing and custom-only files,
unchanged files, malformed catalogs, and unsafe destination paths. Run addon
tests, the full Go suite, vet, and a static Linux build.
