## Context

- 支付子系统以 `payment.Provider` 接口为核心：`CreatePayment` / `QueryOrder` / `VerifyNotification` / `Refund`，由 `provider.CreateProvider(providerKey, instanceID, config)` 按服务商 key 构造；服务层通过 `DefaultLoadBalancer.SelectInstance` 按实例 `supported_types` 挑实例，下单后把 `pay_url`/`qr_code`/`payment_trade_no` 写回订单，并把 `provider_key`、`merchant_id`、`currency` 写进订单快照供回调一致性校验。
- 回调入口是 `PaymentWebhookHandler.handleNotify`：先用 `extractOutTradeNo` 从原始 body 提取商户订单号找回下单实例，再让实例 `VerifyNotification`，最后 `HandlePaymentNotification` 按法币金额与 `pay_amount` 容差比对。
- 前台可见方式由已启用实例的 `supported_types` 聚合而来（`GetAvailableMethodLimits`），`enabled_payment_types` 设置只影响管理端能否创建该类型服务商。前端 `decidePaymentLaunch` 在只有 `pay_url` 时走 `redirect_waiting`。
- 线上网关：`gmwallet/epusdt:latest`（镜像标签 v2.0.0），公开地址 `https://epusdt.sparkcode.top`，`/payments/gmpay/v1/config` 返回仅 TRON 链的 `TRX`/`USDT`；旧版 `/api/v1/order/create-transaction` 已 404；GMPay 只接受 HMAC-SHA256 签名。

## Goals / Non-Goals

**Goals:**
- 与 Airwallex/EasyPay 同构地接入一个新服务商，不改动 Provider 接口与订单状态机。
- 签名与验签逐字复刻网关源码 `util/sign`：排除 `signature`、空值与 nil；浮点用 `strconv.FormatFloat(v, 'f', -1, 64)`；按整条 `k=v` 排序后 `&` 拼接；HMAC-SHA256 小写十六进制。
- 建单用表单编码，使 `amount` 字符串按 Sub2API 格式化结果原样参与签名，避免 JSON 数字被网关重新规范化。
- 回调金额（法币）直接走现有金额比对；查单在已支付时补读收银台信息拿回法币金额，满足 `isValidProviderAmount`。

**Non-Goals:**
- 不实现二维码直付（链上地址不是标准支付 URI，收银台已提供地址、金额与二维码）。
- 不接 Epusdt 的 EPay 兼容接口，也不支持 OkPay 托管通道。
- 不做汇率或代币数量的本地计算与展示；代币金额由网关决定，仅在回调元数据里记录 `actual_amount`。
- 不实现网关退款、取消订单（网关无对应 API）。

## Decisions

### D1：服务商 key 为 `epusdt`，可见支付方式为 `usdt`
- 与易支付「服务商 ≠ 支付方式」的模型一致：`SupportedTypes()` 返回 `[usdt]`，实例 `supported_types` 存 `usdt`，前台按钮显示 `USDT`；`GetBasePaymentType("epusdt")` 返回自身，`"usdt"` 走默认分支原样返回。
- 备选：把 provider key 也叫 `usdt`。否决：网关是 Epusdt，后续接其它 USDT 网关时会撞名。

### D2：配置项与校验
- 必填 `pid`、`secretKey`、`apiBase`、`notifyUrl`；`returnUrl` 可空。`apiBase` 必须是绝对 http(s) URL，允许粘贴整段建单路径（自动剥离 `/payments/gmpay/...`）。
- `token`/`network` 转小写；「同填或同空」，只填一项直接拒绝——网关对只缺一个的请求返回 10009，提前在保存时暴露。都为空时不发送这两个字段，让网关创建状态 4 占位订单，由收银台选链。
- `currency` 走 `payment.NormalizePaymentCurrency`，默认 CNY；并把 `epusdt` 加进 `paymentProviderConfigCurrency` 的分支，使订单金额按实例币种计算、快照记录币种。
- 敏感字段：`secretkey`；待支付保护字段：`secretkey`、`pid`、`apibase`、`currency`。
- 前端不给 `token`/`network` 设 `defaultValue`：对话框 `applyDefaults()` 会在每次打开时回填默认值，会把管理员刻意清空的链悄悄钉回去。

### D3：建单与跳转
- 表单字段：`pid`、`order_id`、`currency`（小写）、`amount`、`notify_url`、`redirect_url`、`name`、`token`、`network`，空值不发送；`signature` 用同一套规则计算。
- 返回 `TradeNo=trade_id`、`PayURL=payment_url`；`payment_url` 若是站内相对路径则按 `apiBase` 补全，为空时回退拼 `/pay/checkout-counter/{trade_id}`。不返回 `QRCode`，前端因此走 `redirect_waiting`（桌面端弹窗、移动端整页跳转）。
- 实例 `payment_mode` 保持空串，管理端不展示模式选择。

### D4：回调与查单
- `extractOutTradeNo` 新增 `epusdt` 分支解析 JSON `order_id`，保证多实例时找回原实例。
- `VerifyNotification` 把 JSON 解成 `map[string]any` 后用 D1 中的规则重算签名并 `hmac.Equal`；`status==2` 记 success，其它记 failed（服务层对非 success 直接忽略）。元数据带 `pid`、`token`、`actual_amount`、`block_transaction_id`、`receive_address`。
- 快照校验新增 `epusdt` 分支：回调/查单元数据的 `pid` 必须与快照 `merchant_id` 一致。
- `QueryOrder` 用 `payment_trade_no`（默认分支已按 provider key 选择 trade_no 而非 out_trade_no）：先 `check-status`，为 2 时再读 `checkout-counter-resp` 取 `amount`（法币）。
- 成功应答沿用默认纯文本 `success`；网关接受 `ok`/`success`（不区分大小写）。

### D5：退款
- `Refund` 固定返回 `epusdt does not support refunds`。管理端引导文案提示不要开启退款；不额外在保存时拦截 `refund_enabled`，与其它无退款能力服务商保持一致。

## Risks / Trade-offs

- 网关签名把浮点按 `'f', -1` 格式化，若网关未来改用 `json.Number` 或字符串金额，回调验签会失败；测试固定了官方文档示例签名与浮点/字符串等价性以便回归。
- 回调 `amount` 是网关回显的法币金额而非链上实付；用户少付时网关不会回调，多付则按订单金额入账，与网关设计一致。
- `apiBase` 为站内反代地址时（如 `http://epusdt:8000`）网关返回的 `payment_url` 仍按其自身 `app_uri`/`X-Forwarded-Proto` 生成，Sub2API 只对相对路径做补全。
