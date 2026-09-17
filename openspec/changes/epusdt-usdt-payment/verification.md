# Epusdt USDT 支付验证（2026-09-17）

## 已实现

后端新增 `epusdt` 服务商（GMPay 协议）：表单编码建单 + HMAC-SHA256 签名、check-status/checkout-counter-resp 查单、JSON 回调验签（数字按网关 `'f', -1` 规范化）、订单快照记录并校验 PID、币种按实例配置、回调路由与纯文本 `success` 应答、退款明确不支持。前端新增 `USDT` 可见方式（图标、配色、跳转收银台）、管理端服务商类型与配置表单、订单筛选、中英文 i18n。文档补充 Epusdt 章节与回调路径。

## 自动验证

- `GOTOOLCHAIN=go1.27.0 go build ./...`：通过。
- `go vet -tags=unit ./internal/payment/... ./internal/handler/ ./internal/service/ ./internal/server/routes/`：通过。
- `go test -tags=unit ./internal/payment/... ./internal/handler/ -run 'Epusdt|Webhook|ExtractOutTradeNo|WriteSuccess|GetBasePaymentType'`：通过。
- `go test -tags=unit ./internal/service/ -run 'Snapshot|Sensitive|ValidateProviderRequest|ProviderConfig|PaymentCurrency|ProviderInstance'`：通过。
- `gofmt -l` 变更文件：无输出。
- 前端 `vitest run` 六个支付相关 spec：6 个文件 88 个测试通过。
- `vue-tsc --noEmit`：通过；变更文件 ESLint：通过。
- `git diff --check`：通过。
- golangci-lint v2.13.2（用 go1.27.0 重新 `go install` 构建，原有 go1.26 构建的二进制无法读取 go1.27 导出数据）`run ./internal/payment/... ./internal/handler/ ./internal/server/routes/ ./internal/service/`：0 issues。

## 线上网关核对

- `GET https://epusdt.sparkcode.top/payments/gmpay/v1/config`：`version=v2.0.0`，`supported_assets` 仅 `tron: [TRX, USDT]`，`epay.default_token=usdt / default_network=tron`。
- 未签名 `POST /payments/gmpay/v1/order/create-transaction` 返回 HTTP 401 `signature verification failed`（路由存在、签名校验生效）；旧版 `/api/v1/order/create-transaction` 返回 404。
- `GET /pay/check-status/000` 返回 `status_code=10008 order does not exist`。
- 签名实现与 `GMWalletApp/epusdt` master 的 `src/util/sign/sign.go`、`src/middleware/check_sign.go` 逐字对照；单测固定了 wiki/API.md 的示例签名。

## 验证边界

单测用 httptest 模拟网关，未对线上网关真实建单。任务 4.3 的线上联调（创建实例、小额下单、收银台转账、回调入账）尚未执行。
