#!/bin/sh
# 检查提交说明：首行写成“类型: 说明”，不带 AI 工具的署名行。
# 由 lefthook 的 commit-msg 钩子调用，参数是提交说明文件。
set -eu

msg=$(grep -v '^#' "$1")
subject=$(printf '%s\n' "$msg" | sed -n '/[^[:space:]]/{p;q;}')

case $subject in
  'Merge '* | 'Revert '* | 'fixup! '* | 'squash! '* | 'amend! '*) exit 0 ;;
esac

types='feat|fix|docs|chore|refactor|test|build|ci|perf|revert'
if ! printf '%s\n' "$subject" | grep -Eq "^($types)(\([a-z0-9-]+\))?!?: [^ ]"; then
  echo "提交说明首行要写成“类型: 说明”，类型是 ${types} 之一，例如 fix: close expired orders" >&2
  echo "现在是：$subject" >&2
  exit 1
fi
if [ "$(printf '%s' "$subject" | wc -m)" -gt 72 ]; then
  echo "提交说明首行超过 72 个字符：$subject" >&2
  exit 1
fi
if printf '%s\n' "$msg" | grep -Eiq '(co-authored-by|generated (with|by)).*(claude|anthropic|openai|codex|copilot|gemini|cursor)|🤖'; then
  echo '提交说明里有 AI 工具的署名行，请删掉' >&2
  exit 1
fi
