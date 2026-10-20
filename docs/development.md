# 开发与验证

需要 Go 1.26 以上、Node.js 24 以上、pnpm 10.32.1。Go 版本以 `go.mod` 为准：`go` 行是最低版本，`toolchain` 行是 CI 和镜像构建用的版本，本机 Go 较旧时会自动下载这个版本。后端是根模块，`web/go.mod` 仅用于让 Go 工具跳过前端依赖目录。

## 构建和默认检查

```sh
make build
make test
make check
```

`make build` 先按锁文件安装并构建前端，再编译嵌入前端的 `bin/tillbox`。只执行 `go build` 不会更新前端产物。`make test` 依次跑 Go 测试、前端测试和巡检脚本的测试：Go 测试默认使用各测试自己的临时 SQLite 数据库，支付宝请求使用本地假服务器；前端用 Vitest 只测纯逻辑（金额和时间的显示、查询参数、本机付款记录的过滤、渠道运行情况的说法），测试文件和被测代码放在一起，命名为 `*.test.ts`。`make check` 运行 Go vet 和 TypeScript 类型检查。

发版前要跑 `make ci`：和 CI 相同的全部检查，包括 lint、漏洞检查、race 检测、浏览器逐页检查和 PostgreSQL 测试（需要 Docker，脚本临时起一个容器，跑完删掉）。各步骤见 [release.md](release.md#本机-ci)。

开发预览用两个终端：

```sh
TILLBOX_DEMO=true TILLBOX_LISTEN=127.0.0.1:8080 go run ./cmd/tillbox
```

```sh
pnpm --dir web install --frozen-lockfile
TILLBOX_DEV_BACKEND=http://127.0.0.1:8080 pnpm --dir web dev --host 127.0.0.1
```

Vite 将 API 请求转发到后端。打开开发服务器给出的 `/admin` 地址，密码见后端首次启动日志。演示模式启动时如果还没有任何渠道，会自动建一个演示渠道，并让没选渠道的商户用它。演示付款可以向 `POST /web-api/dev/pay` 提交 JSON `{"trade_no":"平台订单号"}`，或 `{"amount":"1.23","payer":"测试付款人"}`（有多个演示渠道时可加 `"channel_id"`，默认第一个），然后等查账确认；这个接口只在 `fake` 演示模式下注册，只能付演示渠道的订单。

要在本机联调上游易支付渠道，可以再起一个演示模式的 Tillbox 当上游：在上游实例建商户，把它的地址、商户号和密钥填进下游实例的上游渠道（本机地址允许 HTTP），下游下单后，用上游实例的 `/web-api/dev/pay` 付上游那边的订单（订单号是上游实例的平台订单号，在下游订单详情的“上游预下单”里能看到）。

## 代码规范和提交钩子

第一次克隆后运行一次：

```sh
make deps
```

它按锁文件装前端依赖和浏览器检查用的 Chromium，按 `Makefile` 开头写的版本安装 golangci-lint、govulncheck、gitleaks、lefthook（装到 `go env GOPATH` 下的 `bin`）和 ruff（用 `uv tool` 装到 `~/.local/bin`），最后装上提交钩子。这两个目录要在 `PATH` 里。升级工具就改 `Makefile` 里的版本号再跑一次。CI 只装 lint 和漏洞检查要用的工具（`make tools`）。

| 命令 | 作用 |
| --- | --- |
| `make lint` | 只检查不改文件：Go 用 golangci-lint（配置 `.golangci.yml`），前端用 ESLint（带类型信息）和 Prettier，Python 用 ruff（配置 `ruff.toml`） |
| `make fmt` | 按规范格式化全部代码：Go 用 gofmt 和 goimports，前端用 Prettier（不加分号、单引号、行宽 120、Tailwind 类名排序），Python 用 ruff |
| `make vuln` | 用 govulncheck 查 Go 依赖里实际调用到的已知漏洞 |
| `make test-race` | 订单、流水、通知等并发相关的包开 race 检测再跑一遍 Go 测试 |
| `make e2e` | 浏览器逐页检查，见下文[浏览器逐页检查](#浏览器逐页检查) |

编辑器读 `.editorconfig`：统一 UTF-8、LF、文件末尾换行，Go 和 Makefile 用 Tab，其余用空格。

提交钩子（`lefthook.yml`）只检查这次暂存的文件，不改文件：

- 提交前：Go 文件的格式和这次改动的行有没有新问题，前端文件的 ESLint 和 Prettier，Python 文件的 ruff，以及用 gitleaks 扫暂存内容里的密钥（`.gitleaks.toml` 只放行签名测试向量里的测试密钥）。
- 写提交说明时：首行要写成 `类型: 说明`（类型是 feat、fix、docs、chore、refactor、test、build、ci、perf、revert 之一，不超过 72 个字符），不能带 AI 工具的署名行。规则在 `scripts/commit-msg.sh`。

本机想加自己的检查时，在仓库根目录建 `lefthook-local.yml`（不入库），lefthook 会把它和 `lefthook.yml` 合并。比如用一份放在仓库外的关键词清单拦住不该公开的内容：

```yaml
pre-commit:
  jobs:
    - name: private-patterns
      run: sh scripts/check-patterns.sh /path/to/patterns.txt
```

清单每行一个扩展正则，不区分大小写，`#` 开头的行是注释；`scripts/check-patterns.sh` 检查暂存改动里新增的行和文件名。

前端的 TypeScript 用 7.0（`tsc`，包名 `@typescript/native`）。typescript-eslint 还不支持 7.0，所以 `package.json` 里的 `typescript` 按微软的并排安装做法指向 TypeScript 6.0 的兼容包 `@typescript/typescript6`，只给 ESLint 读类型用。typescript-eslint 支持 7.0 以后，删掉这个别名，把 `@typescript/native` 改回 `typescript`。

## PostgreSQL

使用专用测试库，测试账号须能创建和删除 schema；每个测试建独立 schema 并在结束时删除。

```sh
docker run -d --rm --name tillbox-test-pg \
  -e POSTGRES_USER=tillbox -e POSTGRES_PASSWORD=tillbox-test \
  -e POSTGRES_DB=tillbox -p 127.0.0.1:55491:5432 postgres:17-alpine
# 等待 pg_isready 成功后运行
docker exec tillbox-test-pg pg_isready -h 127.0.0.1 -U tillbox
TILLBOX_TEST_PG_DSN='postgres://tillbox:tillbox-test@127.0.0.1:55491/tillbox?sslmode=disable' make test-pg
docker stop tillbox-test-pg
```

## 官方易支付 SDK 互通

官方 PHP SDK 不带许可证，源码不放进仓库。自行准备以下目录结构：

```text
epay-sdk/
  v1/lib/EpayCore.class.php
  v2/SDK/lib/EpayCore.class.php
```

SDK 路径交给测试：

```sh
TILLBOX_EPAY_SDK=/path/to/epay-sdk \
  go test -tags interop -count=1 ./internal/interop/
```

需要 Docker，测试使用 `php:8.3-cli-alpine` 和 host 网络访问本地 Go 测试服务，在 Linux 上运行。覆盖 V1/V2 下单、链接和表单跳转、查单、退款拒绝、V2 关单、响应及通知和回跳验签。没有设置 SDK 目录时会跳过，不能将跳过算成通过。

签名向量保存在 `internal/epay/testdata/sign_vectors.json`。重新生成方法见 `scripts/sign-vectors.sh`，只提交生成的数据，不复制 SDK。

## 前端约定

### 组件库 @yreidev/ui（私有包）

页面里的组件一律从 `@yreidev/ui` 导入，不写 `from '@heroui/react'`。它是维护者自己的组件库：原样转出开源 HeroUI 的全部导出（`linkVariants` 这类样式函数也在），另有侧栏、指标卡、图表等组件。

- 它发布在 GitHub Packages，是私有包。`web/.npmrc` 指定 `@yreidev` 作用域的仓库地址，令牌取环境变量 `GITHUB_TOKEN`，文件里不写令牌本身。
- 装前端依赖（`make build`、`make deps`、`pnpm install`）前，先导出一个能读这个包的令牌：GitHub Packages 只认经典个人令牌（personal access token classic），至少勾 `read:packages`。没有读权限的人从源码构建不了前端，请直接用发布好的镜像（见 [deploy.md](deploy.md)）。
- 构建镜像时令牌作为构建密钥传入（`docker build --secret id=GITHUB_TOKEN`，`make image` 和 `compose.yaml` 都已带上），只在 `pnpm install` 那一步可见，不留在镜像层里。
- CI 用工作流自带的 `GITHUB_TOKEN`：包设置里的 “Manage Actions access” 给了本仓库读权限，工作流声明了 `packages: read`。换仓库或 fork 后要重新授权，否则装依赖这一步会报 401/403。
- 样式：组件包自己的组件按组件引样式，一个组件一行 `@import '@yreidev/ui/css/<目录名>.css';`（不加 layer，依赖的组件会一起带上）。对外页面（收银台、付款自助、登录）也用到的写在 `web/src/styles/theme.css`，只有后台用的写在 `web/src/styles/admin.css`（随后台代码按需加载）。不要整包引 `@yreidev/ui/css`，每个页面的样式表会多 20 KB 左右（gzip），收银台也一样。拆分和层的顺序见 [ui.md](ui.md#代码组织)。
- 组件包缺的组件由维护者加进组件库、发新版本，这里升级后再用，不在本仓库里另写一个。升级：`pnpm --dir web update @yreidev/ui --latest`。

### HeroUI 代理工具

按 [HeroUI 官方说明](https://heroui.com/en/docs/react/getting-started/agent-skills) 装了 HeroUI v3 的三样代理工具，都是开发工具，不影响应用运行依赖，也不进 Docker 镜像（见 `.dockerignore`）：

| 工具 | 文件 | 来源 |
| --- | --- | --- |
| 技能 `heroui-react` | `.claude/skills/`（Claude Code）、`.agents/skills/`（Codex）、`.opencode/skills/`（OpenCode） | `curl -fsSL https://heroui.com/install \| HEROUI_SKILL_SCOPE=project bash -s heroui-react` |
| MCP `heroui-react` | `.mcp.json`（Claude Code）、`.codex/config.toml`（Codex）、`opencode.json`（OpenCode） | `npx -y @heroui/react-mcp@latest` |
| 文档索引 | `AGENTS.md`（Codex 等）、`CLAUDE.md`（Claude Code），文档在 `.heroui-docs/`（不入库） | `npx heroui-cli@latest agents-md --react --output AGENTS.md CLAUDE.md` |

- 新克隆的仓库没有 `.heroui-docs/`。按 `AGENTS.md` 里的提示重新运行上表最后一条命令，就会重新下载文档并更新索引。
- 更新技能也是重新运行上表第一条命令。
- 首次使用项目级 MCP 时，Claude Code 会询问是否信任 `.mcp.json`；Codex 重新加载项目后在 `/mcp` 里查看状态。

### 页面开发

界面用 HeroUI v3 开源组件（从 `@yreidev/ui` 导入，见上文）、Tailwind CSS v4 和 Gravity UI 图标，规范和公共组件说明见 [ui.md](ui.md)。写组件前先按 `AGENTS.md` / `CLAUDE.md` 的文档索引查 `.heroui-docs/` 里的文档，以文档和 `node_modules/@heroui` 的源码为准。HeroUI Pro 只当版式参考，不引入它的包和源码。

放在自动刷新页面里的表单（如运维页的保留期）用 `key` 绑定服务端数据，数据变了才重建表单，避免刷新时冲掉正在填写的内容。对外页面只引 `web/src/lib/ui-base.tsx` 和 `ui-form.tsx`，不要引 `ui.tsx`，否则收银台会连带下载后台组件。

TanStack Router 自动生成 `web/src/routeTree.gen.ts`，生成文件纳入版本管理。页面按路由拆包，收银台不加载后台页面。二维码绘制使用 `uqr`，上传图片识别使用 `zxing-wasm`，WASM 随构建产物提供。

## 浏览器逐页检查

`make e2e` 用 Playwright 起两个演示模式的 Tillbox（用 `bin/tillbox`，要先 `make build`）：一个空数据，看空列表的兜底；一个由 `web/e2e/setup.ts` 通过接口造好数据（两个商户、待付和已付订单、两条对不上订单的流水异常（一笔少付、一笔金额谁也对不上）、一条付款申诉），看各种状态。每个实例按桌面和手机宽度、浅色和深色各打开一遍所有后台页面和对外页面，检查：

- 没有脚本异常和控制台错误；
- 没落到路由的出错页，后台页面没被跳回登录页；
- 页面不横向滚动。

两个实例只监听 `127.0.0.1:18591` 和 `127.0.0.1:18592`，数据目录在 `web/e2e/.data/`，每次运行前清空；检查跑完 Playwright 会停掉它们。Chromium 由 `make deps` 装，版本跟着 `web/package.json` 里的 `@playwright/test`；单独装用 `pnpm --dir web exec playwright install chromium`，缺系统库时加 `--with-deps`（要 sudo）。

- 只跑一部分：`pnpm --dir web exec playwright test -c e2e --project setup --project data-desktop-light`。项目名是 `<empty|data>-<desktop|mobile>-<light|dark>`，`setup` 负责登录和造数据，不能省。
- 改版后人工过一遍：加环境变量 `E2E_SCREENSHOTS=1`，每页的整页截图存到 `web/test-results/<用例>/page.png`。
- 没过时 `web/test-results/` 里有截图和 trace，用 `pnpm --dir web exec playwright show-trace <trace.zip>` 打开；CI 没过时上传 HTML 报告（附件 `playwright-report`）。
- 新增页面要加进 `web/e2e/pages.spec.ts` 的页面列表。

## 验证范围

自动化测试证明本地协议、数据库、状态流转和模拟支付宝响应处理符合断言，不代表实际账户能查询流水或调用方已完成真实充值。上线验收见 [integration.md](integration.md)。
