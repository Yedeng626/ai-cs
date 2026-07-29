#!/usr/bin/env sh
# 下载 ip2region v4/v6 xdb 到 backend/data/（本地开发用）
set -e
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIR="$ROOT/backend/data"
mkdir -p "$DIR"
BASE="https://github.com/lionsoul2014/ip2region/raw/master/data"
for f in ip2region_v4.xdb ip2region_v6.xdb; do
  DEST="$DIR/$f"
  curl -fsSL -o "$DEST" "$BASE/$f"
  echo "OK: $DEST ($(wc -c < "$DEST" | tr -d ' ') bytes)"
done
