## Why

站点已经在 OVH 上自托管了开源的 Epusdt（GM Pay）加密货币收款网关（`gmwallet/epusdt` v2.0.0，仅启用 TRON 链的 USDT/TRX），但 Sub2API 的内置支付只有易支付、支付宝官方、微信官方、Stripe 和 Airwallex 五种服务商，没有任何链上收款能力。想让用户用 USDT 充值，目前只能借易支付「自定义支付方式」转发到兼容 EPay 协议的第三方，既绕远又无法直接对接自己的网关。

## What Changes

- 新增服务商 `epusdt`，对接 Epusdt v2 的 GMPay 协议：`POST /payments/gmpay/v1/order/create-transaction` 建单（HMAC-SHA256 签名、表单编码），`GET /pay/check-status/{trade_id}` 与 `GET /pay/checkout-counter-resp/{trade_id}` 查单，`POST` JSON 回调按网关同款规则验签。
- 前台新增可见支付方式 `usdt`：用户按站点法币金额下单，跳转到网关收银台转账，链上到账后回调自动完成充值。
- 服务商实例配置：`pid`、`secretKey`（敏感）、`apiBase`、`token`/`network`（可选，同填或同空）、`currency`（默认 CNY）；异步通知与同步跳转地址由前端按站点域名自动拼接并随每笔订单提交给网关。
- 订单快照记录 `merchant_id=pid` 与币种；回调与查单结果都要与快照中的 PID 一致才能确认。
- 回调路由 `POST /api/v1/payment/webhook/epusdt`，成功应答纯文本 `success`。
- 退款：该服务商不支持网关退款，`Refund` 直接返回错误。
- 管理端：服务商类型下拉、启用类型开关、服务商卡片标签、订单筛选、配置引导文案；用户端：方法选择器图标与配色、支付按钮配色。
- 文档：`docs/PAYMENT_CN.md`、`docs/PAYMENT.md` 与三语 README 补充 Epusdt。

## Capabilities

### New Capabilities
- `epusdt-usdt-payment`：Epusdt 服务商的配置校验、建单、查单、回调验签、订单快照一致性，以及前台 `usdt` 可见方式的展示与跳转。

### Modified Capabilities
<!-- openspec/specs 目前为空，没有既有能力需要修改。 -->

## Impact

- **后端**：`internal/payment/types.go` 新增 `TypeEpusdt`/`TypeUSDT`；`internal/payment/provider/epusdt.go` 新实现并注册进 `factory.go`；`payment_webhook_handler.go`/`routes/payment.go` 新增回调；`payment_config_providers.go` 的敏感字段、待支付保护字段、合法服务商 key；`payment_currency.go`、`payment_order.go`、`payment_order_provider_snapshot.go` 的币种与商户快照分支。
- **前端**：`providerConfig.ts`、`paymentFlow.ts`、`types/payment.ts`、`PaymentMethodSelector.vue`、`PaymentProviderDialog.vue`、`ProviderCard.vue`、`SettingsView.vue`、`AdminOrderTable.vue`、`AdminOrdersView.vue`、`PaymentView.vue`、`style.css`、新增 `assets/icons/usdt.svg`、中英文 i18n。
- **数据库**：无迁移。服务商 key 与支付方式均为字符串列，`enabled_payment_types` 设置项按逗号分隔存储。
- **外部依赖**：无新增 Go/npm 依赖。网关必须能访问本站回调地址；本站必须能访问网关 API。
