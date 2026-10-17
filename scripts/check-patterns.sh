#!/bin/sh
# 用一份关键词清单检查暂存的改动（新增的行和文件名），清单里每行一个扩展正则，不区分大小写。
# 清单放在仓库外，用来拦住不该公开的内容，例如自己的域名和内部地址：
#   scripts/check-patterns.sh <清单文件>
set -eu

list=${1:?用法：scripts/check-patterns.sh <清单文件>}
patterns=$(grep -v -e '^[[:space:]]*$' -e '^#' "$list" || true)
[ -n "$patterns" ] || exit 0

hits=$({
  git diff --cached --name-only --diff-filter=ACMR
  git diff --cached -U0 --no-color --diff-filter=ACMR | grep '^+' | grep -v '^+++'
} | grep -i -E -e "$patterns" || true)

if [ -n "$hits" ]; then
  echo '暂存的改动里有清单中的关键词：' >&2
  printf '%s\n' "$hits" | cut -c1-200 >&2
  exit 1
fi
