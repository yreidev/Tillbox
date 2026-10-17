# 易支付协议规格（V1 + V2）

本文记录易支付官方服务端和官方 SDK 的实际行为，作为本系统对齐的依据：

- V1：MD5 签名，SDK 版本 V1.3；
- V2：RSA 签名。

官方文档和服务端代码不一致时，以代码为准，差异在文中注明。本系统在哪些地方有意和官方不同、为什么不同，见 [epay-compat.md](epay-compat.md)。

## 1. 两个版本的关系

| | V1 | V2 |
| --- | --- | --- |
| 跳转下单 | `/submit.php` | `/api/pay/submit` |
| 接口下单 | `/mapi.php` | `/api/pay/create` |
| 其他接口 | `/api.php?act=order\|query\|orders\|settle\|refund` | `/api/pay/query\|refund\|refundquery\|close`、`/api/merchant/info\|orders`、`/api/transfer/*` |
| 时间戳 | 不要求 | 必须带 |
| 成功码 | `code=1`（`act=refund` 例外，见第 7 节） | `code=0` |
| 响应签名 | 无 | 平台私钥签名 |

- 官方用 nginx 把 `/api/xxx` 重写成 `/api.php?s=xxx`。所以 `/api.php?s=pay/query` 和 `/api/pay/query` 是同一个接口，两种路径都要认。
- 查询串里的 `s` 是路由参数，不参与请求验签。
- V1 和 V2 共用下单代码：`/submit.php` 和 `/api/pay/submit` 是同一段代码，`/mapi.php` 和 `/api/pay/create` 也是同一段代码。两版只在三处不同：
  1. V2 入口要求 `timestamp`，见第 3.5 节。
  2. 订单会记下自己是从哪个入口创建的。**通知用哪种签名由订单版本决定**：V1 订单发 MD5 通知，V2 订单发 RSA 通知，和下单请求用的 `sign_type` 无关。
  3. 接口下单的返回格式不同，见第 5.4 节。
- 验签方式看请求里的 `sign_type`：值为 `RSA` 时用商户公钥验签，其他情况（包括不传）按 MD5 验签。因此 V1 入口也能用 RSA 签名，V2 入口也能用 MD5 签名（这时仍要带 `timestamp`）。
- 商户的密钥类型设为"只收 RSA"时，所有 `sign_type` 不是 `RSA` 的请求都会被拒绝。V1 `api.php` 里用明文 `key` 鉴权的接口也一样，返回 `{"code":-3,"msg":"该商户只能使用RSA签名类型"}`。

## 2. 密钥

| 项 | 用途 |
| --- | --- |
| 商户 ID `pid` | 整数 |
| 商户密钥 `key` | 用于 MD5 签名，也用于 V1 `api.php` 的明文鉴权 |
| 商户 RSA 公钥 | 验 RSA 请求的签名；对应的私钥由商户自己保管 |
| 密钥类型 | 0：MD5 和 RSA 都收；1：只收 RSA |
| 平台 RSA 私钥 | 给 V2 接口的响应和 V2 通知签名 |
| 平台 RSA 公钥 | 交给商户，用来验响应和通知 |

- 密钥都以纯 Base64 文本交换，不带 PEM 头尾：私钥是 PKCS#8 格式（`BEGIN PRIVATE KEY`），公钥是 SubjectPublicKeyInfo 格式（`BEGIN PUBLIC KEY`）。
- 使用时，官方把 Base64 每 64 个字符换一行，再补上 PEM 头尾；如果文本里已经有 `-----BEGIN`，就原样使用。
- 官方 SDK 样例用的是 2048 位密钥。

## 3. 签名

### 3.1 待签名串（V1 和 V2 相同）

1. 去掉 `sign` 和 `sign_type`，去掉值是数组的参数，去掉空值。空值指 null，或者去掉首尾空白后为空串（空白字符：空格、`\t`、`\n`、`\r`、`\0`、`\x0B`）。字符串 `"0"` 不算空值。
2. 参数名按字节序升序排列。
3. 拼成 `k=v`，用 `&` 连接。不做 URL 编码，值用 URL 解码后的原文。
4. **收到请求时，要对收到的全部参数验签**，不能只取已知字段。调用方可能会带额外参数，例如 `cid`、`device`，它们也参与签名。

和 SDK 的差异：V1 SDK 判断空值用的是 `$v != ''`，不去首尾空白。所以值全是空白的参数，SDK 会把它算进签名，服务端不会，两边算出的签名对不上。遇到这种情况，官方服务端照样拒绝，本系统也按服务端的规则处理。

### 3.2 MD5

签名为 `md5(待签名串 + key)`，结果是 32 位小写十六进制，`key` 直接接在后面，不加 `&`。比对签名时区分大小写（官方用的是 `===`）。

### 3.3 RSA

- 算法：SHA256WithRSA，PKCS#1 v1.5 填充。签名结果用标准 Base64 编码，带填充。
- 请求：商户用私钥签名，平台用商户公钥验签。
- 响应和通知：平台用私钥签名，商户用平台公钥验签。

### 3.4 V2 响应里值的字符串形式

商户 SDK 收到响应后，先做 JSON 解码，再按 PHP 的规则把每个值转成字符串，拼出待签名串。转换规则如下：

| JSON 值 | 转成的字符串 |
| --- | --- |
| 整数 | 十进制数字 |
| 字符串 | 原样 |
| 浮点数 | 按 PHP 规则转换，例如 `10.0` 变成 `"10"` |
| `true` | `"1"` |
| `false` | 空串（因此被跳过） |
| null、数组、对象 | 跳过 |

平台签名时必须用和这里完全相同的字符串。本系统约定：**需要签名的响应字段只用 JSON 字符串和整数**，不用浮点数和布尔值。

### 3.5 时间戳

- V2 请求必须带 `timestamp`（10 位，单位秒）。它和服务器时间相差超过 300 秒就拒绝，提示"时间戳字段不正确，请检查服务器时间"。
- SDK 验响应和通知时也只接受 300 秒以内的时间戳，所以：
  - V2 通知每次重试都要用当时的时间戳重新签名；
  - `return_url` 跳转的参数要在跳转那一刻生成。

## 4. 各接口从哪里读参数

| 接口 | 方法 | 读参数的方式 | 没有 `pid` 时 |
| --- | --- | --- | --- |
| `/submit.php`、`/api/pay/submit` | GET 或 POST | 查询串里有 `pid` 就整组用查询串，否则整组用表单 | 纯文本 `你还未配置支付接口商户！` |
| `/mapi.php`、`/api/pay/create` | POST | 只读表单 | `{"code":-4,"msg":"未传入任何参数"}` |
| 其他 `/api/*` | POST | 只读表单 | 同上 |
| `/api.php?act=order\|query\|orders\|settle` | GET | `act` 和参数都从查询串读 | — |
| `/api.php?act=refund` | POST | `act` 从查询串读，参数从表单读 | — |

## 5. 下单

### 5.1 参数

跳转下单（submit）和接口下单（create）共用一套参数。

| 参数 | submit | create | 说明 |
| --- | --- | --- | --- |
| `pid` | 必填 | 必填 | |
| `type` | 可选 | 必填 | 支付方式，如 `alipay`、`wxpay`、`qqpay`。submit 不传时进收银台让付款人选 |
| `out_trade_no` | 必填 | 必填 | 只能包含 `a-z A-Z 0-9 . _ - \|` |
| `notify_url` | 必填 | 必填 | 必须是 http(s) 地址 |
| `return_url` | 必填 | 可选 | |
| `name` | 必填 | 必填 | 超过 127 字节时，按 UTF-8 字符边界截断 |
| `money` | 必填 | 必填 | 只能包含数字和点，且大于 0。官方按两位小数入库 |
| `clientip` | — | 必填 | 必须是合法 IP |
| `device` | — | 可选 | `pc`（默认）、`mobile`、`qq`、`wechat`、`alipay`、`app`、`jump` |
| `method` | — | V2 用 | `web`、`jump`、`jsapi`、`scan`、`app`、`applet`。`device=jump` 等同于 `method=jump` |
| `param` | 可选 | 可选 | 通知时原样带回 |
| `channel_id`、`cert_no`、`cert_name`、`min_age` | 可选 | 可选 | 指定通道；实名信息 |
| `auth_code`、`sub_openid`、`sub_appid`、`is_applet` | — | 可选 | 付款码、JSAPI、小程序支付用 |
| `timestamp` | V2 必填 | V2 必填 | |
| `sign`、`sign_type` | 必填 | 必填 | |

### 5.2 同一订单号再次下单

同一个 `pid + out_trade_no` 在 10 天内再次下单时：

- 原订单已支付：报错"该订单(…)已完成支付，请勿重复发起支付"。
- `money`（按两位小数比较）、`name`、`notify_url`、`return_url`、`param` 中有任何一项变了：报错"该订单(…)支付参数有变化，请更换订单号重新发起支付"。
- 其他情况：沿用原来的平台订单号。可以换 `type`。

超过 10 天再用同一个订单号，会新建一笔同号订单。

### 5.3 平台订单号

`trade_no` 由下单时间 `YmdHis`（14 位）加 5 位随机数组成，共 19 位数字。

### 5.4 返回

**submit**：直接返回页面，跳转到支付页或显示二维码页。出错时返回 HTML 错误页，不是 JSON。

**create，V1 格式（`/mapi.php`）**

- 成功：`{"code":1,"trade_no":"…"}`，外加下面四个字段之一：
  - `payurl`：跳转地址；
  - `qrcode`：二维码内容；
  - `urlscheme`：小程序链接；
  - `html`：HTML 片段。

  成功时没有 `msg`。
- 失败：`{"code":-1,"msg":"…"}`。签名错误时 `code=-3`，通道出错时 `code=-2`。

**create，V2 格式（`/api/pay/create`）**

- 成功：`{"code":0,"trade_no":"…","pay_type":"…","pay_info":"…","timestamp":"…","sign_type":"RSA","sign":"…"}`。
  - `pay_type` 取值：`jump`、`html`、`qrcode`、`urlscheme`、`jsapi`、`app`、`scan`、`wxplugin`、`wxapp`。
  - `pay_info` 如果是对象，先编码成 JSON 字符串，再参与签名。
- 失败：`{"code":<负数>,"msg":"…"}`，不签名。

## 6. 查单

### 6.1 V1：`GET /api.php?act=order&pid=…&key=…&trade_no=…`

也可以用 `out_trade_no` 代替 `trade_no`。

- 两个都传时以 `trade_no` 为准。两个都不传：`{"code":-4,"msg":"订单号不能为空"}`。
- 商户不存在、`key` 错误、商户只收 RSA：都返回 `code=-3`。
- 订单不存在：`{"code":-1,"msg":"订单号不存在"}`。
- 成功时返回的字段（括号里是 JSON 类型）：
  - `code`（整数 1）、`msg`（`"succ"`）；
  - `trade_no`、`out_trade_no`、`type`、`name`（字符串）；
  - `api_trade_no`、`bill_trade_no`、`param`、`buyer`、`payurl`（字符串或 null）；
  - `pid`（整数）；
  - `addtime`（`Y-m-d H:i:s`）、`endtime`（字符串或 null）；
  - `money`（字符串，两位小数，如 `"10.00"`）；
  - `status`（整数）：0 未支付，1 已支付，2 已退款，3 已冻结，4 预授权。
- JSON 类型要严格照上面的来。有的调用方把 `status` 当整数解析，把 `money` 当字符串解析，类型不对整个响应都会解析失败。PHP 8.1 及以上版本的官方服务端返回的就是这些类型。
- 官方 V1 SDK：`queryOrder` 按 `trade_no` 查，`orderStatus` 判断 `status==1`。

### 6.2 V2：`POST /api/pay/query`

- 参数：`trade_no` 或 `out_trade_no`，`trade_no` 优先。
- 成功：`code` 为 0，加上以下字段，再附 `timestamp`、`sign_type`、`sign`：
  - `trade_no`、`out_trade_no`、`api_trade_no`、`bill_trade_no`、`bill_mch_trade_no`；
  - `type`、`pid`、`addtime`、`endtime`、`name`、`money`、`param`；
  - `buyer`、`clientip`、`status`、`refundmoney`。

  **值为空的字段整个不出现**（不是返回 null）。
- 订单不存在：`{"code":-1,"msg":"订单号不存在"}`。
- 官方 V2 SDK：`queryOrder` 按 `trade_no` 查，`orderStatus` 判断 `status==1`。

## 7. 退款

### 7.1 V1：`POST /api.php?act=refund`

- 表单参数：`pid`、`key`、`trade_no` 或 `out_trade_no`、`money`（必填，数字）。
- 成功：`{"code":0,"money":"…","msg":"退款成功！退款金额¥…"}`。**注意成功码是 0，不是 1。**
- 失败：

| 情况 | `code` | `msg` |
| --- | --- | --- |
| 平台没开自助退款 | -4 | |
| 商户没开退款接口 | -2 | |
| 金额错误 | -1 | |
| 订单不存在 | -1 | "当前订单不存在！" |
| 通道不支持退款 | -1 | "当前支付通道不支持API退款" |

### 7.2 V2：`POST /api/pay/refund`

- 参数：
  - `trade_no` 或 `out_trade_no`；
  - `money`；
  - `out_refund_no`：长度超过 5 才会用来防重复提交。
- 成功：`code` 为 0，返回 `refund_no`、`out_refund_no`、`trade_no`、`money`、`reducemoney`、`msg`。同一个 `out_refund_no` 已经退款成功时，直接返回那一笔。
- 失败：返回 `code=-1` 和 `msg`，**同样带签名**。

### 7.3 V2：`POST /api/pay/refundquery`

- 参数：`refund_no` 或 `out_refund_no`。
- 成功：`code` 为 0，返回以下字段：
  - `refund_no`、`out_refund_no`、`trade_no`、`out_trade_no`；
  - `uid`、`money`、`reducemoney`；
  - `status`：0 失败，1 成功；
  - `addtime`、`endtime`。
- 退款记录不存在：返回"退款记录不存在"。

## 8. 关单：`POST /api/pay/close`

- 参数：`trade_no` 或 `out_trade_no`。
- 只有未支付的订单（`status=0`）能关。成功返回 `{"code":0,"msg":"订单关闭成功"}`，带签名。
- 其他状态返回 `code=-1`，提示"该订单状态不支持关闭！"。

## 9. 商户信息和订单列表

### 9.1 V1

| 接口 | 参数 | 返回 |
| --- | --- | --- |
| `GET act=query` | `pid`、`key` | `code=1`，以及 `pid`、`key`、`active`、`money`（余额）、`type`、`account`、`username`、`orders`、`orders_today`、`orders_lastday` |
| `GET act=orders` | `pid`、`key`、`limit`（默认 10，最大 50）、`offset`、`status` | `code=1`，以及 `msg`、`count`、`data` |
| `GET act=settle` | `pid`、`key`、`limit`、`offset` | 结算记录 |

### 9.2 V2

| 接口 | 参数 | 返回 |
| --- | --- | --- |
| `/api/merchant/info` | — | `code=0`，以及 `pid`、`status`、`pay_status`、`settle_status`、`money`、`settle_type`、`settle_account`、`settle_name`、`order_num`、`order_num_today`、`order_num_lastday`、`order_money_today`、`order_money_lastday`。值为空的字段不出现 |
| `/api/merchant/orders` | `limit`、`offset`、`status` | `code=0` 和 `data`。`data` 是数组，不参与签名 |
| `/api/transfer/*` | | 代付相关接口 |

## 10. 支付结果通知

### 10.1 怎么发

- 用 GET 请求，参数拼在 `notify_url` 后面：地址里已有 `?` 就用 `&` 连接，否则用 `?`。
- 拼接不重新解析地址，也不会把参数移到 `#` 片段前；使用哈希路由的回跳页面须按实际片段读取参数。
- 超时 5 秒。不跟随重定向，也不看 HTTP 状态码。
- 响应正文只要包含 `success`、`SUCCESS`、`Success` 中的任意一个，就算送达。
- 发送时机：到账后立即发一次。失败后，大约在第 1、3、19、55 分钟各重试一次（一共 5 次），然后停下，等后台手动重发。官方代码注释写的是"1分钟，3分钟，20分钟，1小时，2小时"，实际时间点以代码为准。

### 10.2 V1 订单（MD5）

字段：`pid`、`trade_no`、`out_trade_no`、`type`、`name`、`money`、`trade_status=TRADE_SUCCESS`、`param`（非空才带）、`sign`、`sign_type=MD5`。

V1 通知不带 `timestamp`。`param` 按 PHP `empty()` 判断，因此字符串 `"0"` 也省略；这与待签名串的空值规则不同。V1 SDK 调用方需自行补入 `pid`，不能假定 SDK 的所有方法都会添加。

### 10.3 V2 订单（RSA）

字段：`pid`、`trade_no`、`out_trade_no`、`type`、`name`、`money`、`trade_status`、`api_trade_no`（非空才带）、`buyer`（非空才带）、`param`（非空才带）、`timestamp`、`sign_type=RSA`、`sign`。

和文档的差异：文档列了 `addtime`、`endtime`，实际代码不发。文档还说明，以后可能增加回调字段，商户验签时必须能处理多出来的字段。

`api_trade_no`、`buyer`、`param` 同样按 `empty()` 判断，`"0"` 不发送。

### 10.4 `money` 的格式

官方先把订单金额转成浮点数，再拼进参数，所以末尾的 0 会被去掉（PHP 8.3 实测）：

| 订单金额 | 通知里的 `money` |
| --- | --- |
| 10.00 | `10` |
| 10.50 | `10.5` |
| 10.03 | `10.03` |

### 10.5 跳回 `return_url`

- 带的参数和通知完全相同。V2 订单用跳转那一刻的时间戳重新签名。
- 付款完成超过 5 分钟后再打开，不再跳回商户，改为显示一个固定的成功页。

### 10.6 字段内容会被转义

官方在入库前，对 `name`、`param`、`notify_url`、`return_url` 做了 HTML 转义。所以通知里带回的是转义后的值，例如 `&` 会变成 `&amp;`。

## 11. 错误码汇总

| `code` | 含义 |
| --- | --- |
| 1 | V1 成功（`act=refund` 除外） |
| 0 | V2 成功；V1 `act=refund` 成功 |
| -1 | 一般错误。V2 里的"商户不存在"也是这个码 |
| -2 | 通道出错；商户没开退款接口 |
| -3 | 商户不存在（V1）、密钥错误、签名错误、商户只收 RSA |
| -4 | 没有传参数；订单号为空；平台没开退款 |
| -5 | 未知接口：V1 返回 `No Act!`，V2 返回"接口方法不存在" |
