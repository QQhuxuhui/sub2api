## 1. 后端服务商实现

- [x] 1.1 `internal/payment/types.go` 新增 `TypeEpusdt="epusdt"`、`TypeUSDT="usdt"`，`GetBasePaymentType` 识别 `epusdt`
- [x] 1.2 新增 `internal/payment/provider/epusdt.go`：配置校验（必填项、apiBase 规范化、token/network 同填同空、币种归一化）、`CreatePayment`（表单编码 + HMAC-SHA256）、`QueryOrder`（check-status → checkout-counter-resp）、`VerifyNotification`（JSON 验签、状态映射、元数据）、`Refund` 返回不支持、`MerchantIdentityMetadata`
- [x] 1.3 `factory.go` 注册 `epusdt`
- [x] 1.4 单测 `epusdt_test.go`：配置校验、官方文档示例签名、浮点/字符串规范化等价、建单表单与签名、无链省略字段、网关错误与非 JSON 响应、查单待支付/已支付/不存在、回调成功/未支付/篡改/错密钥/缺签名/非 JSON、退款不支持、相对 payment_url 补全

## 2. 服务层与回调

- [x] 2.1 `payment_webhook_handler.go` 新增 `EpusdtNotify` 与 `extractOutTradeNo` 的 JSON `order_id` 分支；`routes/payment.go` 注册 `POST /payment/webhook/epusdt`
- [x] 2.2 `payment_config_providers.go`：`validProviderKeys`、敏感字段 `secretkey`、待支付保护字段 `secretkey/pid/apibase/currency`
- [x] 2.3 `payment_currency.go` 让 `epusdt` 按实例 `currency` 计价
- [x] 2.4 `payment_order.go` 快照写入 `merchant_id=pid` 与 `currency`；`payment_order_provider_snapshot.go` 校验回调 `pid`
- [x] 2.5 补充测试：`payment_config_providers_test.go`（合法 key、敏感字段表）、`payment_webhook_handler_test.go`（成功应答、`order_id` 提取与畸形 body）、`payment_order_provider_snapshot_test.go`（快照字段）

## 3. 前端

- [x] 3.1 `providerConfig.ts`：`PROVIDER_SUPPORTED_TYPES.epusdt=['usdt']`、`METHOD_ORDER` 加 `usdt`、`WEBHOOK_PATHS`/`PROVIDER_CALLBACK_PATHS`、配置字段（pid/secretKey/apiBase/token/network/currency）
- [x] 3.2 `paymentFlow.ts` 可见方式别名与类型；`types/payment.ts` 联合类型
- [x] 3.3 `PaymentMethodSelector.vue` USDT 图标与选中配色；新增 `assets/icons/usdt.svg`；`PaymentView.vue` 按钮类；`style.css` `.btn-usdt`
- [x] 3.4 `SettingsView.vue` 启用类型与服务商下拉；`ProviderCard.vue` 标签；`AdminOrderTable.vue`/`AdminOrdersView.vue` 订单筛选；`PaymentProviderDialog.vue` 配置引导
- [x] 3.5 中英文 i18n：`payment.methods.usdt/epusdt`、`providerEpusdt`、`field_token/field_network`、PID/apiBase/链提示、引导文案；更新币种提示
- [x] 3.6 前端测试：`providerConfig.spec.ts`、`paymentFlow.spec.ts`、`PaymentProviderDialog.spec.ts`

## 4. 文档与验证

- [x] 4.1 `docs/PAYMENT_CN.md`、`docs/PAYMENT.md` 新增 Epusdt 章节与回调路径；三语 README 支付特性一句话
- [x] 4.2 后端 `go build`、`go vet`、单元测试、gofmt；前端 vitest、vue-tsc、eslint；`git diff --check`
- [ ] 4.3 线上联调：在管理端创建 Epusdt 实例指向 `https://epusdt.sparkcode.top`，小额下单、收银台转账、确认回调入账与订单状态
