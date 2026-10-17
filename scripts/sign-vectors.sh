#!/usr/bin/env bash
# 用官方易支付服务端源码和官方 SDK 生成签名测试向量，写到 internal/epay/testdata/sign_vectors.json。
#
# 用法：scripts/sign-vectors.sh <官方服务端源码目录> <官方 SDK 目录>
#   官方服务端源码目录：里面有 includes/functions.php 和 includes/lib/Payment.php
#   官方 SDK 目录：里面有 v1/lib/EpayCore.class.php 和 v2/SDK/lib/EpayCore.class.php
#
# 需要 Docker。容器不联网，源码只读挂载。
set -euo pipefail

if [[ $# -ne 2 ]]; then
	sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
fi
server=$(realpath "$1")
sdk=$(realpath "$2")
root=$(cd "$(dirname "$0")/.." && pwd)
out="$root/internal/epay/testdata/sign_vectors.json"

for f in "$server/includes/functions.php" "$server/includes/lib/Payment.php" \
	"$sdk/v1/lib/EpayCore.class.php" "$sdk/v2/SDK/lib/EpayCore.class.php"; do
	[[ -f $f ]] || { echo "找不到 $f" >&2; exit 1; }
done

docker run --rm --network none \
	-v "$server:/epay:ro" -v "$sdk:/sdk:ro" -v "$root/scripts/sign-vectors:/gen:ro" \
	php:8.3-cli-alpine php -d display_errors=stderr -d error_reporting=E_ALL\&~E_WARNING /gen/gen.php >"$out.tmp"
mv "$out.tmp" "$out"
echo "已写入 $out"
