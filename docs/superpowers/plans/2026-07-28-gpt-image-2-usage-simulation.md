# gpt-image-2 usage 模拟与响应对齐 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让被标记账号的 `/v1/images/*` 响应在 usage 结构与官方 gpt-image-2 一致，并按实测三维 token 表（比例 × 尺寸档 × quality）计费。

**Architecture:** 一个纯函数模块，先过四道闸门（账号标记 / 模型白名单 / 请求能力 / 几何精确匹配），任一不过即原样透传；通过后合成 usage，同一份数据既写入响应体又作为计费 usage 返回，一致性由单测守住。唯一接入点是 `handleOpenAIImagesNonStreamingResponse`，在 `c.Data` 写出前拦截。

**Tech Stack:** Go；`tidwall/gjson`（读）+ `tidwall/sjson`（写）；`image.DecodeConfig`（单次解码取宽高与格式）；标准库 `testing`（表驱动，与 `gemini_pro_image_mask_test.go` 同风格，不引入 testify）。

设计文档：`docs/superpowers/specs/2026-07-28-gpt-image-2-usage-simulation-design.md`
数据来源：`docs/GPT_IMAGE_2_TOKEN_REFERENCE.md`

> **v2 说明**：本计划为采纳代码评审 5 项 P1 后的重写版。相对 v1 的实质改动：
> ①token 表由「尺寸档兼作 quality」改为真实三维；②新增模型白名单与请求能力门控；
> ③几何由「最近比例吸附」改为「30 组精确尺寸匹配，未命中即降级」；
> ④远程输入图改用上游聚合值兜底；⑤base64 单次解码，响应校验与改写共享一次结构化解析。
>
> **v2.1 说明**：核对 adobe2api 实际实现后的三项调整（详见设计文档同名小节）：
> ⑥闸门 3 增加 `response_format` —— adobe2api 支持 `response_format=url`，
> 此时无 `b64_json` 可解码，而官方响应根本没有 `url` 字段（Task 3）；
> ⑦`resolveOpenAIImageGeometry` **删除**「回落请求侧 size」这一级 ——
> adobe2api 有 `fallback_aspect_ratio`，请求尺寸 ≠ 出图尺寸，按它查表会计到别的格（Task 6）；
> ⑧上游聚合值兜底新增跨仓库前置条件 —— adobe2api 写死 `INPUT_IMAGE_TOKENS = 300`，
> 不先改就会在主用例上采信假值（Task 7 + 上线前置事项 0）。
>
> **v2.2 说明**：实现前复审后再收紧三点：
> ⑨响应侧也必须是 opaque/png，不能只检查请求参数；
> ⑩实际 `b64_json` 是尺寸与格式的唯一计费真源，顶层 `size` 只做一致性校验；
> ⑪所谓“54 格全实测”仅指横版/方图，其他竖版仍是对称性推定，文档不再声称全部 30 组尺寸均实测。

## Global Constraints

- 包名 `service`，全部新代码放在 `backend/internal/service/`。
- 四道闸门任一不过 → `applied=false`，响应体与 usage **逐字节不变**。
- 任何失败路径一律降级为透传，**不得让请求失败**。
- 不得触碰 `data[].b64_json` / `data[].url` 的内容。
- **不得覆盖用户显式指定的字段**（尤其 `background`）。
- 尺寸与格式以首张 `b64_json` 实际解码结果为真源；响应顶层 `size`
  若存在，必须与实际图片一致，否则降级。
- 响应侧 `background` 非 `opaque`、`output_format` 非 `png`、或实际编码非 PNG 时降级。
- 档位由**精确尺寸查表**得出，不做最近比例吸附。
- `quality` 取请求值，`auto`/缺省 → `low`（已实测：官方自身即回显 low）。
- 目标表述为「**结构与官方一致**」，不是「逐字节不可分辨」（`model` 字段保留，见设计文档已知差异 1）。
- 输出图 token 使用实测表；文本输入 token 若上游未提供，依次用
  `input_tokens - image_tokens` 和提示词粗估，这一部分只是近似值，必须在响应和计费中同源。
- 仓库**没有** `backend/internal/service/account_test.go`；账号相关单测统一放新建的
  `openai_images_usage_simulation_test.go`（highres 的同类测试在 `openai_images_highres_test.go`）。
- 2026-07-28 实现后的 backend 全量基线为 `go test ./...` 全部通过；验收不得新增失败。
- `docs/*` 在 `.gitignore` 中，文档改动需 `git add -f`。

## File Structure

| 文件 | 职责 |
|---|---|
| `backend/internal/service/openai_images_usage_simulation.go`（新建） | 四道闸门、三维 token 表、输入图 token、合成器、响应体改写、编排 |
| `backend/internal/service/openai_images_usage_simulation_test.go`（新建） | 全部单测 + 一致性不变量 + 账号标记测试 |
| `backend/internal/service/account.go`（修改） | 凭据标记常量 + `SupportsOpenAIImagesUsageSimulation()` |
| `backend/internal/service/openai_gateway_response_handling.go:747`（修改） | `openAIUsageFromGJSON` 补读 `input_tokens_details.image_tokens` |
| `backend/internal/service/openai_gateway_service_test.go`（修改） | 上述 usage 解析补丁单测（仓库无独立 response handling 测试文件） |
| `backend/internal/service/openai_images.go:891,728`（修改） | 接入点：改签名 + `c.Data` 前拦截 |
| `backend/internal/service/openai_images_test.go`（修改） | 集成测试 |

---

### Task 1: 修复 ImageInputTokens 从未被填充

独立缺陷，先行修复：`openAIUsageFromGJSON` 读了 output 侧的 image_tokens，漏读 input 侧，
导致所有改图请求的输入图像 token（$8/1M 档）从不计费，官方直连链路同样受影响。

**Files:**
- Modify: `backend/internal/service/openai_gateway_response_handling.go:747-772`
- Test: `backend/internal/service/openai_gateway_service_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `openAIUsageFromGJSON` 填充 `OpenAIUsage.ImageInputTokens`

- [ ] **Step 1: 写失败测试**

```go
func TestOpenAIUsageFromGJSONReadsImageInputTokens(t *testing.T) {
	body := `{"usage":{"input_tokens":1518,"input_tokens_details":{"image_tokens":1508,"text_tokens":10},"output_tokens":196,"output_tokens_details":{"image_tokens":196,"text_tokens":0},"total_tokens":1714}}`
	usage, ok := extractOpenAIUsageFromJSONBytes([]byte(body))
	if !ok {
		t.Fatalf("expected usage to be parsed")
	}
	if usage.ImageInputTokens != 1508 {
		t.Errorf("ImageInputTokens = %d, want 1508", usage.ImageInputTokens)
	}
	if usage.InputTokens != 1518 {
		t.Errorf("InputTokens = %d, want 1518", usage.InputTokens)
	}
	if usage.ImageOutputTokens != 196 {
		t.Errorf("ImageOutputTokens = %d, want 196", usage.ImageOutputTokens)
	}
}

func TestOpenAIUsageFromGJSONImageInputTokensAbsent(t *testing.T) {
	usage, ok := extractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":54,"output_tokens":229}}`))
	if !ok {
		t.Fatalf("expected usage to be parsed")
	}
	if usage.ImageInputTokens != 0 {
		t.Errorf("ImageInputTokens = %d, want 0", usage.ImageInputTokens)
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd backend && go test ./internal/service/ -run TestOpenAIUsageFromGJSONReadsImageInputTokens -v
```

预期：FAIL，`ImageInputTokens = 0, want 1508`。

- [ ] **Step 3: 实现**

在 `openAIUsageFromGJSON` 中 `imageOutputTokens` 那段之后插入：

```go
	imageInputTokens := value.Get("input_tokens_details.image_tokens").Int()
	if imageInputTokens == 0 {
		imageInputTokens = value.Get("prompt_tokens_details.image_tokens").Int()
	}
```

返回结构体增加一行 `ImageInputTokens: int(imageInputTokens),`。

- [ ] **Step 4: 运行确认通过**

```bash
cd backend && go test ./internal/service/ -run TestOpenAIUsageFromGJSON -v
```

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_gateway_response_handling.go \
        backend/internal/service/openai_gateway_service_test.go
git commit -m "fix(images): usage 解析补读 input_tokens_details.image_tokens"
```

---

### Task 2: 闸门 1 — 账号标记

**Files:**
- Modify: `backend/internal/service/account.go`（常量区 ~:93、方法区 ~:1449）
- Create: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Produces: `func (a *Account) SupportsOpenAIImagesUsageSimulation() bool`

- [ ] **Step 1: 写失败测试**

新建 `openai_images_usage_simulation_test.go`：

```go
package service

import "testing"

func TestSupportsOpenAIImagesUsageSimulation(t *testing.T) {
	cases := []struct {
		name  string
		creds map[string]any
		want  bool
	}{
		{"bool true", map[string]any{"openai_images_usage_simulation": true}, true},
		{"bool false", map[string]any{"openai_images_usage_simulation": false}, false},
		{"string true", map[string]any{"openai_images_usage_simulation": "true"}, true},
		{"string on", map[string]any{"openai_images_usage_simulation": "on"}, true},
		{"string no", map[string]any{"openai_images_usage_simulation": "no"}, false},
		{"number 1", map[string]any{"openai_images_usage_simulation": float64(1)}, true},
		{"number 0", map[string]any{"openai_images_usage_simulation": float64(0)}, false},
		{"absent", map[string]any{"other": true}, false},
		{"nil creds", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{Credentials: tc.creds}
			if got := a.SupportsOpenAIImagesUsageSimulation(); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
	var nilAccount *Account
	if nilAccount.SupportsOpenAIImagesUsageSimulation() {
		t.Errorf("nil account should return false")
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd backend && go test ./internal/service/ -run TestSupportsOpenAIImagesUsageSimulation -v
```

预期：编译失败 `a.SupportsOpenAIImagesUsageSimulation undefined`。

- [ ] **Step 3: 实现**

`account.go` 常量区，紧邻 `openAIImagesHighResCredentialKey` 之后：

```go
// openAIImagesUsageSimulationCredentialKey 标记账号的 images 响应需被改写成
// 官方 gpt-image-2 口径。白名单语义：仅显式开启时生效。
const openAIImagesUsageSimulationCredentialKey = "openai_images_usage_simulation"
```

把 `SupportsOpenAIImagesHighRes` 的解析体提取为共用私有方法（DRY）：

```go
func (a *Account) credentialFlag(key string) bool {
	if a == nil || a.Credentials == nil {
		return false
	}
	raw, found := a.Credentials[key]
	if !found || raw == nil {
		return false
	}
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1", "yes", "on", "enabled":
			return true
		}
		return false
	case float64:
		return value != 0
	case int:
		return value != 0
	default:
		return false
	}
}

func (a *Account) SupportsOpenAIImagesHighRes() bool {
	return a.credentialFlag(openAIImagesHighResCredentialKey)
}

// SupportsOpenAIImagesUsageSimulation 判断账号是否启用 images usage 模拟改写。
func (a *Account) SupportsOpenAIImagesUsageSimulation() bool {
	return a.credentialFlag(openAIImagesUsageSimulationCredentialKey)
}
```

- [ ] **Step 4: 运行确认通过（含既有 highres 测试不回归）**

```bash
cd backend && go test ./internal/service/ -run 'TestSupportsOpenAIImages' -v
```

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/account.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 新增账号级 usage 模拟开关凭据标记"
```

---

### Task 3: 闸门 2、3 — 模型白名单与请求能力门控

`isOpenAIImageGenerationModel`（`openai_images.go:457`）放行所有 `gpt-image-*` 前缀
**以及全部 Grok 生图模型**。账号一旦同时承载 `gpt-image-1` 或 `grok-imagine`，
会被错误套用 gpt-image-2 的表。请求侧同理：参考数据未覆盖的形态必须先降级。

**Files:**
- Create: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Produces:
  - `func isSimulatableOpenAIImagesModel(model string) bool`
  - `func openAIImagesRequestSimulatable(parsed *OpenAIImagesRequest) bool`

- [ ] **Step 1: 写失败测试**

```go
func TestIsSimulatableOpenAIImagesModel(t *testing.T) {
	cases := map[string]bool{
		"gpt-image-2":            true,
		"GPT-IMAGE-2":            true,
		"  gpt-image-2  ":        true,
		"gpt-image-2-2026-04-21": true,
		"gpt-image-1":            false,
		"gpt-image-1.5":          false,
		"gpt-image-3":            false,
		"gpt-image-2-codex":      false,
		"grok-imagine":           false,
		"grok-imagine-edit":      false,
		"":                       false,
	}
	for model, want := range cases {
		if got := isSimulatableOpenAIImagesModel(model); got != want {
			t.Errorf("isSimulatableOpenAIImagesModel(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestOpenAIImagesRequestSimulatable(t *testing.T) {
	clean := func() *OpenAIImagesRequest {
		return &OpenAIImagesRequest{N: 1}
	}
	if !openAIImagesRequestSimulatable(clean()) {
		t.Errorf("clean request should be simulatable")
	}
	if openAIImagesRequestSimulatable(nil) {
		t.Errorf("nil request should not be simulatable")
	}

	two := 2
	blocked := []struct {
		name  string
		mutate func(*OpenAIImagesRequest)
	}{
		{"stream", func(r *OpenAIImagesRequest) { r.Stream = true }},
		{"partial images", func(r *OpenAIImagesRequest) { r.PartialImages = &two }},
		{"n=0", func(r *OpenAIImagesRequest) { r.N = 0 }},
		{"n>1", func(r *OpenAIImagesRequest) { r.N = 2 }},
		{"mask", func(r *OpenAIImagesRequest) { r.HasMask = true }},
		{"transparent", func(r *OpenAIImagesRequest) { r.Background = "transparent" }},
		{"jpeg", func(r *OpenAIImagesRequest) { r.OutputFormat = "jpeg" }},
		{"compression", func(r *OpenAIImagesRequest) { r.OutputCompression = &two }},
		{"input fidelity", func(r *OpenAIImagesRequest) { r.InputFidelity = "high" }},
		// adobe2api 支持 response_format=url（generation.py:394,941），此时响应里没有
		// b64_json 可解码；而官方 gpt-image-2 的响应根本没有 url 字段，改写它自相矛盾。
		{"response_format url", func(r *OpenAIImagesRequest) { r.ResponseFormat = "url" }},
	}
	for _, tc := range blocked {
		t.Run(tc.name, func(t *testing.T) {
			r := clean()
			tc.mutate(r)
			if openAIImagesRequestSimulatable(r) {
				t.Errorf("%s should block simulation", tc.name)
			}
		})
	}

	// 显式写了官方默认值不应被挡
	allowed := clean()
	allowed.Background = "opaque"
	allowed.OutputFormat = "png"
	allowed.ResponseFormat = "b64_json"
	if !openAIImagesRequestSimulatable(allowed) {
		t.Errorf("explicit opaque/png/b64_json should still be simulatable")
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd backend && go test ./internal/service/ -run 'TestIsSimulatableOpenAIImagesModel|TestOpenAIImagesRequestSimulatable' -v
```

预期：编译失败，两个函数 undefined。

- [ ] **Step 3: 实现**

新建 `openai_images_usage_simulation.go`：

```go
package service

import "strings"

// isSimulatableOpenAIImagesModel 是闸门 2：显式白名单。
//
// 不能复用 isOpenAIImageGenerationModel —— 它放行所有 gpt-image-* 前缀以及
// 全部 Grok 生图模型，而本模块的 token 表只对 gpt-image-2 成立。
// 版本别名逐个确认、逐个列入，避免未来版本静默复用旧表。
func isSimulatableOpenAIImagesModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch normalized {
	case "gpt-image-2", "gpt-image-2-2026-04-21":
		return true
	default:
		return false
	}
}

// openAIImagesRequestSimulatable 是闸门 3：参考数据未覆盖的请求形态一律不模拟。
// 依据 docs/GPT_IMAGE_2_TOKEN_REFERENCE.md §12「未覆盖」清单。
func openAIImagesRequestSimulatable(parsed *OpenAIImagesRequest) bool {
	if parsed == nil {
		return false
	}
	if parsed.Stream || parsed.PartialImages != nil {
		return false // 流式与 partial images 的 token 规则未实测
	}
	if parsed.N != 1 {
		return false // 只验证过单图；异常值与多图均降级
	}
	if parsed.HasMask {
		return false // mask 作为额外输入图的计法未实测
	}
	if background := strings.ToLower(strings.TrimSpace(parsed.Background)); background != "" && background != "opaque" {
		return false // transparent 未实测；更不能把用户要的透明背景改写成不透明
	}
	if format := strings.ToLower(strings.TrimSpace(parsed.OutputFormat)); format != "" && format != "png" {
		return false // JPEG/WebP 未实测
	}
	if parsed.OutputCompression != nil || strings.TrimSpace(parsed.InputFidelity) != "" {
		return false
	}
	// response_format 必须显式挡在这里，不能指望 openAIImagesCapability
	// （openai_images.go:509）—— 那是路由能力判定，标记账号若也具备 native 能力仍会命中。
	// 挡它的理由：官方响应只有 b64_json、根本没有 url 字段；且 adobe2api 走 url 分支时
	// 响应里没有 b64_json，闸门 4 的尺寸来源与 output_format 都无从取得。
	if format := strings.ToLower(strings.TrimSpace(parsed.ResponseFormat)); format != "" && format != "b64_json" {
		return false
	}
	return true
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd backend && go test ./internal/service/ -run 'TestIsSimulatableOpenAIImagesModel|TestOpenAIImagesRequestSimulatable' -v
```

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 新增模拟的模型白名单与请求能力门控"
```

---

### Task 4: 输入图像 token 公式

**Files:**
- Modify: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Produces: `func openAIImageInputTokens(w, h int) int`

- [ ] **Step 1: 写失败测试**

```go
// 数据来源：docs/GPT_IMAGE_2_TOKEN_REFERENCE.md §6，官方直连与 codex 两条管线实测。
func TestOpenAIImageInputTokens(t *testing.T) {
	cases := []struct{ w, h, want int }{
		{256, 256, 256},
		{512, 512, 1024},
		{512, 1024, 512},
		{550, 368, 704},
		{768, 768, 1024},
		{1024, 1024, 1024},
		{1280, 720, 920},
		{1536, 1536, 1521},
		{2048, 1152, 1508},
		{2048, 2048, 1521},
		{3840, 2160, 1508},
	}
	for _, tc := range cases {
		if got := openAIImageInputTokens(tc.w, tc.h); got != tc.want {
			t.Errorf("openAIImageInputTokens(%d,%d) = %d, want %d", tc.w, tc.h, got, tc.want)
		}
	}
	for _, tc := range [][2]int{{0, 100}, {100, 0}, {-1, -1}} {
		if got := openAIImageInputTokens(tc[0], tc[1]); got != 0 {
			t.Errorf("openAIImageInputTokens(%d,%d) = %d, want 0", tc[0], tc[1], got)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd backend && go test ./internal/service/ -run TestOpenAIImageInputTokens -v
```

- [ ] **Step 3: 实现**

import 增补 `"math"`，追加：

```go
const (
	openAIImageInputPatchLimit     = 1536
	openAIImageInputUpscaleTarget  = 1024
)

// openAIImageInputTokens 按官方口径计算单张输入图的图像 token。
//
//	若 max(w,h) < 1024：按 min(2.0, 1024/max(w,h)) 放大
//	patches = ceil(w/32) * ceil(h/32)
//	若 patches > 1536：等比缩小直到 patches <= 1536
//
// 公式在官方直连与 codex 两条管线共 11 个实测点上精确吻合。
func openAIImageInputTokens(w, h int) int {
	if w <= 0 || h <= 0 {
		return 0
	}
	fw, fh := float64(w), float64(h)
	if longest := math.Max(fw, fh); longest < openAIImageInputUpscaleTarget {
		factor := math.Min(2.0, openAIImageInputUpscaleTarget/longest)
		fw *= factor
		fh *= factor
	}
	patches := imagePatchCount(fw, fh)
	if patches > openAIImageInputPatchLimit {
		factor := math.Sqrt(float64(openAIImageInputPatchLimit) / float64(patches))
		fw *= factor
		fh *= factor
		for imagePatchCount(fw, fh) > openAIImageInputPatchLimit {
			fw *= 0.99
			fh *= 0.99
		}
		patches = imagePatchCount(fw, fh)
	}
	return patches
}

func imagePatchCount(w, h float64) int {
	return int(math.Ceil(w/32)) * int(math.Ceil(h/32))
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd backend && go test ./internal/service/ -run TestOpenAIImageInputTokens -v
```

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 按官方口径实现输入图像 token 公式"
```

---

### Task 5: 三维 token 表、精确尺寸索引与 quality 归一

这是闸门 4 的数据基础。**不做最近比例吸附** —— 未知尺寸必须落到降级路径。

**Files:**
- Modify: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Produces:
  - `type openAIImageGeometry struct { Width, Height int; Ratio, Tier string }`
  - `func lookupOpenAIImageSize(w, h int) (openAIImageGeometry, bool)`
  - `func normalizeOpenAIImageQuality(raw string) (string, bool)`
  - `func openAIImageOutputTokens(ratio, tier, quality string) (int, bool)`

- [ ] **Step 1: 写失败测试**

```go
func TestLookupOpenAIImageSizeKnown(t *testing.T) {
	cases := []struct {
		w, h        int
		ratio, tier string
	}{
		{1024, 1024, "1:1", ImageBillingSize1K},
		{2048, 2048, "1:1", ImageBillingSize2K},
		{2880, 2880, "1:1", ImageBillingSize4K},
		{1120, 896, "5:4", ImageBillingSize1K},
		{896, 1120, "5:4", ImageBillingSize1K}, // 竖版归一到横版键
		{1152, 864, "4:3", ImageBillingSize1K},
		{864, 1152, "4:3", ImageBillingSize1K},
		{1248, 832, "3:2", ImageBillingSize1K},
		{832, 1248, "3:2", ImageBillingSize1K},
		{1280, 720, "16:9", ImageBillingSize1K},
		{720, 1280, "16:9", ImageBillingSize1K},
		{2560, 1440, "16:9", ImageBillingSize2K},
		{3840, 2160, "16:9", ImageBillingSize4K},
		{1456, 624, "21:9", ImageBillingSize1K},
		{3024, 1296, "21:9", ImageBillingSize2K}, // 长边 3024 却属 2K
		{3696, 1584, "21:9", ImageBillingSize4K},
	}
	for _, tc := range cases {
		geom, ok := lookupOpenAIImageSize(tc.w, tc.h)
		if !ok {
			t.Fatalf("lookupOpenAIImageSize(%d,%d) not found", tc.w, tc.h)
		}
		if geom.Ratio != tc.ratio || geom.Tier != tc.tier {
			t.Errorf("(%d,%d) = %q/%q, want %q/%q", tc.w, tc.h, geom.Ratio, geom.Tier, tc.ratio, tc.tier)
		}
		if geom.Width != tc.w || geom.Height != tc.h {
			t.Errorf("(%d,%d) dims not preserved: %dx%d", tc.w, tc.h, geom.Width, geom.Height)
		}
	}
}

// 未知尺寸必须降级，不得吸附到最近比例。
func TestLookupOpenAIImageSizeUnknown(t *testing.T) {
	for _, tc := range [][2]int{
		{1254, 1254}, // codex 出图，非 16 整除
		{1672, 941},  // codex 出图
		{1000, 100},  // 10:1 极端比例
		{1023, 1023}, // 接近 1024 但不等
		{624, 1456},  // 21:9 的竖版，官方比例列表中不存在
		{0, 0},
		{-1, 10},
	} {
		if _, ok := lookupOpenAIImageSize(tc[0], tc[1]); ok {
			t.Errorf("lookupOpenAIImageSize(%d,%d) should not be found", tc[0], tc[1])
		}
	}
}

func TestLookupOpenAIImageSizeCoversThirtyEntries(t *testing.T) {
	if got := len(openAIImageSizeIndex); got != 30 {
		t.Errorf("size index has %d entries, want 30", got)
	}
}

func TestNormalizeOpenAIImageQuality(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"", "low", true},       // 官方默认
		{"auto", "low", true},   // 实测：官方回显 low
		{"AUTO", "low", true},
		{"low", "low", true},
		{"medium", "medium", true},
		{"high", "high", true},
		{" High ", "high", true},
		{"hd", "", false},   // adobe2api 别名，非官方值
		{"4k", "", false},
		{"ultra", "", false},
	}
	for _, tc := range cases {
		got, ok := normalizeOpenAIImageQuality(tc.raw)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("normalizeOpenAIImageQuality(%q) = %q,%v want %q,%v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestOpenAIImageOutputTokensMeasuredCells(t *testing.T) {
	cases := []struct {
		ratio, tier, quality string
		want                 int
	}{
		{"1:1", ImageBillingSize1K, "low", 196},
		{"1:1", ImageBillingSize1K, "medium", 1756},
		{"1:1", ImageBillingSize1K, "high", 7024},
		{"1:1", ImageBillingSize2K, "low", 397},
		{"1:1", ImageBillingSize2K, "medium", 3568},
		{"5:4", ImageBillingSize1K, "medium", 1370},
		{"4:3", ImageBillingSize1K, "medium", 1294},
		{"3:2", ImageBillingSize1K, "medium", 1167},
		{"16:9", ImageBillingSize1K, "medium", 947},
		{"16:9", ImageBillingSize4K, "low", 371},
		{"16:9", ImageBillingSize4K, "medium", 3336},
		{"16:9", ImageBillingSize4K, "high", 13342},
		{"21:9", ImageBillingSize1K, "low", 82},
		{"21:9", ImageBillingSize1K, "medium", 733},
		{"21:9", ImageBillingSize1K, "high", 2863},
		// 以下为 2026-07-28 补测格，刻意选取与早期推导值不同者，防止回填抄错
		{"1:1", ImageBillingSize4K, "medium", 5930},  // 早期推导 5845
		{"1:1", ImageBillingSize4K, "high", 23719},   // 早期推导 23380
		{"5:4", ImageBillingSize1K, "high", 5551},    // 早期推导 5480
		{"3:2", ImageBillingSize2K, "medium", 2363},  // 早期推导 2404
		{"21:9", ImageBillingSize4K, "high", 7729},   // 早期推导 7804
	}
	for _, tc := range cases {
		got, ok := openAIImageOutputTokens(tc.ratio, tc.tier, tc.quality)
		if !ok || got != tc.want {
			t.Errorf("openAIImageOutputTokens(%q,%q,%q) = %d,%v want %d,true",
				tc.ratio, tc.tier, tc.quality, got, ok, tc.want)
		}
	}
}

func TestOpenAIImageOutputTokensUnknown(t *testing.T) {
	if _, ok := openAIImageOutputTokens("7:3", ImageBillingSize1K, "low"); ok {
		t.Errorf("unknown ratio should return false")
	}
	if _, ok := openAIImageOutputTokens("1:1", "8K", "low"); ok {
		t.Errorf("unknown tier should return false")
	}
	if _, ok := openAIImageOutputTokens("1:1", ImageBillingSize1K, "ultra"); ok {
		t.Errorf("unknown quality should return false")
	}
}
```

> 实际测试必须枚举参考文档 §4 的**全部 54 格**，并断言用例数为 54；
> 上面片段仅展示表驱动写法，不得只保留抽样格。

- [ ] **Step 2: 运行确认失败**

```bash
cd backend && go test ./internal/service/ -run 'TestLookupOpenAIImageSize|TestNormalizeOpenAIImageQuality|TestOpenAIImageOutputTokens' -v
```

- [ ] **Step 3: 实现**

import 增补 `"fmt"`，追加：

```go
// openAIImageTokenCell 是三维 token 表的一格：给定比例与尺寸档下的三个 quality 值。
//
// 数据来源 docs/GPT_IMAGE_2_TOKEN_REFERENCE.md §4：横版/方图 54 格全部实测；
// 竖版索引沿用转置后的比例/档位值，除 720x1280 low 外尚未逐格实测。
// 倍率不可用于外推：med/low 跨比例从 3:2 的 8.71 到 21:9 的 9.00，
// high/med 从 21:9 的 3.905 到 5:4 的 4.052；早期按全局倍率推导时
// 21:9 的 1K high 推得 2944 而实测 2863（−2.8%）。
type openAIImageTokenCell struct {
	Size   string
	Low    int
	Medium int
	High   int
}

// openAIImageTokenTable 键为 [归一后的横版比例][尺寸档]。
var openAIImageTokenTable = map[string]map[string]openAIImageTokenCell{
	"1:1": {
		ImageBillingSize1K: {"1024x1024", 196, 1756, 7024},
		ImageBillingSize2K: {"2048x2048", 397, 3568, 14272},
		ImageBillingSize4K: {"2880x2880", 659, 5930, 23719},
	},
	"5:4": {
		ImageBillingSize1K: {"1120x896", 157, 1370, 5551},
		ImageBillingSize2K: {"2240x1792", 313, 2743, 11115},
		ImageBillingSize4K: {"3200x2560", 530, 4648, 18835},
	},
	"4:3": {
		ImageBillingSize1K: {"1152x864", 144, 1294, 5176},
		ImageBillingSize2K: {"2304x1728", 288, 2584, 10336},
		ImageBillingSize4K: {"3264x2448", 480, 4316, 17264},
	},
	"3:2": {
		ImageBillingSize1K: {"1248x832", 134, 1167, 4667},
		ImageBillingSize2K: {"2496x1664", 271, 2363, 9452},
		ImageBillingSize4K: {"3504x2336", 449, 3912, 15645},
	},
	"16:9": {
		ImageBillingSize1K: {"1280x720", 106, 947, 3787},
		ImageBillingSize2K: {"2560x1440", 205, 1843, 7370},
		ImageBillingSize4K: {"3840x2160", 371, 3336, 13342},
	},
	"21:9": {
		ImageBillingSize1K: {"1456x624", 82, 733, 2863},
		ImageBillingSize2K: {"3024x1296", 166, 1492, 5825},
		ImageBillingSize4K: {"3696x1584", 220, 1980, 7729},
	},
}

// openAIImageGeometry 描述一次出图的几何信息。
type openAIImageGeometry struct {
	Width  int
	Height int
	Ratio  string // 归一后的横版比例键
	Tier   string // ImageBillingSize1K / 2K / 4K
}

// openAIImageSizeIndex 是 "WxH" -> 几何 的精确索引，共 30 项
// （6 个横版/方形比例 × 3 档 = 18，加 4 个有官方竖版对应的比例 × 3 档 = 12）。
// 21:9 在官方比例列表中没有竖版对应（无 9:21），故不加其转置。
// 注意：54 格 token 是对 18 个横版/方图尺寸的实测；除 720x1280 low 外，
// 竖版值沿用对应横版是对称性推定，不得在文档中标成竖版全实测。
var openAIImageSizeIndex = buildOpenAIImageSizeIndex()

func buildOpenAIImageSizeIndex() map[string]openAIImageGeometry {
	// 有官方竖版对应的比例；21:9 不在其列。
	transposable := map[string]bool{"5:4": true, "4:3": true, "3:2": true, "16:9": true}
	index := make(map[string]openAIImageGeometry, 30)
	for ratio, tiers := range openAIImageTokenTable {
		for tier, cell := range tiers {
		w, h, ok := parseOpenAIImageWidthHeight(cell.Size)
			if !ok {
				continue
			}
			index[fmt.Sprintf("%dx%d", w, h)] = openAIImageGeometry{Width: w, Height: h, Ratio: ratio, Tier: tier}
			if w != h && transposable[ratio] {
				index[fmt.Sprintf("%dx%d", h, w)] = openAIImageGeometry{Width: h, Height: w, Ratio: ratio, Tier: tier}
			}
		}
	}
	return index
}

// lookupOpenAIImageSize 是闸门 4：只认已知的 30 组精确尺寸。
// 刻意不做最近比例吸附 —— token 不随像素线性变化，吸附会产生错误计费。
func lookupOpenAIImageSize(w, h int) (openAIImageGeometry, bool) {
	if w <= 0 || h <= 0 {
		return openAIImageGeometry{}, false
	}
	geom, ok := openAIImageSizeIndex[fmt.Sprintf("%dx%d", w, h)]
	return geom, ok
}

// normalizeOpenAIImageQuality 把请求的 quality 归一为官方三档。
// 空值与 "auto" 均为 low —— 已实测：1024x1024 + quality:"auto" 返回 196 token
// 且响应回显 quality:"low"。非官方值（如 adobe2api 的 hd/4k/ultra 别名）返回 false，
// 由调用方降级，避免猜测计费。
func normalizeOpenAIImageQuality(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto", "low":
		return "low", true
	case "medium":
		return "medium", true
	case "high":
		return "high", true
	default:
		return "", false
	}
}

func openAIImageOutputTokens(ratio, tier, quality string) (int, bool) {
	tiers, ok := openAIImageTokenTable[ratio]
	if !ok {
		return 0, false
	}
	cell, ok := tiers[tier]
	if !ok {
		return 0, false
	}
	switch quality {
	case "low":
		return cell.Low, true
	case "medium":
		return cell.Medium, true
	case "high":
		return cell.High, true
	default:
		return 0, false
	}
}

// parseOpenAIImageWidthHeight 解析 "1024x1024" 形态；"auto" 等非尺寸值返回 ok=false。
func parseOpenAIImageWidthHeight(value string) (int, int, bool) {
	parts := strings.SplitN(strings.TrimSpace(strings.ToLower(value)), "x", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || w <= 0 {
		return 0, 0, false
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}
```

import 再增补 `"strconv"`。

- [ ] **Step 4: 运行确认通过**

```bash
cd backend && go test ./internal/service/ -run 'TestLookupOpenAIImageSize|TestNormalizeOpenAIImageQuality|TestOpenAIImageOutputTokens' -v
```

预期：全部 PASS，含 `TestLookupOpenAIImageSizeCoversThirtyEntries` 断言索引恰为 30 项。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 新增三维 token 表、30 组精确尺寸索引与 quality 归一"
```

---

### Task 6: 单次解码的图像元信息读取与几何解析

v1 对同一份 base64 解码两次（一次取宽高、一次取格式），4K 并发下内存翻倍。改为单次。

**Files:**
- Modify: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Produces:
  - `func decodeImageMeta(encoded string) (w, h int, format string, ok bool)`
  - `func resolveOpenAIImageGeometry(body []byte) (openAIImageGeometry, string, bool)`（第二个返回值为图像格式，空串表示未知；**不接受请求侧 size**，见下）

- [ ] **Step 1: 写失败测试**

测试文件顶部 import 增补 `"bytes"`、`"encoding/base64"`、`"image"`、`"image/color"`、`"image/png"`、`"github.com/tidwall/gjson"`。

```go
func pngBase64(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestDecodeImageMeta(t *testing.T) {
	w, h, format, ok := decodeImageMeta(pngBase64(t, 1280, 720))
	if !ok || w != 1280 || h != 720 || format != "png" {
		t.Errorf("decodeImageMeta = %d,%d,%q,%v want 1280,720,png,true", w, h, format, ok)
	}
	if _, _, _, ok := decodeImageMeta("not-base64!!!"); ok {
		t.Errorf("invalid base64 should return false")
	}
	if _, _, _, ok := decodeImageMeta(""); ok {
		t.Errorf("empty should return false")
	}
	if _, _, _, ok := decodeImageMeta(base64.StdEncoding.EncodeToString([]byte("hello"))); ok {
		t.Errorf("non-image bytes should return false")
	}
}

func TestResolveOpenAIImageGeometrySizeMatchesActualImage(t *testing.T) {
	body := []byte(`{"size":"2048x2048","data":[{"b64_json":"` + pngBase64(t, 2048, 2048) + `"}]}`)
	geom, format, ok := resolveOpenAIImageGeometry(body)
	if !ok || geom.Ratio != "1:1" || geom.Tier != ImageBillingSize2K || format != "png" {
		t.Fatalf("geom = %+v format=%q ok=%v", geom, format, ok)
	}
}

func TestResolveOpenAIImageGeometryRejectsSizeMismatchOrInvalidImage(t *testing.T) {
	mismatch := []byte(`{"size":"2048x2048","data":[{"b64_json":"` + pngBase64(t, 1024, 1024) + `"}]}`)
	if _, _, ok := resolveOpenAIImageGeometry(mismatch); ok {
		t.Errorf("declared size differing from actual image must degrade")
	}
	invalid := []byte(`{"size":"2048x2048","data":[{"b64_json":"aGk="}]}`)
	if _, _, ok := resolveOpenAIImageGeometry(invalid); ok {
		t.Errorf("declared size must not bypass b64 validation")
	}
}

func TestResolveOpenAIImageGeometryFromB64(t *testing.T) {
	body := []byte(`{"data":[{"b64_json":"` + pngBase64(t, 1280, 720) + `"}]}`)
	geom, format, ok := resolveOpenAIImageGeometry(body)
	if !ok || geom.Ratio != "16:9" || geom.Tier != ImageBillingSize1K {
		t.Fatalf("geom = %+v ok=%v", geom, ok)
	}
	if format != "png" {
		t.Errorf("format = %q, want png", format)
	}
}

// 只有 url 没有 b64_json 时必须降级，不得拿请求尺寸顶上：
// adobe2api 的 resolve_image_geometry 有 output_size 映射与 fallback_aspect_ratio，
// 请求尺寸 ≠ 出图尺寸，按它查表会计到另一格上去。
// （函数签名本身已不接受请求尺寸，这个用例守住的是「不要再把它加回来」。）
func TestResolveOpenAIImageGeometryURLOnlyBody(t *testing.T) {
	body := []byte(`{"data":[{"url":"https://example.com/a.png"}]}`)
	if _, _, ok := resolveOpenAIImageGeometry(body); ok {
		t.Errorf("url-only body must not resolve geometry")
	}
}

// 出图尺寸不在已知 30 组内时必须降级。
func TestResolveOpenAIImageGeometryUnknownSize(t *testing.T) {
	body := []byte(`{"size":"1672x941","data":[{"b64_json":"aGk="}]}`)
	if _, _, ok := resolveOpenAIImageGeometry(body); ok {
		t.Errorf("codex size 1672x941 should not resolve")
	}
	if _, _, ok := resolveOpenAIImageGeometry([]byte(`{"data":[]}`)); ok {
		t.Errorf("empty data should not resolve")
	}
	if _, _, ok := resolveOpenAIImageGeometry([]byte("not json")); ok {
		t.Errorf("invalid json should not resolve")
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd backend && go test ./internal/service/ -run 'TestDecodeImageMeta|TestResolveOpenAIImageGeometry' -v
```

- [ ] **Step 3: 实现**

生产文件 import 增补 `"encoding/base64"`、`"image"`、`_ "image/jpeg"`、`_ "image/png"`、
`"github.com/tidwall/gjson"`、`_ "golang.org/x/image/webp"`。

```go
// decodeImageMeta 用流式 base64 decoder 一次取出宽高与格式。
// image.DecodeConfig 只读图像头，不为整张 4K 图分配解码后的完整 raw buffer。
func decodeImageMeta(encoded string) (int, int, string, bool) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return 0, 0, "", false
	}
	if idx := strings.Index(encoded, ","); strings.HasPrefix(encoded, "data:") && idx > 0 {
		encoded = encoded[idx+1:]
	}
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
	cfg, format, err := image.DecodeConfig(decoder)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, "", false
	}
	return cfg.Width, cfg.Height, format, true
}

// resolveOpenAIImageGeometry 以首张 b64_json 实际图像头为唯一真源。
// 实际尺寸必须命中已知 30 组；顶层 size 存在时只做一致性校验。
//
// 刻意不接受请求侧 size：adobe2api 的 resolve_image_geometry 带 output_size 映射与
// fallback_aspect_ratio，请求的比例 Firefly 不支持时会落到别的比例，出图尺寸随之改变，
// 按请求尺寸查表会计到另一格上去。闸门 3 已挡掉 response_format=url，
// 所以 b64_json 必然存在；解码不出来就说明这个响应不是我们理解的形态，应当降级而不是猜。
func resolveOpenAIImageGeometry(body []byte) (openAIImageGeometry, string, bool) {
	if !gjson.ValidBytes(body) {
		return openAIImageGeometry{}, "", false
	}
	w, h, format, ok := decodeImageMeta(gjson.GetBytes(body, "data.0.b64_json").String())
	if !ok {
		return openAIImageGeometry{}, "", false
	}
	geom, found := lookupOpenAIImageSize(w, h)
	if !found {
		return openAIImageGeometry{}, "", false
	}
	if declared := gjson.GetBytes(body, "size"); declared.Exists() {
		dw, dh, parsed := parseOpenAIImageWidthHeight(declared.String())
		if !parsed || dw != w || dh != h {
			return openAIImageGeometry{}, "", false
		}
	}
	return geom, format, true
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd backend && go test ./internal/service/ -run 'TestDecodeImageMeta|TestResolveOpenAIImageGeometry' -v
```

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 出图几何解析改为精确尺寸匹配并单次解码"
```

---

### Task 7: 输入图 token 取数与远程 URL 兜底

`openai_images.go:283` 明确支持远程 `images[].image_url`，网关不下载这些图。
v1 直接跳过 http URL 再求和 → 远程改图的输入图 token 归 0。

> **⚠️ 本任务有跨仓库前置条件，先看「上线前置事项 0」再动手。**
> 兜底采信的是上游 `usage.input_tokens_details.image_tokens`。链路 A（官方直连）给的是真值；
> **链路 C（adobe2api）写死 `INPUT_IMAGE_TOKENS = 300`**（`core/models/resolver.py:33`），
> 与图像尺寸无关。而「标记账号 = adobe + 远程 image_url 改图」正是主用例，
> 必然走到这条兜底 —— 不先改 adobe2api，这里就是在用假值计费（1024×1024 少收 71%）。
> sub2api 侧无法在运行时分辨真假（响应体上没有可判别标记），故按前置条件处理，
> 本任务的代码逻辑不变。

**Files:**
- Modify: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Produces: `func resolveOpenAIImagesInputTokens(body []byte, parsed *OpenAIImagesRequest) (int, bool)`

- [ ] **Step 1: 写失败测试**

```go
func TestResolveOpenAIImagesInputTokensNoInputImages(t *testing.T) {
	parsed := &OpenAIImagesRequest{N: 1}
	got, ok := resolveOpenAIImagesInputTokens([]byte(`{"usage":{}}`), parsed)
	if !ok || got != 0 {
		t.Errorf("got %d,%v want 0,true", got, ok)
	}
}

func TestResolveOpenAIImagesInputTokensAllLocal(t *testing.T) {
	parsed := &OpenAIImagesRequest{
		N: 1,
		InputImageURLs: []string{
			"data:image/png;base64," + pngBase64(t, 1024, 1024),
			"data:image/png;base64," + pngBase64(t, 1280, 720),
		},
	}
	got, ok := resolveOpenAIImagesInputTokens([]byte(`{"usage":{}}`), parsed)
	if !ok || got != 1024+920 {
		t.Errorf("got %d,%v want %d,true", got, ok, 1024+920)
	}
}

// 含远程 URL 时改用上游聚合值，不能只把能解码的那几张求和。
func TestResolveOpenAIImagesInputTokensRemoteUsesUpstream(t *testing.T) {
	parsed := &OpenAIImagesRequest{
		N:              1,
		InputImageURLs: []string{"https://example.com/a.png", "data:image/png;base64," + pngBase64(t, 1024, 1024)},
	}
	body := []byte(`{"usage":{"input_tokens_details":{"image_tokens":2212}}}`)
	got, ok := resolveOpenAIImagesInputTokens(body, parsed)
	if !ok || got != 2212 {
		t.Errorf("got %d,%v want 2212,true", got, ok)
	}
}

// 含远程 URL 且上游没有可信值 → 整次放弃模拟。
func TestResolveOpenAIImagesInputTokensRemoteWithoutUpstream(t *testing.T) {
	parsed := &OpenAIImagesRequest{N: 1, InputImageURLs: []string{"https://example.com/a.png"}}
	if _, ok := resolveOpenAIImagesInputTokens([]byte(`{"usage":{}}`), parsed); ok {
		t.Errorf("should return false when upstream aggregate is missing")
	}
	if _, ok := resolveOpenAIImagesInputTokens([]byte(`{"usage":{"input_tokens_details":{"image_tokens":0}}}`), parsed); ok {
		t.Errorf("zero upstream aggregate is not trustworthy")
	}
}

func TestResolveOpenAIImagesInputTokensUploads(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(pngBase64(t, 2048, 2048))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	parsed := &OpenAIImagesRequest{N: 1, Uploads: []OpenAIImagesUpload{{Data: raw}}}
	got, ok := resolveOpenAIImagesInputTokens([]byte(`{"usage":{}}`), parsed)
	if !ok || got != 1521 {
		t.Errorf("got %d,%v want 1521,true", got, ok)
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd backend && go test ./internal/service/ -run TestResolveOpenAIImagesInputTokens -v
```

- [ ] **Step 3: 实现**

```go
// resolveOpenAIImagesInputTokens 计算输入图像 token 总数。
//
// 网关不下载远程图（openai_images.go:283 允许 images[].image_url 为 http(s)），
// 因此只要有任意一张尺寸取不到，就改用上游返回的聚合值；
// 上游也没有可信值时返回 false，由调用方整次放弃模拟 —— 宁可不模拟，
// 也不能把远程输入图当成 0 token 少收费。
func resolveOpenAIImagesInputTokens(body []byte, parsed *OpenAIImagesRequest) (int, bool) {
	if parsed == nil {
		return 0, false
	}
	total := 0
	unknown := false

	for _, imageURL := range parsed.InputImageURLs {
		w, h, _, ok := decodeImageMeta(imageURL)
		if !ok {
			unknown = true
			continue
		}
		total += openAIImageInputTokens(w, h)
	}
	for _, upload := range parsed.Uploads {
		// Uploads 的 Width/Height 来自 multipart 头部（parseOpenAIImageDimensions），
		// 客户端通常不带，故多为 0；非 0 时可省一次解码。
		if upload.Width > 0 && upload.Height > 0 {
			total += openAIImageInputTokens(upload.Width, upload.Height)
			continue
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(upload.Data))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			unknown = true
			continue
		}
		total += openAIImageInputTokens(cfg.Width, cfg.Height)
	}

	if !unknown {
		return total, true
	}
	// 前置条件：上游给的聚合值必须是真的。链路 A 成立；链路 C（adobe2api）在
	// INPUT_IMAGE_TOKENS = 300 未改成 patch 公式之前不成立 —— 见「上线前置事项 0」。
	// 这里只能挡掉「缺失 / 为 0」这种明显不可信的，分辨不了「是个假的正数」。
	upstream := int(gjson.GetBytes(body, "usage.input_tokens_details.image_tokens").Int())
	if upstream <= 0 {
		return 0, false
	}
	return upstream, true
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd backend && go test ./internal/service/ -run TestResolveOpenAIImagesInputTokens -v
```

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 输入图 token 支持远程 URL 的上游聚合值兜底"
```

---

### Task 8: usage 合成器

**Files:**
- Modify: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Produces:
  - `type openAIImagesSynthUsage struct { TextInputTokens, ImageInputTokens, ImageOutputTokens, InputTokens, OutputTokens, TotalTokens int }`
  - `func synthesizeOpenAIImagesUsage(geom openAIImageGeometry, quality string, textInputTokens, imageInputTokens int) (openAIImagesSynthUsage, bool)`
  - `func (s openAIImagesSynthUsage) toOpenAIUsage() OpenAIUsage`
  - `func estimateOpenAIImagePromptTokens(prompt string) int`

- [ ] **Step 1: 写失败测试**

```go
func TestSynthesizeOpenAIImagesUsage(t *testing.T) {
	geom := openAIImageGeometry{Width: 1024, Height: 1024, Ratio: "1:1", Tier: ImageBillingSize1K}

	s, ok := synthesizeOpenAIImagesUsage(geom, "low", 15, 0)
	if !ok {
		t.Fatalf("expected ok")
	}
	if s.ImageOutputTokens != 196 || s.OutputTokens != 196 {
		t.Errorf("output = %d/%d want 196/196", s.ImageOutputTokens, s.OutputTokens)
	}
	if s.InputTokens != 15 || s.TotalTokens != 211 {
		t.Errorf("input/total = %d/%d want 15/211", s.InputTokens, s.TotalTokens)
	}

	// quality 维度生效：同尺寸不同 quality 取不同格
	if m, _ := synthesizeOpenAIImagesUsage(geom, "medium", 15, 0); m.ImageOutputTokens != 1756 {
		t.Errorf("medium = %d, want 1756", m.ImageOutputTokens)
	}
	if h, _ := synthesizeOpenAIImagesUsage(geom, "high", 15, 0); h.ImageOutputTokens != 7024 {
		t.Errorf("high = %d, want 7024", h.ImageOutputTokens)
	}

	// 输入图 token 计入
	withInput, _ := synthesizeOpenAIImagesUsage(geom, "low", 10, 2212)
	if withInput.ImageInputTokens != 2212 || withInput.InputTokens != 2222 {
		t.Errorf("input = %d/%d want 2212/2222", withInput.ImageInputTokens, withInput.InputTokens)
	}
	if withInput.TotalTokens != withInput.InputTokens+withInput.OutputTokens {
		t.Errorf("total invariant broken")
	}

	// 未知几何 → false
	bad := openAIImageGeometry{Width: 10, Height: 10, Ratio: "7:3", Tier: ImageBillingSize1K}
	if _, ok := synthesizeOpenAIImagesUsage(bad, "low", 10, 0); ok {
		t.Errorf("unknown ratio should return false")
	}

	// 文本 token 下限
	floor, _ := synthesizeOpenAIImagesUsage(geom, "low", 0, 0)
	if floor.TextInputTokens != 1 {
		t.Errorf("TextInputTokens = %d, want floor 1", floor.TextInputTokens)
	}
}

func TestEstimateOpenAIImagePromptTokens(t *testing.T) {
	if got := estimateOpenAIImagePromptTokens("变成黑夜"); got != 4 {
		t.Errorf("CJK = %d, want 4", got)
	}
	if got := estimateOpenAIImagePromptTokens("make it night"); got != 3 {
		t.Errorf("ascii = %d, want 3", got)
	}
	if got := estimateOpenAIImagePromptTokens(""); got != 1 {
		t.Errorf("empty = %d, want floor 1", got)
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd backend && go test ./internal/service/ -run 'TestSynthesizeOpenAIImagesUsage|TestEstimateOpenAIImagePromptTokens' -v
```

- [ ] **Step 3: 实现**

```go
// openAIImagesSynthUsage 是一次请求合成出的 usage，
// 同时用于改写响应体与计费，保证两者口径一致。
type openAIImagesSynthUsage struct {
	TextInputTokens   int
	ImageInputTokens  int
	ImageOutputTokens int
	InputTokens       int
	OutputTokens      int
	TotalTokens       int
}

func synthesizeOpenAIImagesUsage(
	geom openAIImageGeometry,
	quality string,
	textInputTokens int,
	imageInputTokens int,
) (openAIImagesSynthUsage, bool) {
	outputTokens, ok := openAIImageOutputTokens(geom.Ratio, geom.Tier, quality)
	if !ok {
		return openAIImagesSynthUsage{}, false
	}
	if textInputTokens < 1 {
		textInputTokens = 1
	}
	if imageInputTokens < 0 {
		imageInputTokens = 0
	}
	s := openAIImagesSynthUsage{
		TextInputTokens:   textInputTokens,
		ImageInputTokens:  imageInputTokens,
		ImageOutputTokens: outputTokens,
	}
	s.InputTokens = s.TextInputTokens + s.ImageInputTokens
	s.OutputTokens = s.ImageOutputTokens
	s.TotalTokens = s.InputTokens + s.OutputTokens
	return s, true
}

func (s openAIImagesSynthUsage) toOpenAIUsage() OpenAIUsage {
	return OpenAIUsage{
		InputTokens:       s.InputTokens,
		ImageInputTokens:  s.ImageInputTokens,
		OutputTokens:      s.OutputTokens,
		ImageOutputTokens: s.ImageOutputTokens,
	}
}

// estimateOpenAIImagePromptTokens 在上游未回传文本 token 时粗估：
// CJK 按 1 token/字，其余按 4 字符/token，下限 1。
func estimateOpenAIImagePromptTokens(prompt string) int {
	cjk, other := 0, 0
	for _, r := range prompt {
		if (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3040 && r <= 0x30FF) || (r >= 0xAC00 && r <= 0xD7AF) {
			cjk++
			continue
		}
		other++
	}
	if tokens := cjk + other/4; tokens >= 1 {
		return tokens
	}
	return 1
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd backend && go test ./internal/service/ -run 'TestSynthesizeOpenAIImagesUsage|TestEstimateOpenAIImagePromptTokens' -v
```

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 新增带 quality 维度的 usage 合成器"
```

---

### Task 9: 响应体改写

**Files:**
- Modify: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Produces: `func rewriteOpenAIImagesResponseBody(body []byte, model, quality, format string, s openAIImagesSynthUsage, geom openAIImageGeometry) ([]byte, bool)`

- [ ] **Step 1: 写失败测试**

```go
func TestRewriteOpenAIImagesResponseBodyAdobe(t *testing.T) {
	b64 := pngBase64(t, 8, 8)
	body := []byte(`{"created":1,"model":"gpt-image-2","data":[{"b64_json":"` + b64 + `"}],` +
		`"usage":{"input_tokens":304,"output_tokens":400,"total_tokens":704}}`)
	geom := openAIImageGeometry{Width: 2048, Height: 2048, Ratio: "1:1", Tier: ImageBillingSize2K}
	s, _ := synthesizeOpenAIImagesUsage(geom, "low", 12, 0)

	out, ok := rewriteOpenAIImagesResponseBody(body, "gpt-image-2", "low", "png", s, geom)
	if !ok {
		t.Fatalf("expected ok")
	}
	checks := map[string]string{
		"background":    "opaque",
		"output_format": "png",
		"quality":       "low", // 回显请求归一值，不由尺寸档反推
		"size":          "2048x2048",
		"model":         "gpt-image-2",
	}
	for path, want := range checks {
		if got := gjson.GetBytes(out, path).String(); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if got := gjson.GetBytes(out, "usage.output_tokens_details.image_tokens").Int(); got != 397 {
		t.Errorf("image_tokens = %d, want 397", got)
	}
	if got := gjson.GetBytes(out, "usage.output_tokens_details.text_tokens").Int(); got != 0 {
		t.Errorf("output text_tokens = %d, want 0", got)
	}
	if gjson.GetBytes(out, "data.0.b64_json").String() != b64 {
		t.Errorf("b64_json was modified")
	}
}

// 上游已给出允许的 background 时必须沿用。
// transparent 不应调用到此函数，编排层的响应侧闸门会先降级。
func TestRewriteOpenAIImagesResponseBodyKeepsUpstreamBackground(t *testing.T) {
	body := []byte(`{"background":"opaque","data":[{"b64_json":"` + pngBase64(t, 8, 8) + `"}],"usage":{}}`)
	geom := openAIImageGeometry{Width: 1024, Height: 1024, Ratio: "1:1", Tier: ImageBillingSize1K}
	s, _ := synthesizeOpenAIImagesUsage(geom, "low", 5, 0)
	out, ok := rewriteOpenAIImagesResponseBody(body, "gpt-image-2", "low", "png", s, geom)
	if !ok {
		t.Fatalf("expected ok")
	}
	if got := gjson.GetBytes(out, "background").String(); got != "opaque" {
		t.Errorf("background = %q, want opaque", got)
	}
}

func TestRewriteOpenAIImagesResponseBodyStripsCodexFingerprint(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2-codex","quality":"auto",` +
		`"data":[{"b64_json":"` + pngBase64(t, 8, 8) + `","revised_prompt":"expanded"}],"usage":{}}`)
	geom := openAIImageGeometry{Width: 1280, Height: 720, Ratio: "16:9", Tier: ImageBillingSize1K}
	s, _ := synthesizeOpenAIImagesUsage(geom, "medium", 20, 0)

	out, ok := rewriteOpenAIImagesResponseBody(body, "gpt-image-2", "medium", "png", s, geom)
	if !ok {
		t.Fatalf("expected ok")
	}
	if got := gjson.GetBytes(out, "model").String(); got != "gpt-image-2" {
		t.Errorf("model = %q, want gpt-image-2", got)
	}
	if gjson.GetBytes(out, "data.0.revised_prompt").Exists() {
		t.Errorf("revised_prompt should be removed")
	}
	if got := gjson.GetBytes(out, "quality").String(); got != "medium" {
		t.Errorf("quality = %q, want medium", got)
	}
	if got := gjson.GetBytes(out, "size").String(); got != "1280x720" {
		t.Errorf("size = %q, want 1280x720", got)
	}
}

func TestRewriteOpenAIImagesResponseBodyInvalidJSON(t *testing.T) {
	geom := openAIImageGeometry{Width: 1024, Height: 1024, Ratio: "1:1", Tier: ImageBillingSize1K}
	s, _ := synthesizeOpenAIImagesUsage(geom, "low", 10, 0)
	if _, ok := rewriteOpenAIImagesResponseBody([]byte("not json"), "gpt-image-2", "low", "png", s, geom); ok {
		t.Errorf("invalid json should return ok=false")
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd backend && go test ./internal/service/ -run TestRewriteOpenAIImagesResponseBody -v
```

- [ ] **Step 3: 实现**

import 增补 `"github.com/tidwall/sjson"`。

```go
// rewriteOpenAIImagesResponseBody 用合成 usage 替换响应体，补齐官方字段、抹除上游指纹。
// 不触碰 data[].b64_json / data[].url 的内容。
//
// quality 写入的是**归一后的请求值**（与计费同源），不由尺寸档反推 ——
// 官方 quality 与 size 是正交维度。
func rewriteOpenAIImagesResponseBody(
	body []byte,
	model string,
	quality string,
	format string,
	s openAIImagesSynthUsage,
	geom openAIImageGeometry,
) ([]byte, bool) {
	if !gjson.ValidBytes(body) {
		return body, false
	}
	out := body
	set := func(path string, value any) bool {
		next, err := sjson.SetBytes(out, path, value)
		if err != nil {
			return false
		}
		out = next
		return true
	}

	usage := map[string]any{
		"input_tokens": s.InputTokens,
		"input_tokens_details": map[string]any{
			"image_tokens": s.ImageInputTokens,
			"text_tokens":  s.TextInputTokens,
		},
		"output_tokens": s.OutputTokens,
		"output_tokens_details": map[string]any{
			"image_tokens": s.ImageOutputTokens,
			"text_tokens":  0,
		},
		"total_tokens": s.TotalTokens,
	}
	if !set("usage", usage) {
		return body, false
	}

	// 上游已给 background 就沿用；绝不覆盖用户/上游的显式取值。
	if !gjson.GetBytes(out, "background").Exists() {
		if !set("background", "opaque") {
			return body, false
		}
	}
	if !gjson.GetBytes(out, "output_format").Exists() {
		resolved := strings.TrimSpace(format)
		if resolved == "" {
			resolved = "png"
		}
		if !set("output_format", resolved) {
			return body, false
		}
	}
	if !set("quality", quality) {
		return body, false
	}
	if !set("size", fmt.Sprintf("%dx%d", geom.Width, geom.Height)) {
		return body, false
	}
	if trimmed := strings.TrimSpace(model); trimmed != "" {
		if !set("model", trimmed) {
			return body, false
		}
	}

	for i := range gjson.GetBytes(out, "data").Array() {
		path := fmt.Sprintf("data.%d.revised_prompt", i)
		if !gjson.GetBytes(out, path).Exists() {
			continue
		}
		next, err := sjson.DeleteBytes(out, path)
		if err != nil {
			return body, false
		}
		out = next
	}
	return out, true
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd backend && go test ./internal/service/ -run TestRewriteOpenAIImagesResponseBody -v
```

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 响应体改写为官方结构并回显请求 quality"
```

---

### Task 10: 编排、四道闸门与一致性不变量

**Files:**
- Modify: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Produces:
  - `func openAIImagesResponseSimulatable(body []byte, actualFormat, expectedQuality string) bool`
  - `func applyOpenAIImagesUsageSimulation(body []byte, parsed *OpenAIImagesRequest) ([]byte, OpenAIUsage, bool)`
  - `func maybeSimulateOpenAIImagesUsage(body []byte, account *Account, parsed *OpenAIImagesRequest, effectiveUpstreamModel string) ([]byte, OpenAIUsage, bool)`

- [ ] **Step 1: 写失败测试**

```go
// 不变量：改写后的 body 再解析出的 usage，必须与用于计费的 usage 完全一致。
func TestApplyOpenAIImagesUsageSimulationConsistency(t *testing.T) {
	body := []byte(`{"created":1,"model":"gpt-image-2","data":[{"b64_json":"` + pngBase64(t, 2048, 2048) + `"}],` +
		`"usage":{"input_tokens":304,"output_tokens":400,"total_tokens":704}}`)
	parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "a plain blue circle", Size: "2048x2048", N: 1}

	out, usage, ok := applyOpenAIImagesUsageSimulation(body, parsed)
	if !ok {
		t.Fatalf("expected applied")
	}
	parsedUsage, parsedOK := extractOpenAIUsageFromJSONBytes(out)
	if !parsedOK {
		t.Fatalf("rewritten body has no parsable usage")
	}
	if parsedUsage != usage {
		t.Errorf("usage mismatch:\n body   = %+v\n billed = %+v", parsedUsage, usage)
	}
	// 不传 quality → low → 2K 1:1 = 397（与官方一致，而非早期草案的 3568）
	if usage.ImageOutputTokens != 397 {
		t.Errorf("ImageOutputTokens = %d, want 397", usage.ImageOutputTokens)
	}
}

func TestApplyOpenAIImagesUsageSimulationQualityDimension(t *testing.T) {
	body := []byte(`{"data":[{"b64_json":"` + pngBase64(t, 2048, 2048) + `"}],"usage":{}}`)
	for _, tc := range []struct {
		quality string
		want    int
	}{{"", 397}, {"auto", 397}, {"low", 397}, {"medium", 3568}} {
		parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "x", Size: "2048x2048", N: 1, Quality: tc.quality}
		_, usage, ok := applyOpenAIImagesUsageSimulation(body, parsed)
		if !ok || usage.ImageOutputTokens != tc.want {
			t.Errorf("quality=%q -> %d,%v want %d,true", tc.quality, usage.ImageOutputTokens, ok, tc.want)
		}
	}
	// 非官方 quality 值 → 降级
	parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "x", Size: "2048x2048", N: 1, Quality: "hd"}
	if _, _, ok := applyOpenAIImagesUsageSimulation(body, parsed); ok {
		t.Errorf("non-official quality should degrade")
	}
}

func TestApplyOpenAIImagesUsageSimulationRejectsUnsupportedResponseMetadata(t *testing.T) {
	parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "x", N: 1}
	for name, body := range map[string]string{
		"transparent response": `{"background":"transparent","data":[{"b64_json":"` + pngBase64(t, 1024, 1024) + `"}],"usage":{}}`,
		"webp response field": `{"output_format":"webp","data":[{"b64_json":"` + pngBase64(t, 1024, 1024) + `"}],"usage":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			out, _, ok := applyOpenAIImagesUsageSimulation([]byte(body), parsed)
			if ok || string(out) != body {
				t.Errorf("unsupported response metadata must pass through")
			}
		})
	}
}

func TestMaybeSimulateOpenAIImagesUsageGates(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","data":[{"b64_json":"` + pngBase64(t, 1024, 1024) + `"}],"usage":{}}`)
	marked := &Account{Credentials: map[string]any{"openai_images_usage_simulation": true}}
	unmarked := &Account{Credentials: map[string]any{}}
	clean := func() *OpenAIImagesRequest {
		return &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "x", Size: "1024x1024", N: 1}
	}

	if _, usage, ok := maybeSimulateOpenAIImagesUsage(body, marked, clean(), "gpt-image-2"); !ok || usage.ImageOutputTokens != 196 {
		t.Errorf("marked account should simulate, got %d,%v", usage.ImageOutputTokens, ok)
	}

	gates := []struct {
		name    string
		account *Account
		parsed  *OpenAIImagesRequest
	}{
		{"闸门1 未标记账号", unmarked, clean()},
		{"闸门2 非白名单模型", marked, &OpenAIImagesRequest{Model: "gpt-image-1", Prompt: "x", Size: "1024x1024", N: 1}},
		{"闸门2 grok", marked, &OpenAIImagesRequest{Model: "grok-imagine", Prompt: "x", Size: "1024x1024", N: 1}},
		{"闸门3 n>1", marked, &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "x", Size: "1024x1024", N: 2}},
		{"闸门3 transparent", marked, &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "x", Size: "1024x1024", N: 1, Background: "transparent"}},
		{"闸门4 未知尺寸", marked, &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "x", Size: "1254x1254", N: 1}},
	}
	for _, tc := range gates {
		t.Run(tc.name, func(t *testing.T) {
			unknownBody := []byte(`{"size":"1254x1254","data":[{"url":"x"}],"usage":{}}`)
			in := body
			if strings.Contains(tc.name, "闸门4") {
				in = unknownBody
			}
			out, _, ok := maybeSimulateOpenAIImagesUsage(in, tc.account, tc.parsed, tc.parsed.Model)
			if ok {
				t.Errorf("%s should not simulate", tc.name)
			}
			if string(out) != string(in) {
				t.Errorf("%s: body must be untouched", tc.name)
			}
		})
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd backend && go test ./internal/service/ -run 'TestApplyOpenAIImagesUsageSimulation|TestMaybeSimulateOpenAIImagesUsage' -v
```

- [ ] **Step 3: 实现**

```go
// openAIImagesResponseSimulatable 守住上游实际响应边界。
// 缺失元数据可由改写器补齐，但显式不支持的值和实际非 PNG 编码必须降级。
func openAIImagesResponseSimulatable(body []byte, actualFormat, expectedQuality string) bool {
	if !strings.EqualFold(strings.TrimSpace(actualFormat), "png") {
		return false
	}
	if background := gjson.GetBytes(body, "background"); background.Exists() &&
		!strings.EqualFold(strings.TrimSpace(background.String()), "opaque") {
		return false
	}
	if outputFormat := gjson.GetBytes(body, "output_format"); outputFormat.Exists() &&
		!strings.EqualFold(strings.TrimSpace(outputFormat.String()), "png") {
		return false
	}
	if quality := gjson.GetBytes(body, "quality"); quality.Exists() &&
		!strings.EqualFold(strings.TrimSpace(quality.String()), expectedQuality) {
		return false
	}
	if model := gjson.GetBytes(body, "model"); model.Exists() && !isSimulatableOpenAIImagesModel(model.String()) {
		return false
	}
	if usage := gjson.GetBytes(body, "usage"); usage.Exists() &&
		(openAICacheReadTokensFromUsage(usage) > 0 || openAICacheCreationTokensFromUsage(usage) > 0) {
		return false // 缓存图像输入的 $2/1M 口径未验证
	}
	return true
}

// applyOpenAIImagesUsageSimulation 串联闸门 3/4 与合成、改写。
// 任一环节失败返回原 body 与 applied=false，调用方沿用原 usage。
func applyOpenAIImagesUsageSimulation(
	body []byte,
	parsed *OpenAIImagesRequest,
) ([]byte, OpenAIUsage, bool) {
	if len(body) == 0 || !openAIImagesRequestSimulatable(parsed) {
		return body, OpenAIUsage{}, false
	}
	quality, ok := normalizeOpenAIImageQuality(parsed.Quality)
	if !ok {
		return body, OpenAIUsage{}, false
	}
	geom, format, ok := resolveOpenAIImageGeometry(body)
	if !ok {
		return body, OpenAIUsage{}, false
	}
	// 请求闸门不能代替响应闸门：上游可能返回与请求不同的背景/格式。
	if !openAIImagesResponseSimulatable(body, format, quality) {
		return body, OpenAIUsage{}, false
	}
	// 闸门 3 已要求 n==1；此处再确认响应确实只含一张图。
	if len(gjson.GetBytes(body, "data").Array()) != 1 {
		return body, OpenAIUsage{}, false
	}

	imageInput, ok := resolveOpenAIImagesInputTokens(body, parsed)
	if !ok {
		return body, OpenAIUsage{}, false
	}
	textTokens := int(gjson.GetBytes(body, "usage.input_tokens_details.text_tokens").Int())
	if textTokens <= 0 {
		if aggregate := int(gjson.GetBytes(body, "usage.input_tokens").Int()); aggregate > imageInput {
			textTokens = aggregate - imageInput
		}
	}
	if textTokens <= 0 {
		textTokens = estimateOpenAIImagePromptTokens(parsed.Prompt) // 最后兜底，只是近似值
	}

	synth, ok := synthesizeOpenAIImagesUsage(geom, quality, textTokens, imageInput)
	if !ok {
		return body, OpenAIUsage{}, false
	}
	newBody, ok := rewriteOpenAIImagesResponseBody(body, parsed.Model, quality, format, synth, geom)
	if !ok {
		return body, OpenAIUsage{}, false
	}
	return newBody, synth.toOpenAIUsage(), true
}

// maybeSimulateOpenAIImagesUsage 是对外入口，先过闸门 1（账号标记）与闸门 2（模型白名单）。
func maybeSimulateOpenAIImagesUsage(
	body []byte,
	account *Account,
	parsed *OpenAIImagesRequest,
	effectiveUpstreamModel string,
) ([]byte, OpenAIUsage, bool) {
	if !account.SupportsOpenAIImagesUsageSimulation() {
		return body, OpenAIUsage{}, false
	}
	if parsed == nil || !isSimulatableOpenAIImagesModel(parsed.Model) ||
		!isSimulatableOpenAIImagesModel(effectiveUpstreamModel) {
		return body, OpenAIUsage{}, false
	}
	return applyOpenAIImagesUsageSimulation(body, parsed)
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd backend && go test ./internal/service/ -run 'TestApplyOpenAIImagesUsageSimulation|TestMaybeSimulateOpenAIImagesUsage' -v
```

预期：全部 PASS，尤其是一致性不变量与六项闸门降级。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 新增四道闸门编排与响应体/计费一致性保证"
```

---

### Task 11: 接入转发链路与真实集成测试

**Files:**
- Modify: `backend/internal/service/openai_images.go:891`（`handleOpenAIImagesNonStreamingResponse`）
- Modify: `backend/internal/service/openai_images.go:728`（唯一调用点）
- Modify: `backend/internal/service/openai_gateway_service.go`（转发结果携带模拟标记）
- Modify: `backend/internal/service/openai_gateway_usage.go`（模拟结果强制走 token 计费）
- Modify: `backend/internal/service/pricing_service.go`、`billing_service.go`（传递图片输入 token 单价）
- Test: `backend/internal/service/openai_images_test.go`

**Interfaces:**
- Consumes: `maybeSimulateOpenAIImagesUsage`（Task 10）

- [ ] **Step 1: 写失败集成测试**

追加到 `backend/internal/service/openai_images_test.go`，必须复用既有
`httpUpstreamRecorder + gin.CreateTestContext + ForwardImages` 夹具，真正经过
`handleOpenAIImagesNonStreamingResponse`。**不得**直接调用
`maybeSimulateOpenAIImagesUsage`，否则测试在接入点修改前就会通过。

```go
func TestOpenAIGatewayServiceForwardImages_APIKeySimulationIntegration(t *testing.T) {
	// 按本文件既有 APIKey ForwardImages 用例构造真实 HTTP 请求、
	// httpUpstreamRecorder 响应和带模拟标记的 APIKey 账号。
	// 断言：
	// 1. rec.Body 已被改写且 b64_json 不变；
	// 2. result.Usage.ImageOutputTokens 与 rec.Body.usage 同为 196；
	// 3. 去掉账号标记后 rec.Body 与上游原文逐字节一致。
}

// 远程 images[].image_url 的 edits：输入图 token 必须走上游聚合值，不能为 0
func TestForwardOpenAIImagesSimulationRemoteEdits(t *testing.T) {
	raw := `{"model":"gpt-image-2","data":[{"b64_json":"` + pngBase64(t, 1024, 1024) + `"}],` +
		`"usage":{"input_tokens":1518,"input_tokens_details":{"image_tokens":1508,"text_tokens":10},` +
		`"output_tokens":196,"output_tokens_details":{"image_tokens":196,"text_tokens":0},"total_tokens":1714}}`
	account := &Account{Credentials: map[string]any{"openai_images_usage_simulation": true}}
	parsed := &OpenAIImagesRequest{
		Model: "gpt-image-2", Prompt: "make it night", Size: "1024x1024", N: 1,
		Endpoint:       openAIImagesEditsEndpoint,
		InputImageURLs: []string{"https://example.com/a.png"},
	}
	// 同样必须通过 ForwardImages，断言 result.Usage 和 rec.Body.usage 都为 1508。
}

// response_format=url：adobe2api 只回 data[].url，没有 b64_json；
// 官方响应也没有 url 字段。整条必须原样透传。
func TestForwardOpenAIImagesSimulationResponseFormatURL(t *testing.T) {
	raw := `{"model":"gpt-image-2","data":[{"url":"http://adobe2api:6001/generated/x.png"}],` +
		`"usage":{"input_tokens":10,"output_tokens":1056,"total_tokens":1066}}`
	account := &Account{Credentials: map[string]any{"openai_images_usage_simulation": true}}
	parsed := &OpenAIImagesRequest{
		Model: "gpt-image-2", Prompt: "night", Size: "1024x1024", N: 1,
		ResponseFormat: "url",
		Endpoint:       openAIImagesGenerationsEndpoint,
	}
	// 同样必须通过 ForwardImages，断言 rec.Body 与 raw 逐字节一致。
}

// 必须串起 ForwardImages -> RecordUsage；只验证响应 usage 不足以证明实际按 token 扣费。
func TestForwardOpenAIImagesSimulation_RecordUsageBillsSynthesizedTokens(t *testing.T) {
	// 远程改图：text=10、image input=1508、image output=196。
	// 断言 billing_mode=token，费用分别按 $5/$8/$30 per MTok 计算，
	// total_cost = 0.017994，不能落回默认按张价格。
}

// SSE 编辑响应也要保留解析出的 image input token。
func TestOpenAIGatewayServiceForwardImages_APIKeyStreamingEditMergesImageInputTokens(t *testing.T) {
	// 上游 SSE usage.input_tokens_details.image_tokens=1508；
	// 断言 result.Usage.ImageInputTokens=1508。
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd backend && go test ./internal/service/ -run TestForwardOpenAIImagesSimulation -v
```

预期：FAIL。Task 10 即使已通过，这些用例仍应因真实转发链路尚未接入而失败。

- [ ] **Step 3: 实现接入点**

`openai_images.go` 中把 `handleOpenAIImagesNonStreamingResponse` 改为：

```go
func (s *OpenAIGatewayService) handleOpenAIImagesNonStreamingResponse(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	parsed *OpenAIImagesRequest,
	effectiveUpstreamModel string,
) (OpenAIUsage, int, []string, bool, error) {
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return OpenAIUsage{}, 0, nil, false, err
	}
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	contentType := "application/json"
	if s.cfg != nil && !s.cfg.Security.ResponseHeaders.Enabled {
		if upstreamType := resp.Header.Get("Content-Type"); upstreamType != "" {
			contentType = upstreamType
		}
	}

	// 模拟改写必须在写出之前，且响应体与计费 usage 同源。
	simulatedBody, simulatedUsage, simulated := maybeSimulateOpenAIImagesUsage(body, account, parsed, effectiveUpstreamModel)
	if simulated {
		body = simulatedBody
	}

	c.Data(resp.StatusCode, contentType, body)

	usage := simulatedUsage
	if !simulated {
		usage, _ = extractOpenAIUsageFromJSONBytes(body)
	}
	return usage, extractOpenAIImageCountFromJSONBytes(body), collectOpenAIResponseImageOutputSizesFromJSONBytes(body), simulated, nil
}
```

唯一调用点（`openai_images.go:728`）补两个实参：

```go
		nonStreamUsage, nonStreamCount, nonStreamSizes, usageSimulated, err := s.handleOpenAIImagesNonStreamingResponse(resp, c, account, parsed, upstreamModel)
		// OpenAIForwardResult.ImageUsageSimulated = usageSimulated
```

- [ ] **Step 4: 串通 token 计费与图片输入单价**

仅改写响应不够：`RecordUsage` 遇到 `ImageCount > 0` 默认会走按张计费。
给 `OpenAIForwardResult` 增加内部字段 `ImageUsageSimulated bool`，且仅在模拟实际成功时置为 true；
`calculateOpenAIRecordUsageCost` 对该标记跳过图片按张分支，进入 token 分支。
未模拟的普通图片请求继续保持原行为。

同时把价格 JSON 已有的 `input_cost_per_image_token` 串过
`LiteLLMRawEntry -> LiteLLMModelPricing -> ModelPricing.ImageInputPricePerToken`，否则图片输入会误用文本输入价。
`mergeOpenAIUsage` 也必须合并 `ImageInputTokens`，避免流式编辑响应丢失该字段。

- [ ] **Step 5: 运行确认通过**

```bash
cd backend && go test ./internal/service/ -run TestForwardOpenAIImagesSimulation -v
```

- [ ] **Step 6: 全量回归**

```bash
cd backend && go build ./... && go vet ./internal/service/
cd backend && go test ./internal/service/ ./internal/handler/ 2>&1 | tail -30
```

预期：编译与 vet 通过；失败用例与改动前基线一致，无新增失败。

- [ ] **Step 7: 提交**

```bash
git add backend/internal/service/openai_images.go \
        backend/internal/service/openai_images_test.go \
        backend/internal/service/openai_gateway_service.go \
        backend/internal/service/openai_gateway_usage.go \
        backend/internal/service/pricing_service.go \
        backend/internal/service/pricing_service_test.go \
        backend/internal/service/billing_service.go
git commit -m "feat(images): 转发链路接入 usage 模拟（四道闸门门控）"
```

---

## 上线前置事项

### 0. 【阻塞】改掉 adobe2api 写死的 `INPUT_IMAGE_TOKENS = 300`

**不做这项就不要给 adobe 账号打标记**，否则主用例上的输入图 token 是假的。

- 位置：`adobe2api/core/models/resolver.py:33`，`build_image_usage` 里 `img_in = 张数 × 300`。
- 改法：换成本方案 Task 4 的 patch 公式（adobe2api 手里有图片字节，能精确算）：
  `max(w,h) < 1024` 时按 `min(2.0, 1024/max(w,h))` 放大 → `ceil(w/32) * ceil(h/32)`
  → 超过 1536 patch 则等比缩小。
- 配套：`tests/test_images_edits.py:157,183,539` 三处断言 `== 300 / 600` 需同步改成按尺寸算的期望值。
- 验收：同一张 1024×1024 输入图，`input_tokens_details.image_tokens` 应为 **1024**（不再是 300）；
  换成 3840×2160 应为 **1508**。

顺带（不阻塞本方案）：`_GPT_IMAGE_OUTPUT_TOKENS` 仍是 gpt-image-1 的 3×3 表、
`_RES_TO_QUALITY` 仍把 1K/2K/4K 当 low/medium/high。打了标记的账号会被 sub2api 整体覆盖，
但**未打标记的账号照旧按错口径计费**。是否一并换成 18 格表 × 质量倍率，
以及倍率选哪一档（4K 16:9 单图 low $0.011 / medium $0.100 / high $0.400），是待拍板的定价决策。

### 1. 横版/方图 token 表已全量实测，竖版仍需确认

54 格（6 比例 × 3 尺寸档 × 3 quality）已于 2026-07-28 对横版/方图全部实测。
索引中另外 12 个竖版尺寸仍沿用对应横版值；目前只有 `720x1280` low
直接验证过对称性。上线前至少覆盖 4 组竖版比例、3 个尺寸档和 3 个 quality
做交叉抽测；若要宣称竖版也是“全实测”，则需补齐剩余 36 格。

### 2. 决定 codex 账号（1118）是否打标记

闸门 4 会因其出图尺寸（`1672x941`、`1254x1254` 等）不在已知 30 组内而直接降级，
**即使打了标记也基本不会被模拟**。这是预期行为。默认建议：只给 adobe2api 账号（1115）打标记。

### 3. 灰度

在目标账号 credentials 加 `"openai_images_usage_simulation": true`，
先观察 `usage_logs.image_output_tokens` 与 `total_cost` 是否符合三维表，再全量。

灰度期必查一项：拿两张尺寸差别大的远程图各打一次改图，
结构化日志事件 `openai_images.usage_simulated` 的 `image_input_tokens` 必须**跟着尺寸变**。
`usage_logs` 当前没有独立的 `image_input_tokens` 列。若两次都等于 `张数 × 300`，
说明前置事项 0 没生效，此时的输入图计费是假的，应立刻摘掉标记。

## 验收标准

- 打标记账号的响应 `usage` 结构与官方一致；`background`/`output_format`/`quality`/`size` 齐备；
  `revised_prompt` 与 `gpt-image-2-codex` 不再出现。
- `quality` 回显等于请求归一值；**只传 `size=2048x2048` 不传 quality 时按 low 计费（397 token）**，
  与官方一致。
- `usage_logs.image_output_tokens` 等于三维表对应格；
  `usage_logs.billing_mode = token`；
  `total_cost = image_output × 30/1M + text_input × 5/1M + image_input × 8/1M`。
- 远程 URL 改图在 `openai_images.usage_simulated` 日志中的 `image_input_tokens`
  非 0（走上游聚合值），**且随输入图尺寸变化**
  —— 恒为 `张数 × 300` 说明上线前置事项 0 未完成。
- `response_format=url` 的请求不被模拟，`data[].url` 原样透传。
- 四道闸门任一不满足时响应体与 usage 逐字节不变。
- `go build ./...`、`go vet ./internal/service/` 通过；测试无新增失败。
