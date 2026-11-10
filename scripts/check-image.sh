#!/bin/sh
# Run inside the finished image: docker run --rm -i IMAGE /bin/sh -s < scripts/check-image.sh
set -eu

. /etc/os-release
if [ "$ID:$VERSION_ID" != steamrt:4 ]; then
    echo "Expected SteamRT4, got $ID:$VERSION_ID" >&2
    exit 1
fi
getconf GNU_LIBC_VERSION
# Affected S2Script shims need the documented forced-stack compatibility mode.
GLIBC_TUNABLES=glibc.rtld.execstack=2 /lib64/ld-linux-x86-64.so.2 --list-tunables |
    grep '^glibc.rtld.execstack: 2 '
# Execute the 32-bit loader: standalone SteamCMD needs i386 support.
test -x /lib/ld-linux.so.2
/lib/ld-linux.so.2 --version
for tool in sh bash curl tar useradd; do
    command -v "$tool"
done
test "$(id -un)" = container
test "$HOME" = /home/container
test -w "$HOME"
/usr/local/bin/cs2egg version
