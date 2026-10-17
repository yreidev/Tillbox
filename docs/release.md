# 发版

版本号写在根目录的 `VERSION`，格式 `x.y.z`；`tillbox version`、镜像的 OCI 标签和 git 标签 `vx.y.z` 都以它为准。

## 准备

- 本机装好 `make deps` 里的工具，能跑 Docker（`make ci` 会临时起一个 PostgreSQL 容器）。
- 提交和标签都要签名，签名用的 SSH agent 先解锁。
- 在 `main` 分支，工作区干净。
- `CHANGELOG.md` 的“未发布”一节写全这次的变更：按使用者能感受到的变化写；升级时要额外做的事（新的数据库迁移、巡检脚本要一起换、配置项改名）单独写清楚。

## 步骤

1. 发版：

   ```sh
   make release VERSION=x.y.z
   ```

   脚本 `scripts/release.sh` 依次：
   1. 检查版本号格式、比当前版本大、标签还不存在、在 `main` 上、工作区干净、“未发布”不是空的。
   2. 跑 `make ci`，和 CI 相同的全部检查，见下文。
   3. 把 `VERSION` 改成新版本，把 `CHANGELOG.md` 的“未发布”改成“x.y.z — 日期”，上面再留一个空的“未发布”。
   4. 提交 `chore: release x.y.z`，打签名标签 `vx.y.z`。

   它不推送，也不构建镜像。

2. 核对提交和标签：

   ```sh
   git show --stat HEAD
   git show vx.y.z --no-patch
   ```

3. 推送：

   ```sh
   git push origin main vx.y.z
   ```

   GitHub Actions 会跑一遍检查；标签检查通过后还会构建 linux/amd64 镜像：
   - 推到 GHCR：`ghcr.io/yreidev/tillbox:x.y.z`；这个标签是所有版本里最新的，还会更新 `latest`。
   - 镜像归档和 SHA256 存在这次运行的 Artifacts 里（`tillbox-image-linux-amd64`），保留 90 天。

   推送失败或要给以前的版本补推时，手动跑一次，会从那个标签的源码重新构建，不再跑检查：

   ```sh
   gh workflow run check.yml -f tag=vx.y.z
   ```

   GHCR 上第一次出现这个包时默认是私有的，要在 GitHub 个人主页的 Packages → tillbox → Package settings → Change visibility 改成公开，只需改一次。

4. 不经过 CI、在本机构建镜像：

   ```sh
   make image IMAGE=tillbox:x.y.z
   docker save tillbox:x.y.z -o tillbox-x.y.z-linux-amd64.tar
   sha256sum tillbox-x.y.z-linux-amd64.tar > tillbox-x.y.z-linux-amd64.tar.sha256
   ```

5. 升级部署：先备份，再按 [deploy.md](deploy.md) 和 [operations.md](operations.md) 的升级说明换镜像。

## 本机 CI

`make ci`（`scripts/ci.sh`）和 `.github/workflows/check.yml` 跑同样的检查，改其中一处要同步另一处：

| 步骤 | 内容 |
| --- | --- |
| `make build` | 按锁文件装前端依赖，构建前端和 `bin/tillbox` |
| `make lint` | golangci-lint、ESLint、Prettier、ruff |
| `make vuln` | govulncheck 查 Go 依赖的已知漏洞 |
| `make test` | Go 测试（SQLite）、前端 Vitest、巡检脚本测试 |
| `make check` | Go vet、TypeScript 类型检查 |
| `make test-race` | 并发相关的包开 race 检测再跑一遍 |
| `make test-pg` | 用 PostgreSQL 再跑一遍 Go 测试；本机由脚本临时起容器，跑完删掉 |

## 出错时

- `make ci` 没过：脚本还没改任何文件，修好后重新发版。
- 提交或打标签时签名失败（例如 SSH agent 锁着）：`VERSION` 和 `CHANGELOG.md` 已经改好。解锁后补上没完成的步骤：

  ```sh
  git commit -m "chore: release x.y.z"   # 提交已经成功的话跳过
  git tag -s vx.y.z -m "Tillbox x.y.z"
  ```

  不发了就用 `git restore VERSION CHANGELOG.md` 还原。
- 标签打错了、还没推送：`git tag -d vx.y.z` 删掉标签，改好后重新打。已经推送的标签不要改，发一个新版本。
