#!/bin/sh
# make release VERSION=x.y.z：检查工作区，跑本机 CI，改版本号和变更记录，提交并打签名标签。
# 不推送，也不构建镜像；之后的步骤见 docs/release.md。
set -eu
cd "$(dirname "$0")/.."

fail() {
  echo "$1" >&2
  exit 1
}

v=${1:-}
current=$(cat VERSION)
printf '%s\n' "$v" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || fail '用法：make release VERSION=x.y.z'
[ "$v" != "$current" ] || fail "VERSION 已经是 $v，发新版本要传新的版本号"
[ "$(printf '%s\n%s\n' "$current" "$v" | sort -V | tail -n 1)" = "$v" ] || fail "$v 比当前版本 $current 小"
! git rev-parse -q --verify "refs/tags/v$v" >/dev/null || fail "标签 v$v 已存在"
[ "$(git branch --show-current)" = main ] || fail '只在 main 分支发版'
[ -z "$(git status --porcelain)" ] || fail '工作区有没提交的改动'
awk '/^## 未发布/ { f = 1; next } /^## / { f = 0 } f && /^- / { n++ } END { exit n ? 0 : 1 }' CHANGELOG.md ||
  fail 'CHANGELOG.md 的“未发布”一节是空的，先写好这次的变更'

make ci
[ -z "$(git status --porcelain)" ] || fail '跑完检查后工作区有改动，先看看是哪个步骤改了文件'

# 把“未发布”换成版本号和日期，上面留一个新的空“未发布”
printf '%s\n' "$v" >VERSION
sed -i "s/^## 未发布\$/## 未发布\n\n## $v — $(date +%F)/" CHANGELOG.md
git add VERSION CHANGELOG.md
git commit -q -m "chore: release $v"
git tag -s "v$v" -m "Tillbox $v"

echo "已提交并打好签名标签 v$v。确认无误后推送：git push origin main v$v"
