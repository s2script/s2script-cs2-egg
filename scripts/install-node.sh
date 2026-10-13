#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Download the matching node binary/checksum, then enter the upstream wizard.
set -euo pipefail

release="${S2NODE_RELEASE:-v0.2.0-alpha.1}"
[[ "$release" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]] || {
    echo "S2NODE_RELEASE must be a release tag such as v0.2.0-alpha.1" >&2; exit 1;
}
[[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]] || {
    echo "The node requires Linux x86-64." >&2; exit 1;
}
headless=false
for argument in "$@"; do [[ "$argument" != --yes ]] || headless=true; done
if ! $headless && ! (exec 3<>/dev/tty) 2>/dev/null; then
    echo "The wizard needs a terminal. Run interactively, or pass --yes for defaults." >&2
    exit 1
fi
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
base="https://github.com/s2script/s2script-cs2-egg/releases/download/$release"
curl -fsSL "$base/cs2node_linux_amd64" -o "$scratch/cs2node_linux_amd64"
curl -fsSL "$base/checksums.txt" -o "$scratch/checksums.txt"
(
    cd "$scratch"
    # Check only the expected binary, never paths supplied by a checksum file.
    awk '$2 == "cs2node_linux_amd64" && length($1) == 64 && $1 !~ /[^0-9a-fA-F]/ {print; count++} END {if(count != 1) exit 1}' checksums.txt > node.sha256
    sha256sum -c node.sha256
)
chmod 700 "$scratch/cs2node_linux_amd64"
# VPK sync executes SteamCMD on the host, outside the SteamRT3 container.
if command -v dpkg-query >/dev/null && command -v apt-get >/dev/null; then
    missing=()
    for package in lib32gcc-s1 lib32stdc++6; do
        [[ "$(dpkg-query -W -f='${db:Status-Status}' "$package" 2>/dev/null || true)" == installed ]] || missing+=("$package")
    done
    if ((${#missing[@]})); then
        echo "Installing host SteamCMD runtime: ${missing[*]}"
        sudo apt-get update
        sudo apt-get install -y --no-install-recommends "${missing[@]}"
    fi
else
    echo "This bootstrap supports Debian/Ubuntu with apt. On other distributions, install the host's 32-bit SteamCMD runtime and run the verified cs2node binary's install command directly." >&2
    exit 1
fi
channel=stable
[[ "$release" != *-* ]] || channel=beta
if $headless; then
    sudo "$scratch/cs2node_linux_amd64" install --channel "$channel" "$@" </dev/null
else
    # curl | bash consumes stdin; the interactive Go wizard reads the terminal.
    sudo "$scratch/cs2node_linux_amd64" install --channel "$channel" "$@" </dev/tty
fi
