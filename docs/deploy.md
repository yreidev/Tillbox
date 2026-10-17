# 部署与维护

## 1. 运行条件

- 单实例运行，同一个收款账户只能由一个 Tillbox 实例分配金额和查账。PostgreSQL 也不支持多实例。
- 一个公网 HTTPS 地址，例如 `https://pay.example.com`，部署在域名根路径。
- 支付宝经营码，以及能够调用 V3 账务明细查询的开放平台应用、公钥模式密钥。
- 服务器启用 NTP 时间同步。流水时间来自支付宝，创建订单时间来自服务器；时间偏差会影响匹配，易支付 V2 也只允许 300 秒时间差。

## 2. Docker Compose

在仓库根目录建立 `.env`：

```dotenv
TILLBOX_PUBLIC_URL=https://pay.example.com
```

启动并查看首次生成的管理员密码：

```sh
docker compose up -d --build
docker compose logs tillbox
```

打开 `https://pay.example.com/admin`，只输入密码。首次登录后，从右上角“管理员 → 修改密码”更换密码。

示例 Compose 只发布 `127.0.0.1:8080`，并使用命名卷保存 `/data`。容器以 UID/GID 10001 运行。改成宿主机目录挂载时，先将该目录的所有者设为 `10001:10001`，否则无法写入主密钥和数据库。

镜像仅支持 `linux/amd64`。示例 Compose 从源码构建；也可以直接用发布好的镜像，把 `compose.yaml` 里的 `build` 去掉，`image` 改成具体版本：

```yaml
image: ghcr.io/yreidev/tillbox:0.3.0
```

每个版本发布时 CI 会推送 `ghcr.io/yreidev/tillbox:x.y.z`，最新版本另有 `latest` 标签；正式运行写具体版本号，升级时再改。只在本机构建镜像：

```sh
make image VERSION=dev
```

## 3. 二进制

```sh
make build VERSION=dev
TILLBOX_DATA_DIR=/var/lib/tillbox \
TILLBOX_LISTEN=127.0.0.1:8080 \
TILLBOX_PUBLIC_URL=https://pay.example.com \
./bin/tillbox serve
```

数据目录应由运行服务的用户拥有。用进程管理器保持服务运行；停止时发送 SIGTERM，HTTP 请求有 15 秒退出宽限期。`GET /healthz` 返回 `ok`，用于进程健康探测，不表示支付宝配置或权限有效。

## 4. 环境变量

| 变量 | 默认值 | 作用 |
| --- | --- | --- |
| `TILLBOX_LISTEN` | `:8080` | HTTP 监听地址 |
| `TILLBOX_DATA_DIR` | `data`（镜像为 `/data`） | SQLite 和自动生成的主密钥所在目录 |
| `TILLBOX_DB_DRIVER` | `sqlite` | `sqlite` 或 `postgres` |
| `TILLBOX_DB_DSN` | SQLite 为数据目录下 `tillbox.db` | PostgreSQL 必填连接串 |
| `TILLBOX_PUBLIC_URL` | 空 | 对外地址；为空时从请求推断，正式部署应显式设置 |
| `TILLBOX_TRUSTED_PROXIES` | 空 | 可信代理 IP 或 CIDR，逗号分隔；只接受这些来源的转发头 |
| `TILLBOX_MASTER_KEY` | 空 | 32 字节密钥的 Base64；为空时读取或生成 `master.key` |
| `TILLBOX_DEMO` | `false` | `true` 是本机演示模式：允许建演示渠道并模拟付款，没有任何渠道时自动建一个。正式运行不要设置，支付渠道全部在后台配置 |
| `TILLBOX_LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` |

支付渠道（经营码、上游易支付）、商户和查账间隔在后台配置，不放环境变量。主密钥用于加密库里的商户密钥、渠道密钥（支付宝应用私钥、上游的商户密钥和私钥）和平台私钥，必须与数据库一起备份。

### PostgreSQL

准备专用数据库和用户，再设置：

```sh
export TILLBOX_DB_DRIVER=postgres
export TILLBOX_DB_DSN='postgres://tillbox:replace-me@db:5432/tillbox?sslmode=require'
```

首次启动自动迁移；连接用户需要建表权限。Compose 使用 PostgreSQL 时，在服务的 `environment` 中加入这两个变量；仍保留 `/data` 卷保存主密钥。数据库使用 `pg_dump` 备份，主密钥另外备份。

SQLite 使用 WAL、10 秒忙等待、最多 4 个连接及 `BEGIN IMMEDIATE` 写事务；PostgreSQL 最多 10 个连接。不要将 SQLite 文件放在网络文件系统，也不要让两个进程共用数据库和收款账户。

## 5. HTTPS 反向代理

Nginx 已有 TLS 配置的站点中转发整个站点：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}
```

设置正确的 `TILLBOX_PUBLIC_URL`。若需要记录真实客户端 IP，将 Tillbox 实际看到的代理来源地址加入 `TILLBOX_TRUSTED_PROXIES`；宿主机代理转到 Docker 发布端口时，来源可能是 Docker 网桥地址，需要按实际网络确认。不要信任全部来源。Go 自己处理 `/api/xxx` 和 `/api.php?s=xxx`，无需 PHP 或额外重写规则。

## 6. 支付渠道配置

收款通过后台的“支付渠道”页配置。渠道有两种，都可以建多个，每个商户选用其中一个：

- 支付宝经营码：付款人扫经营码付款，Tillbox 按金额尾数查支付宝账务明细确认到账。
- 上游易支付：把订单转给另一家易支付收款，配置见[接入文档](integration.md#3-上游易支付渠道)。

新建支付宝经营码渠道：

1. 打开“支付渠道 → 新建渠道 → 支付宝经营码”，填名称后先创建，表单里有接入步骤。
2. 点击“生成密钥对”，将应用公钥上传到开放平台的对应应用，接口加签方式选公钥模式。
3. 填应用 ID 和该应用对应的支付宝公钥；也可以导入已有应用私钥。
4. 上传经营码截图或填写 `https://qr.alipay.com/...` 地址，核对收款账户和收款方名称。
5. 保存，点击“测试连接”。确认应用有账务明细查询权限、查询账户和经营码收款账户一致。
6. 连接成功后打开“启用”，到商户页给商户选这个渠道，再按[接入验收](integration.md)做真实小额测试。

每个启用的经营码渠道有自己的查账任务，进度和退避互不影响。一个支付宝账户只能配一个经营码渠道：两个渠道查同一个账户时，两边都会看到同一笔付款，而金额尾数按渠道分配，一笔付款可能让两个渠道各到账一单。后台会拒绝重复的应用 ID 和经营码；用两个不同应用查同一个账户的情况系统发现不了，不要这样配。只有没有订单、流水，也没有商户在用的渠道才能删除，不能删时删除按钮是灰的，鼠标移上去会说明原因；不用的渠道停用即可，停用后不再查账，历史数据都保留。

从 0.2.x 升级时，原“设置 → 支付宝收款”的配置自动变成 1 号经营码渠道，加密后的私钥原样沿用，历史订单、流水、查账进度都归到它，商户也自动选上它（它没启用时选第一个启用的渠道），不需要手工操作。

应用权限如何开通、真实流水出现的延迟和接口限流阈值，需要在实际支付宝账户上确认；本地测试不会验证这些条件。

## 7. 密码重置

```sh
docker compose exec tillbox tillbox reset-password
```

二进制部署使用相同的数据目录、数据库和主密钥环境变量执行 `./bin/tillbox reset-password`。新密码打印到终端，所有旧登录会话立即失效。命令没有用户名参数。

## 8. 备份、恢复和升级

目前没有自动备份任务，需由运维安排。最直接的 SQLite 备份方式是停服务后备份整个数据目录（含 `master.key`，以及可能存在的 `tillbox.db-wal`、`tillbox.db-shm`），完成后再启动；不要运行中只复制 `.db` 文件。PostgreSQL 使用数据库的一致性备份工具，并单独备份主密钥。若主密钥来自环境变量，备份同一个值。

恢复时先停服务，恢复数据库与配套主密钥，确认目录权限，再启动。只有数据库没有密钥无法恢复商户和渠道密钥。

升级前做备份，再更新源码或镜像并重建、启动。迁移在启动时自动执行；数据库变更后的回滚应恢复对应版本的数据库备份。未发布版本可能修改初始迁移，旧开发库不保证直接兼容，演示数据可换新数据目录，真实数据须先核对迁移。

## 9. 日常检查

在概览或流水页按渠道检查“已查到”“上次成功”“下一轮”和“历史补扫”；上游易支付渠道的预下单、查单和回调错误显示在“支付渠道”页和运维巡检。补扫每轮最多一个时间段，失败独立退避；限流或凭据错误会让主窗口也等待。修好配置后可点“立即查账”清除退避。

“流水与异常”用于核对少付、多付、重复付款、迟付和冲突。人工关联、撤销关联、处理异常都要填写理由，并写入审计日志。撤销关联会把订单改为争议状态并停止尚未完成的通知，无法撤回调用方已发放的额度，须另外与调用方核对。
