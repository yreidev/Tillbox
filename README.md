# Tillbox

兼容易支付 V1（MD5）和 V2（RSA）的自托管收款系统。后端 Go，后台和收银台使用 React + HeroUI，支持 SQLite 和 PostgreSQL，前端嵌入单个二进制。

收款通过后台配置的“支付渠道”，每个商户选用其中一个，两种渠道都可以配多个：

- 支付宝经营码：付款人按收银台显示的金额手动付款，系统定时查询账务明细，按金额尾数和付款时间推断订单归属。同一渠道的商户共用一个收款账户和金额池。
- 上游易支付：把订单转给另一家易支付（V1 或 V2），付款信息仍在 Tillbox 收银台展示，按上游回调和查单确认到账。

个人后台只有一个管理员，登录只需要密码。方案和细节见[支付渠道](docs/channels.md)。

经营码是基于流水的推断收款；金额输错、迟付和重复付款需要人工处理。不支持 API 原路退款。已在实际环境验证过真实支付宝付款、调用方充值到账，以及重复通知不会重复加余额。

## 本地运行

需要 Go 1.26 以上、Node.js 24 以上、pnpm 10.32.1。

```sh
make build
TILLBOX_DEMO=true TILLBOX_LISTEN=127.0.0.1:8080 ./bin/tillbox
```

打开 <http://localhost:8080/admin>，用首次启动日志里的随机密码登录。`TILLBOX_DEMO=true` 是本机演示模式：可以建演示渠道，会开放模拟付款接口，没有任何渠道时自动建一个演示渠道。正式运行不要设置它，支付渠道在后台“支付渠道”页配置。

也可以在源码目录运行：

```sh
docker compose up -d --build
docker compose logs tillbox
```

镜像仅构建 `linux/amd64`。容器默认只把端口发布到本机，数据保存在命名卷中。上线前设置公网地址、HTTPS 反向代理和支付渠道。

## 文档

- [部署、配置、备份与恢复](docs/deploy.md)
- [支付渠道：经营码与上游易支付](docs/channels.md)
- [商户接入与真实收款验收](docs/integration.md)
- [AI 自动处理与付款人自助](docs/ai-support.md)
- [开发与验证](docs/development.md)
- [发版](docs/release.md)
- [后台支付测试、巡检、归档与升级](docs/operations.md)
- [项目计划和当前进度](docs/PLAN.md)
- [收款原理与局限](docs/research/collection-principle.md)
- [易支付协议规格](docs/research/epay-protocol.md)及[兼容取舍](docs/research/epay-compat.md)

许可证：[AGPL-3.0](LICENSE)。
