#!/bin/sh
# make ci：在本机跑一遍和 CI（.github/workflows/check.yml）相同的检查，发版前必须通过。
# 两边的步骤要一起改。PostgreSQL 测试用一个临时 Docker 容器，端口随机，跑完自动删除。
set -eu
cd "$(dirname "$0")/.."

make build
make lint vuln test check test-race e2e

name="tillbox-ci-pg-$$"
docker run -d --rm --name "$name" \
  -e POSTGRES_USER=tillbox -e POSTGRES_PASSWORD=tillbox-test -e POSTGRES_DB=tillbox \
  -p 127.0.0.1::5432 postgres:17-alpine >/dev/null
trap 'docker stop "$name" >/dev/null' EXIT
trap 'exit 130' INT TERM

# 初始化期间数据库只开本地套接字，走 TCP 能连上才算就绪
i=0
until docker exec "$name" pg_isready -q -h 127.0.0.1 -U tillbox; do
  i=$((i + 1))
  if [ "$i" -ge 60 ]; then
    echo 'PostgreSQL 一分钟内没有就绪' >&2
    exit 1
  fi
  sleep 1
done
port=$(docker port "$name" 5432/tcp | head -n 1 | sed 's/.*://')
TILLBOX_TEST_PG_DSN="postgres://tillbox:tillbox-test@127.0.0.1:$port/tillbox?sslmode=disable" make test-pg

echo '本机 CI 检查全部通过'
