#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
    echo "usage: $0 X.Y.Z" >&2
    exit 1
fi

version="$1"
if ! [[ "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "error: version must be numeric X.Y.Z without leading zeros: $version" >&2
    exit 1
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

make build

staging="dist/mon-1stplayer-$version-x86_64-linux"
tarball="dist/mon-1stplayer-x86_64-linux.tar.gz"
sidecar="dist/mon-1stplayer-x86_64-linux.tar.gz.sha256"

rm -rf "$staging" "$tarball" "$sidecar"
mkdir -p "$staging/bin" "$staging/deploy" dist

install -m 0755 bin/mon-1stplayer "$staging/bin/mon-1stplayer"
install -m 0644 deploy/mon-1stplayer.service "$staging/deploy/mon-1stplayer.service"
install -m 0644 deploy/mon-1stplayer.sysusers "$staging/deploy/mon-1stplayer.sysusers"
install -m 0644 deploy/70-mon-1stplayer.rules "$staging/deploy/70-mon-1stplayer.rules"
install -m 0644 deploy/mon-1stplayer.conf "$staging/deploy/mon-1stplayer.conf"
install -m 0644 LICENSE "$staging/LICENSE"
install -m 0644 README.md "$staging/README.md"
find "$staging" -exec touch --no-dereference --date=@0 {} +
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -cf - -C dist "$(basename "$staging")" | gzip -n > "$tarball"

(
    cd dist
    sha256sum mon-1stplayer-x86_64-linux.tar.gz > mon-1stplayer-x86_64-linux.tar.gz.sha256
)
