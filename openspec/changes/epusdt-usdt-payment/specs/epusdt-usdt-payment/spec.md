## Purpose

让站点通过自托管的 Epusdt（GM Pay）加密货币网关接受 USDT 等链上代币充值：管理员配置一个 `epusdt` 服务商实例后，用户端出现 `USDT` 支付方式，按法币金额下单、跳转网关收银台转账，链上到账后由网关回调自动完成充值。

## ADDED Requirements

### Requirement: Epusdt 服务商实例配置校验
系统 SHALL 接受 provider key 为 `epusdt` 的服务商实例，配置项为 `pid`、`secretKey`、`apiBase`、`notifyUrl`、`returnUrl`、`token`、`network`、`currency`。启用实例保存时 MUST 通过构造校验：`pid`、`secretKey`、`apiBase`、`notifyUrl` 非空；`apiBase` 为绝对 http(s) URL；`token` 与 `network` 同时非空或同时为空；`currency` 为合法三字母 ISO 币种（空值视为 CNY）。`secretKey` MUST 作为敏感字段不回显；`secretKey`、`pid`、`apiBase`、`currency` 在实例存在进行中订单时 MUST NOT 被修改。

#### Scenario: 只填写 token 未填写 network
- **WHEN** 管理员保存启用的 epusdt 实例且 `token=usdt`、`network` 为空
- **THEN** 系统 MUST 拒绝保存并提示 token 与 network 必须同时设置

#### Scenario: apiBase 粘贴了完整建单地址
- **WHEN** `apiBase` 为 `https://pay.example.com/payments/gmpay/v1/order/create-transaction`
- **THEN** 系统 MUST 规范化为 `https://pay.example.com` 后保存并用于拼接接口路径

#### Scenario: 读取实例配置
- **WHEN** 管理端读取 epusdt 实例
- **THEN** 返回的 config MUST 不包含 `secretKey`

### Requirement: 用 GMPay 协议创建交易并跳转收银台
用户选择 `usdt` 下单时，系统 SHALL 向 `apiBase + /payments/gmpay/v1/order/create-transaction` 以 `application/x-www-form-urlencoded` 提交 `pid`、`order_id`（商户订单号）、`currency`（实例币种小写）、`amount`（按币种格式化的支付金额字符串）、`notify_url`、`redirect_url`、`name`，以及在实例配置了链时的 `token`、`network`；空值 MUST NOT 发送。`signature` MUST 为：排除 `signature` 与空值后，按 `k=v` 字符串升序以 `&` 拼接，再以 `secretKey` 为密钥计算 HMAC-SHA256 的小写十六进制。成功时系统 MUST 记录 `trade_id` 为订单的服务商交易号，并把 `payment_url` 作为跳转地址返回前端；不返回二维码内容。

#### Scenario: 固定 TRON 链 USDT
- **WHEN** 实例配置 `token=usdt`、`network=tron`、`currency=CNY`，用户下单 100.00
- **THEN** 建单表单 MUST 含 `token=usdt&network=tron&currency=cny&amount=100.00`，且 `signature` 与文档规则一致
- **THEN** 前端 MUST 以 `redirect_waiting` 方式打开 `payment_url`

#### Scenario: 未固定链
- **WHEN** 实例 `token` 与 `network` 均为空
- **THEN** 建单表单 MUST NOT 含 `token`、`network`，网关创建占位订单由收银台选链

#### Scenario: 网关返回业务错误
- **WHEN** 网关返回 `status_code` 非 200（如 10003 无可用钱包地址）
- **THEN** 系统 MUST 以包含网关错误码与 message 的错误终止下单，不写入订单支付信息

#### Scenario: 配置了收银台域名
- **WHEN** 实例配置 `cashierBase=https://pay.example.com`，网关返回 `payment_url=https://gateway.example/pay/checkout-counter/{trade_id}`
- **THEN** 返回前端的跳转地址 MUST 为 `https://pay.example.com/pay/checkout-counter/{trade_id}`（保留路径与查询串）
- **THEN** 不在网关 `/pay/`、`/cashier/` 路径下的第三方托管支付链接 MUST NOT 被改写

#### Scenario: payment_url 为相对路径
- **WHEN** 网关返回 `payment_url=/pay/checkout-counter/{trade_id}`
- **THEN** 系统 MUST 按 `apiBase` 补全为绝对地址后返回

### Requirement: 回调验签与订单确认
系统 SHALL 在 `POST /api/v1/payment/webhook/epusdt` 接收网关 JSON 回调，先从 body 的 `order_id` 找回下单实例，再用该实例的 `secretKey` 验签：签名规则与建单一致，其中 JSON 数字 MUST 按 `strconv.FormatFloat(v, 'f', -1, 64)` 参与签名。验签失败 MUST 返回 400；验签通过且 `status=2` 时 MUST 以 `order_id` 为商户订单号、`trade_id` 为交易号、`amount`（法币）为支付金额触发确认流程，并把 `pid`、`token`、`actual_amount`、`block_transaction_id`、`receive_address` 记入元数据；`status` 非 2 时 MUST 忽略但仍应答成功。成功应答 MUST 为 HTTP 200 纯文本 `success`。

#### Scenario: 合法的支付成功回调
- **WHEN** 回调携带正确签名、`status=2`、`amount` 与订单支付金额一致、`pid` 与订单快照 `merchant_id` 一致
- **THEN** 订单 MUST 进入已支付并完成充值，响应 `success`

#### Scenario: 金额被篡改
- **WHEN** 回调 `amount` 被修改而签名未随之更新
- **THEN** 系统 MUST 判定签名无效并返回 400

#### Scenario: pid 与订单快照不一致
- **WHEN** 回调签名有效但 `pid` 与订单快照中的 `merchant_id` 不同
- **THEN** 系统 MUST 拒绝确认该订单

#### Scenario: 未知订单
- **WHEN** 回调 `order_id` 在本站不存在
- **THEN** 系统 MUST 应答 200 `success` 以停止网关重试，并记录告警日志

### Requirement: 主动查单
系统 SHALL 用订单的服务商交易号调用 `GET /pay/check-status/{trade_id}`；`status=2` 时 MUST 再调用 `GET /pay/checkout-counter-resp/{trade_id}` 读取法币 `amount`，返回已支付状态与该金额；其它状态返回待支付。网关返回 `status_code` 非 200（如 10008 订单不存在）时 MUST 返回错误而非待支付。

#### Scenario: 用户主动核对已支付订单
- **WHEN** 网关 check-status 返回 `status=2`，checkout-counter-resp 返回 `amount=100`
- **THEN** 查单结果 MUST 为已支付且金额 100，可触发与回调相同的确认流程

### Requirement: 不支持退款
epusdt 服务商的退款调用 MUST 返回明确的不支持错误，不得向网关发起任何请求。

#### Scenario: 管理员对 USDT 订单执行网关退款
- **WHEN** 管理员在订单上发起退款
- **THEN** 系统 MUST 返回退款失败，提示该服务商不支持退款

### Requirement: 管理端与用户端展示
管理端 SHALL 在「启用的支付类型」与服务商类型下拉中提供 `epusdt`，服务商卡片显示 Epusdt 标签，订单筛选提供 `usdt`；服务商对话框 SHALL 显示 pid、密钥、API 基础地址、收款币种、收款网络、支付币种字段，以及自动拼接的异步通知与同步跳转地址；不显示支付模式选择。用户端 SHALL 在有启用的 epusdt 实例时显示 `USDT` 方法（自带图标与配色），并按站点法币金额与手续费展示。

#### Scenario: 管理员新建 Epusdt 服务商
- **WHEN** 管理员在启用类型中打开 `epusdt` 并新建服务商
- **THEN** 对话框 MUST 展示上述字段与回调路径 `/api/v1/payment/webhook/epusdt`、`/payment/result`，且 `token`/`network` 不预填默认值
