# gpt-image-2 usage 模拟与响应对齐 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让被标记账号返回的 `/v1/images/*` 响应在 usage 与响应体字段上与官方 gpt-image-2 完全一致，并按实测 token 表计费。

**Architecture:** 新增一个纯函数模块，把「几何归一 → 查 token 表 → 合成 usage → 改写响应体」串成一条链。合成出的同一份数据既写入响应体又作为计费 usage 返回，两者口径由单测守住不变量。唯一接入点是 `handleOpenAIImagesNonStreamingResponse`，在 `c.Data` 写出前拦截。

**Tech Stack:** Go；`github.com/tidwall/gjson`（读）+ `github.com/tidwall/sjson`（写）；`image.DecodeConfig`（只读图像头取宽高）；标准库 `testing`（表驱动，与 `gemini_pro_image_mask_test.go` 同风格，不引入 testify）。

设计文档：`docs/superpowers/specs/2026-07-28-gpt-image-2-usage-simulation-design.md`
数据来源：`docs/GPT_IMAGE_2_TOKEN_REFERENCE.md`

## Global Constraints

- 包名 `service`，全部新代码放在 `backend/internal/service/`。
- 未打标记的账号必须**零行为变化**（响应体逐字节一致、usage 一致）。
- 任何失败路径一律降级为「原样透传 + 原 usage」，**不得让请求失败**。
- 不得触碰 `data[].b64_json` / `data[].url` 的内容。
- 计费档位映射为**自定义**：1K→low、2K→medium、4K→high（官方 quality 与 size 本是正交维度），实现中必须写明该偏离。
- 档位判定按**总像素**，不得按最长边（21:9 的 2K 是 3024×1296，长边会误判）。
- dev 分支存在 2 个预先存在的失败用例，验收以「与基线对比无新增失败」为准，而非绝对全绿。
- `docs/*` 在 `.gitignore` 中，文档类改动需 `git add -f`。

## File Structure

| 文件 | 职责 |
|---|---|
| `backend/internal/service/openai_images_usage_simulation.go`（新建） | 几何归一、token 表、输入图 token 公式、合成器、响应体改写、编排入口 |
| `backend/internal/service/openai_images_usage_simulation_test.go`（新建） | 上述全部单元测试 + 一致性不变量测试 |
| `backend/internal/service/account.go`（修改） | 新增 credentials 标记常量与 `SupportsOpenAIImagesUsageSimulation()` |
| `backend/internal/service/account_test.go`（修改） | 标记解析的单测 |
| `backend/internal/service/openai_gateway_response_handling.go:747`（修改） | `openAIUsageFromGJSON` 补读 `input_tokens_details.image_tokens` |
| `backend/internal/service/openai_gateway_response_handling_test.go`（修改） | 上述补丁的单测 |
| `backend/internal/service/openai_images.go:891,728`（修改） | 接入点：改签名 + 在 `c.Data` 前调用编排入口 |

---

### Task 1: 修复 ImageInputTokens 从未被填充

独立于模拟功能的既有缺陷：`openAIUsageFromGJSON` 读了 output 侧的 image_tokens，
漏读 input 侧，导致所有改图请求的输入图像 token（$8/1M 档）从不计费。先行修复。

**Files:**
- Modify: `backend/internal/service/openai_gateway_response_handling.go:747-772`
- Test: `backend/internal/service/openai_gateway_response_handling_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `openAIUsageFromGJSON` 填充 `OpenAIUsage.ImageInputTokens`

- [ ] **Step 1: 写失败测试**

追加到 `backend/internal/service/openai_gateway_response_handling_test.go`：

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
	body := `{"usage":{"input_tokens":54,"output_tokens":229}}`
	usage, ok := extractOpenAIUsageFromJSONBytes([]byte(body))
	if !ok {
		t.Fatalf("expected usage to be parsed")
	}
	if usage.ImageInputTokens != 0 {
		t.Errorf("ImageInputTokens = %d, want 0", usage.ImageInputTokens)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/service/ -run 'TestOpenAIUsageFromGJSONReadsImageInputTokens' -v
```

预期：FAIL，`ImageInputTokens = 0, want 1508`。

- [ ] **Step 3: 实现**

在 `backend/internal/service/openai_gateway_response_handling.go` 的 `openAIUsageFromGJSON`
中，紧跟 `imageOutputTokens` 那段之后、`return` 之前插入：

```go
	imageInputTokens := value.Get("input_tokens_details.image_tokens").Int()
	if imageInputTokens == 0 {
		imageInputTokens = value.Get("prompt_tokens_details.image_tokens").Int()
	}
```

并在返回的结构体里加一行：

```go
	return OpenAIUsage{
		InputTokens:              int(inputTokens),
		ImageInputTokens:         int(imageInputTokens),
		OutputTokens:             int(outputTokens),
		CacheCreationInputTokens: cacheCreationTokens,
		CacheReadInputTokens:     cacheReadTokens,
		ImageOutputTokens:        int(imageOutputTokens),
	}, true
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/service/ -run 'TestOpenAIUsageFromGJSON' -v
```

预期：全部 PASS。

- [ ] **Step 5: 跑一遍 service 包确认无回归**

```bash
cd backend && go test ./internal/service/ 2>&1 | tail -20
```

预期：新增失败为 0（与改动前的基线失败清单一致）。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/service/openai_gateway_response_handling.go \
        backend/internal/service/openai_gateway_response_handling_test.go
git commit -m "fix(images): usage 解析补读 input_tokens_details.image_tokens

输入图像 token 此前恒为 0，改图请求的图像输入部分（\$8/1M 档）从未计费。
billing_service 已支持 ImageInputTokens>0 的分单价路径，仅解析侧缺失。"
```

---

### Task 2: 账号级模拟开关

**Files:**
- Modify: `backend/internal/service/account.go`（常量区 ~:93、方法区 ~:1449）
- Test: `backend/internal/service/account_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `func (a *Account) SupportsOpenAIImagesUsageSimulation() bool`

- [ ] **Step 1: 写失败测试**

追加到 `backend/internal/service/account_test.go`：

```go
func TestSupportsOpenAIImagesUsageSimulation(t *testing.T) {
	cases := []struct {
		name string
		creds map[string]any
		want bool
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

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/service/ -run TestSupportsOpenAIImagesUsageSimulation -v
```

预期：编译失败 `a.SupportsOpenAIImagesUsageSimulation undefined`。

- [ ] **Step 3: 实现**

在 `backend/internal/service/account.go` 的常量区，紧邻
`openAIImagesHighResCredentialKey` 之后加：

```go
// openAIImagesUsageSimulationCredentialKey 标记账号的 images 响应需要被改写成
// 官方 gpt-image-2 口径（usage + 官方字段）。白名单语义：仅显式开启时生效。
const openAIImagesUsageSimulationCredentialKey = "openai_images_usage_simulation"
```

在 `SupportsOpenAIImagesHighRes` 之后加（与其解析逻辑一致，复用同一套取值语义）：

```go
// SupportsOpenAIImagesUsageSimulation 判断账号是否启用 images usage 模拟改写。
func (a *Account) SupportsOpenAIImagesUsageSimulation() bool {
	return a.credentialFlag(openAIImagesUsageSimulationCredentialKey)
}
```

并把 `SupportsOpenAIImagesHighRes` 里的解析体提取为共用私有方法（DRY）：

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
```

`SupportsOpenAIImagesHighRes` 改为：

```go
func (a *Account) SupportsOpenAIImagesHighRes() bool {
	return a.credentialFlag(openAIImagesHighResCredentialKey)
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/service/ -run 'TestSupportsOpenAIImages' -v
```

预期：新测试 PASS，既有的 highres 测试仍 PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/account.go backend/internal/service/account_test.go
git commit -m "feat(images): 新增账号级 usage 模拟开关凭据标记"
```

---

### Task 3: 输入图像 token 公式

**Files:**
- Create: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `func openAIImageInputTokens(w, h int) int`

- [ ] **Step 1: 写失败测试**

新建 `backend/internal/service/openai_images_usage_simulation_test.go`：

```go
package service

import "testing"

// 数据来源：docs/GPT_IMAGE_2_TOKEN_REFERENCE.md §6，官方直连与 codex 两条管线实测。
func TestOpenAIImageInputTokens(t *testing.T) {
	cases := []struct {
		w, h int
		want int
	}{
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
}

func TestOpenAIImageInputTokensInvalid(t *testing.T) {
	for _, tc := range [][2]int{{0, 100}, {100, 0}, {-1, -1}} {
		if got := openAIImageInputTokens(tc[0], tc[1]); got != 0 {
			t.Errorf("openAIImageInputTokens(%d,%d) = %d, want 0", tc[0], tc[1], got)
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/service/ -run TestOpenAIImageInputTokens -v
```

预期：编译失败 `undefined: openAIImageInputTokens`。

- [ ] **Step 3: 实现**

新建 `backend/internal/service/openai_images_usage_simulation.go`：

```go
package service

import "math"

// openAIImageInputPatchLimit 是官方对单张输入图的 patch 数上限。
const openAIImageInputPatchLimit = 1536

// openAIImageInputUpscaleTarget 是最长边不足时的放大目标（放大倍数另有 2 倍上限）。
const openAIImageInputUpscaleTarget = 1024

// openAIImageInputTokens 按官方口径计算单张输入图的图像 token。
//
//	若 max(w,h) < 1024：按 min(2.0, 1024/max(w,h)) 放大
//	patches = ceil(w/32) * ceil(h/32)
//	若 patches > 1536：等比缩小直到 patches <= 1536
//
// 该公式在官方直连与 codex 两条管线共 11 个实测点上精确吻合，
// 依据见 docs/GPT_IMAGE_2_TOKEN_REFERENCE.md §6。
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

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/service/ -run TestOpenAIImageInputTokens -v
```

预期：全部 PASS（11 个点逐一命中）。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 按官方口径实现输入图像 token 公式"
```

---

### Task 4: 输出 token 表与比例/档位归一

**Files:**
- Modify: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `func normalizeOpenAIImageRatio(w, h int) string`（返回归一后的横版比例键）
  - `func openAIImageTierByPixels(w, h int) string`（返回 `"1K"`/`"2K"`/`"4K"`）
  - `func openAIImageOutputTokens(ratio, tier string) (int, bool)`

- [ ] **Step 1: 写失败测试**

追加到 `openai_images_usage_simulation_test.go`：

```go
func TestNormalizeOpenAIImageRatio(t *testing.T) {
	cases := []struct {
		w, h int
		want string
	}{
		{1024, 1024, "1:1"},
		{2880, 2880, "1:1"},
		{1120, 896, "5:4"},
		{896, 1120, "5:4"},   // 竖版归一到横版键
		{1152, 864, "4:3"},
		{864, 1152, "4:3"},
		{1248, 832, "3:2"},
		{832, 1248, "3:2"},
		{1280, 720, "16:9"},
		{720, 1280, "16:9"},
		{3840, 2160, "16:9"},
		{1456, 624, "21:9"},
		{3024, 1296, "21:9"},
	}
	for _, tc := range cases {
		if got := normalizeOpenAIImageRatio(tc.w, tc.h); got != tc.want {
			t.Errorf("normalizeOpenAIImageRatio(%d,%d) = %q, want %q", tc.w, tc.h, got, tc.want)
		}
	}
}

// 档位必须按总像素判定：21:9 的 2K 是 3024x1296，最长边 3024 会被长边阈值误判成 4K。
func TestOpenAIImageTierByPixels(t *testing.T) {
	cases := []struct {
		w, h int
		want string
	}{
		{1456, 624, "1K"},   // 1K 最小像素 908,544
		{1024, 1024, "1K"},  // 1K 最大像素 1,048,576
		{2560, 1440, "2K"},  // 2K 最小像素 3,686,400
		{3024, 1296, "2K"},  // 长边 3024，但属 2K
		{2048, 2048, "2K"},  // 2K 最大像素 4,194,304
		{3696, 1584, "4K"},  // 4K 最小像素 5,854,464
		{3840, 2160, "4K"},
		{2880, 2880, "4K"},  // 4K 最大像素 8,294,400
	}
	for _, tc := range cases {
		if got := openAIImageTierByPixels(tc.w, tc.h); got != tc.want {
			t.Errorf("openAIImageTierByPixels(%d,%d) = %q, want %q", tc.w, tc.h, got, tc.want)
		}
	}
}

func TestOpenAIImageOutputTokens(t *testing.T) {
	cases := []struct {
		ratio, tier string
		want        int
	}{
		{"1:1", "1K", 196}, {"1:1", "2K", 3568}, {"1:1", "4K", 23658},
		{"5:4", "1K", 157}, {"5:4", "2K", 2811}, {"5:4", "4K", 19027},
		{"4:3", "1K", 144}, {"4:3", "2K", 2586}, {"4:3", "4K", 17232},
		{"3:2", "1K", 134}, {"3:2", "2K", 2434}, {"3:2", "4K", 16119},
		{"16:9", "1K", 106}, {"16:9", "2K", 1841}, {"16:9", "4K", 13342},
		{"21:9", "1K", 82}, {"21:9", "2K", 1491}, {"21:9", "4K", 7898},
	}
	for _, tc := range cases {
		got, ok := openAIImageOutputTokens(tc.ratio, tc.tier)
		if !ok || got != tc.want {
			t.Errorf("openAIImageOutputTokens(%q,%q) = %d,%v want %d,true", tc.ratio, tc.tier, got, ok, tc.want)
		}
	}
	if _, ok := openAIImageOutputTokens("7:3", "1K"); ok {
		t.Errorf("unknown ratio should return ok=false")
	}
	if _, ok := openAIImageOutputTokens("1:1", "8K"); ok {
		t.Errorf("unknown tier should return ok=false")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/service/ -run 'TestNormalizeOpenAIImageRatio|TestOpenAIImageTierByPixels|TestOpenAIImageOutputTokens' -v
```

预期：编译失败，三个函数 undefined。

- [ ] **Step 3: 实现**

追加到 `openai_images_usage_simulation.go`（同时把 `import "math"` 保持不变）：

```go
// openAIImageRatioKeys 是官方 gpt-image-2 支持的 10 个比例归一后的 6 个横版键。
// 竖版（4:5 / 3:4 / 2:3 / 9:16）token 与对应横版相同，故统一归一到横版键。
var openAIImageRatioKeys = []struct {
	key   string
	value float64
}{
	{"1:1", 1.0},
	{"5:4", 5.0 / 4.0},
	{"4:3", 4.0 / 3.0},
	{"3:2", 3.0 / 2.0},
	{"16:9", 16.0 / 9.0},
	{"21:9", 21.0 / 9.0},
}

// normalizeOpenAIImageRatio 取与实际宽高比最接近的受支持比例键。
// 竖版先转成横版再比对。
func normalizeOpenAIImageRatio(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	long, short := float64(w), float64(h)
	if long < short {
		long, short = short, long
	}
	target := long / short
	best := openAIImageRatioKeys[0].key
	bestDiff := math.Abs(target - openAIImageRatioKeys[0].value)
	for _, candidate := range openAIImageRatioKeys[1:] {
		if diff := math.Abs(target - candidate.value); diff < bestDiff {
			best, bestDiff = candidate.key, diff
		}
	}
	return best
}

// 档位阈值按总像素。三档实测像素区间互不重叠且间隔充裕：
//	1K [908,544, 1,048,576]  2K [3,686,400, 4,194,304]  4K [5,854,464, 8,294,400]
// 不可改为按最长边判定：21:9 的 2K 是 3024x1296，长边 3024 会被误判成 4K。
const (
	openAIImageTier1KMaxPixels = 1_600_000
	openAIImageTier2KMaxPixels = 4_500_000
)

func openAIImageTierByPixels(w, h int) string {
	pixels := w * h
	switch {
	case pixels <= 0:
		return ""
	case pixels <= openAIImageTier1KMaxPixels:
		return ImageBillingSize1K
	case pixels <= openAIImageTier2KMaxPixels:
		return ImageBillingSize2K
	default:
		return ImageBillingSize4K
	}
}

// openAIImageOutputTokensTable 为输出图像 token 表，键为 [比例][档位]。
//
// 档位到官方 quality 的映射为**自定义**（官方 quality 与 size 是两个正交维度）：
// 1K 取 low、2K 取 medium、4K 取 high，以获得更陡的价格梯度。
// 表中 196 / 3568 / 13342 为实测值，其余由 low × 8.98（medium）
// 与 low × 35.9（high）推导。依据见 docs/GPT_IMAGE_2_TOKEN_REFERENCE.md §4、§5。
var openAIImageOutputTokensTable = map[string]map[string]int{
	"1:1":  {ImageBillingSize1K: 196, ImageBillingSize2K: 3568, ImageBillingSize4K: 23658},
	"5:4":  {ImageBillingSize1K: 157, ImageBillingSize2K: 2811, ImageBillingSize4K: 19027},
	"4:3":  {ImageBillingSize1K: 144, ImageBillingSize2K: 2586, ImageBillingSize4K: 17232},
	"3:2":  {ImageBillingSize1K: 134, ImageBillingSize2K: 2434, ImageBillingSize4K: 16119},
	"16:9": {ImageBillingSize1K: 106, ImageBillingSize2K: 1841, ImageBillingSize4K: 13342},
	"21:9": {ImageBillingSize1K: 82, ImageBillingSize2K: 1491, ImageBillingSize4K: 7898},
}

func openAIImageOutputTokens(ratio, tier string) (int, bool) {
	tiers, ok := openAIImageOutputTokensTable[ratio]
	if !ok {
		return 0, false
	}
	tokens, ok := tiers[tier]
	return tokens, ok
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/service/ -run 'TestNormalizeOpenAIImageRatio|TestOpenAIImageTierByPixels|TestOpenAIImageOutputTokens' -v
```

预期：全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 新增 gpt-image-2 输出 token 实测表与比例/档位归一"
```

---

### Task 5: 几何归一

**Files:**
- Modify: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Consumes: `normalizeOpenAIImageRatio`、`openAIImageTierByPixels`（Task 4）
- Produces:
  - `type openAIImageGeometry struct { Width, Height int; Ratio, Tier string }`
  - `func resolveOpenAIImageGeometry(body []byte, requestSize string) (openAIImageGeometry, bool)`

- [ ] **Step 1: 写失败测试**

追加到 `openai_images_usage_simulation_test.go`（文件顶部 import 增补
`"bytes"`、`"encoding/base64"`、`"image"`、`"image/color"`、`"image/png"`）：

```go
func pngDataURLBase64(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestResolveOpenAIImageGeometryFromSizeField(t *testing.T) {
	body := []byte(`{"size":"2048x2048","data":[{"b64_json":"aGk="}]}`)
	geom, ok := resolveOpenAIImageGeometry(body, "")
	if !ok {
		t.Fatalf("expected ok")
	}
	if geom.Width != 2048 || geom.Height != 2048 {
		t.Errorf("dims = %dx%d, want 2048x2048", geom.Width, geom.Height)
	}
	if geom.Ratio != "1:1" || geom.Tier != ImageBillingSize2K {
		t.Errorf("ratio/tier = %q/%q, want 1:1/2K", geom.Ratio, geom.Tier)
	}
}

func TestResolveOpenAIImageGeometryFromB64(t *testing.T) {
	body := []byte(`{"data":[{"b64_json":"` + pngDataURLBase64(t, 1280, 720) + `"}]}`)
	geom, ok := resolveOpenAIImageGeometry(body, "")
	if !ok {
		t.Fatalf("expected ok")
	}
	if geom.Width != 1280 || geom.Height != 720 {
		t.Errorf("dims = %dx%d, want 1280x720", geom.Width, geom.Height)
	}
	if geom.Ratio != "16:9" || geom.Tier != ImageBillingSize1K {
		t.Errorf("ratio/tier = %q/%q, want 16:9/1K", geom.Ratio, geom.Tier)
	}
}

func TestResolveOpenAIImageGeometryFromRequestSize(t *testing.T) {
	body := []byte(`{"data":[{"url":"https://example.com/a.png"}]}`)
	geom, ok := resolveOpenAIImageGeometry(body, "3024x1296")
	if !ok {
		t.Fatalf("expected ok")
	}
	if geom.Ratio != "21:9" || geom.Tier != ImageBillingSize2K {
		t.Errorf("ratio/tier = %q/%q, want 21:9/2K", geom.Ratio, geom.Tier)
	}
}

func TestResolveOpenAIImageGeometryFails(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"data":[]}`),
		[]byte(`{"data":[{"url":"https://example.com/a.png"}]}`),
		[]byte(`not json`),
		nil,
	}
	for i, body := range cases {
		if _, ok := resolveOpenAIImageGeometry(body, ""); ok {
			t.Errorf("case %d: expected ok=false", i)
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/service/ -run TestResolveOpenAIImageGeometry -v
```

预期：编译失败 `undefined: resolveOpenAIImageGeometry`。

- [ ] **Step 3: 实现**

追加到 `openai_images_usage_simulation.go`，并把文件顶部 import 改为：

```go
import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	_ "golang.org/x/image/webp"
)
```

```go
// openAIImageGeometry 描述一次出图的几何信息。
type openAIImageGeometry struct {
	Width  int
	Height int
	Ratio  string // 归一后的横版比例键，如 "16:9"
	Tier   string // ImageBillingSize1K / 2K / 4K
}

// resolveOpenAIImageGeometry 按优先级确定出图几何：
//  1. 响应体 size 字段（官方直连与 codex 管线有，adobe2api 无）
//  2. 解码首张 b64_json 的图像头取真实宽高
//  3. 请求侧 size
//
// 三者皆不可用时返回 ok=false，调用方应放弃改写。
func resolveOpenAIImageGeometry(body []byte, requestSize string) (openAIImageGeometry, bool) {
	if w, h, ok := parseWidthHeight(gjson.GetBytes(body, "size").String()); ok {
		return buildOpenAIImageGeometry(w, h)
	}
	if gjson.ValidBytes(body) {
		encoded := gjson.GetBytes(body, "data.0.b64_json").String()
		if w, h, ok := decodeImageDimensionsBase64(encoded); ok {
			return buildOpenAIImageGeometry(w, h)
		}
	}
	if w, h, ok := parseWidthHeight(requestSize); ok {
		return buildOpenAIImageGeometry(w, h)
	}
	return openAIImageGeometry{}, false
}

func buildOpenAIImageGeometry(w, h int) (openAIImageGeometry, bool) {
	ratio := normalizeOpenAIImageRatio(w, h)
	tier := openAIImageTierByPixels(w, h)
	if ratio == "" || tier == "" {
		return openAIImageGeometry{}, false
	}
	return openAIImageGeometry{Width: w, Height: h, Ratio: ratio, Tier: tier}, true
}

// parseWidthHeight 解析 "1024x1024" 形态；"auto" 等非尺寸值返回 ok=false。
func parseWidthHeight(value string) (int, int, bool) {
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

// decodeImageDimensionsBase64 只解图像头，不解整幅图像。
func decodeImageDimensionsBase64(encoded string) (int, int, bool) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return 0, 0, false
	}
	if idx := strings.Index(encoded, ","); strings.HasPrefix(encoded, "data:") && idx > 0 {
		encoded = encoded[idx+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 {
		return 0, 0, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/service/ -run TestResolveOpenAIImageGeometry -v
```

预期：全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 新增出图几何归一（size 字段/b64 图像头/请求尺寸三级回退）"
```

---

### Task 6: usage 合成器

**Files:**
- Modify: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Consumes: `openAIImageInputTokens`（Task 3）、`openAIImageOutputTokens`（Task 4）
- Produces:
  - `type openAIImagesSynthUsage struct { TextInputTokens, ImageInputTokens, ImageOutputTokens, InputTokens, OutputTokens, TotalTokens int }`
  - `func synthesizeOpenAIImagesUsage(geom openAIImageGeometry, textInputTokens int, inputImageDims [][2]int, imageCount int) (openAIImagesSynthUsage, bool)`

- [ ] **Step 1: 写失败测试**

```go
func TestSynthesizeOpenAIImagesUsageTextOnly(t *testing.T) {
	geom := openAIImageGeometry{Width: 1024, Height: 1024, Ratio: "1:1", Tier: ImageBillingSize1K}
	s, ok := synthesizeOpenAIImagesUsage(geom, 15, nil, 1)
	if !ok {
		t.Fatalf("expected ok")
	}
	if s.ImageOutputTokens != 196 || s.OutputTokens != 196 {
		t.Errorf("output = %d/%d, want 196/196", s.ImageOutputTokens, s.OutputTokens)
	}
	if s.ImageInputTokens != 0 || s.InputTokens != 15 {
		t.Errorf("input = %d/%d, want 0/15", s.ImageInputTokens, s.InputTokens)
	}
	if s.TotalTokens != 211 {
		t.Errorf("total = %d, want 211", s.TotalTokens)
	}
}

func TestSynthesizeOpenAIImagesUsageWithInputImages(t *testing.T) {
	geom := openAIImageGeometry{Width: 1024, Height: 1024, Ratio: "1:1", Tier: ImageBillingSize1K}
	// 550x368 -> 704, 2048x1152 -> 1508, 可加
	s, ok := synthesizeOpenAIImagesUsage(geom, 10, [][2]int{{550, 368}, {2048, 1152}}, 1)
	if !ok {
		t.Fatalf("expected ok")
	}
	if s.ImageInputTokens != 2212 {
		t.Errorf("ImageInputTokens = %d, want 2212", s.ImageInputTokens)
	}
	if s.InputTokens != 2222 {
		t.Errorf("InputTokens = %d, want 2222", s.InputTokens)
	}
	if s.TotalTokens != s.InputTokens+s.OutputTokens {
		t.Errorf("total invariant broken")
	}
}

func TestSynthesizeOpenAIImagesUsageMultipleOutputs(t *testing.T) {
	geom := openAIImageGeometry{Width: 3840, Height: 2160, Ratio: "16:9", Tier: ImageBillingSize4K}
	s, ok := synthesizeOpenAIImagesUsage(geom, 20, nil, 2)
	if !ok {
		t.Fatalf("expected ok")
	}
	if s.ImageOutputTokens != 13342*2 {
		t.Errorf("ImageOutputTokens = %d, want %d", s.ImageOutputTokens, 13342*2)
	}
}

func TestSynthesizeOpenAIImagesUsageUnknownGeometry(t *testing.T) {
	geom := openAIImageGeometry{Width: 100, Height: 100, Ratio: "7:3", Tier: ImageBillingSize1K}
	if _, ok := synthesizeOpenAIImagesUsage(geom, 10, nil, 1); ok {
		t.Errorf("unknown ratio should return ok=false")
	}
}

func TestSynthesizeOpenAIImagesUsageTextTokenFloor(t *testing.T) {
	geom := openAIImageGeometry{Width: 1024, Height: 1024, Ratio: "1:1", Tier: ImageBillingSize1K}
	s, _ := synthesizeOpenAIImagesUsage(geom, 0, nil, 1)
	if s.TextInputTokens != 1 {
		t.Errorf("TextInputTokens = %d, want floor 1", s.TextInputTokens)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/service/ -run TestSynthesizeOpenAIImagesUsage -v
```

预期：编译失败 `undefined: synthesizeOpenAIImagesUsage`。

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

// synthesizeOpenAIImagesUsage 按几何与输入图尺寸合成官方口径 usage。
// imageCount 为实际产出图片数（对应请求的 n）。
func synthesizeOpenAIImagesUsage(
	geom openAIImageGeometry,
	textInputTokens int,
	inputImageDims [][2]int,
	imageCount int,
) (openAIImagesSynthUsage, bool) {
	perImage, ok := openAIImageOutputTokens(geom.Ratio, geom.Tier)
	if !ok {
		return openAIImagesSynthUsage{}, false
	}
	if imageCount < 1 {
		imageCount = 1
	}
	if textInputTokens < 1 {
		textInputTokens = 1
	}
	imageInput := 0
	for _, dim := range inputImageDims {
		imageInput += openAIImageInputTokens(dim[0], dim[1])
	}
	s := openAIImagesSynthUsage{
		TextInputTokens:   textInputTokens,
		ImageInputTokens:  imageInput,
		ImageOutputTokens: perImage * imageCount,
	}
	s.InputTokens = s.TextInputTokens + s.ImageInputTokens
	s.OutputTokens = s.ImageOutputTokens
	s.TotalTokens = s.InputTokens + s.OutputTokens
	return s, true
}

// toOpenAIUsage 产出与响应体同源的计费 usage。
func (s openAIImagesSynthUsage) toOpenAIUsage() OpenAIUsage {
	return OpenAIUsage{
		InputTokens:       s.InputTokens,
		ImageInputTokens:  s.ImageInputTokens,
		OutputTokens:      s.OutputTokens,
		ImageOutputTokens: s.ImageOutputTokens,
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/service/ -run TestSynthesizeOpenAIImagesUsage -v
```

预期：全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 新增 images usage 合成器"
```

---

### Task 7: 响应体改写

**Files:**
- Modify: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Consumes: `openAIImagesSynthUsage`、`openAIImageGeometry`
- Produces: `func rewriteOpenAIImagesResponseBody(body []byte, model string, s openAIImagesSynthUsage, geom openAIImageGeometry) ([]byte, bool)`

- [ ] **Step 1: 写失败测试**

```go
func TestRewriteOpenAIImagesResponseBodyAdobe(t *testing.T) {
	b64 := pngDataURLBase64(t, 8, 8)
	body := []byte(`{"created":1,"model":"gpt-image-2","data":[{"b64_json":"` + b64 + `"}],` +
		`"usage":{"input_tokens":304,"output_tokens":400,"total_tokens":704,` +
		`"input_tokens_details":{"image_tokens":300,"text_tokens":4},` +
		`"output_tokens_details":{"image_tokens":400,"text_tokens":0}}}`)
	geom := openAIImageGeometry{Width: 2048, Height: 2048, Ratio: "1:1", Tier: ImageBillingSize2K}
	s, _ := synthesizeOpenAIImagesUsage(geom, 12, nil, 1)

	out, ok := rewriteOpenAIImagesResponseBody(body, "gpt-image-2", s, geom)
	if !ok {
		t.Fatalf("expected ok")
	}
	if got := gjson.GetBytes(out, "background").String(); got != "opaque" {
		t.Errorf("background = %q, want opaque", got)
	}
	if got := gjson.GetBytes(out, "output_format").String(); got != "png" {
		t.Errorf("output_format = %q, want png", got)
	}
	if got := gjson.GetBytes(out, "quality").String(); got != "medium" {
		t.Errorf("quality = %q, want medium (2K)", got)
	}
	if got := gjson.GetBytes(out, "size").String(); got != "2048x2048" {
		t.Errorf("size = %q, want 2048x2048", got)
	}
	if got := gjson.GetBytes(out, "usage.output_tokens_details.image_tokens").Int(); got != 3568 {
		t.Errorf("image_tokens = %d, want 3568", got)
	}
	if got := gjson.GetBytes(out, "usage.output_tokens_details.text_tokens").Int(); got != 0 {
		t.Errorf("output text_tokens = %d, want 0", got)
	}
	// 图像数据必须逐字节保留
	if gjson.GetBytes(out, "data.0.b64_json").String() != b64 {
		t.Errorf("b64_json was modified")
	}
}

func TestRewriteOpenAIImagesResponseBodyCodex(t *testing.T) {
	b64 := pngDataURLBase64(t, 8, 8)
	body := []byte(`{"created":1,"model":"gpt-image-2-codex","quality":"auto","size":"1672x941",` +
		`"data":[{"b64_json":"` + b64 + `","revised_prompt":"expanded prompt text"}],` +
		`"usage":{"input_tokens":2327,"output_tokens":1158,"total_tokens":3485}}`)
	geom := openAIImageGeometry{Width: 1280, Height: 720, Ratio: "16:9", Tier: ImageBillingSize1K}
	s, _ := synthesizeOpenAIImagesUsage(geom, 20, nil, 1)

	out, ok := rewriteOpenAIImagesResponseBody(body, "gpt-image-2", s, geom)
	if !ok {
		t.Fatalf("expected ok")
	}
	if got := gjson.GetBytes(out, "model").String(); got != "gpt-image-2" {
		t.Errorf("model = %q, want gpt-image-2", got)
	}
	if gjson.GetBytes(out, "data.0.revised_prompt").Exists() {
		t.Errorf("revised_prompt should be removed")
	}
	if got := gjson.GetBytes(out, "quality").String(); got != "low" {
		t.Errorf("quality = %q, want low (1K)", got)
	}
	if got := gjson.GetBytes(out, "size").String(); got != "1280x720" {
		t.Errorf("size = %q, want 1280x720", got)
	}
}

func TestRewriteOpenAIImagesResponseBodyInvalid(t *testing.T) {
	geom := openAIImageGeometry{Width: 1024, Height: 1024, Ratio: "1:1", Tier: ImageBillingSize1K}
	s, _ := synthesizeOpenAIImagesUsage(geom, 10, nil, 1)
	if _, ok := rewriteOpenAIImagesResponseBody([]byte("not json"), "gpt-image-2", s, geom); ok {
		t.Errorf("invalid json should return ok=false")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/service/ -run TestRewriteOpenAIImagesResponseBody -v
```

预期：编译失败 `undefined: rewriteOpenAIImagesResponseBody`。

- [ ] **Step 3: 实现**

在 import 中增补 `"fmt"` 与 `"github.com/tidwall/sjson"`，然后追加：

```go
// openAIImageTierQuality 把内部档位映射为响应体里回显的官方 quality 值。
// 这是自定义映射（官方 quality 与 size 正交），与计费表口径保持一致。
var openAIImageTierQuality = map[string]string{
	ImageBillingSize1K: "low",
	ImageBillingSize2K: "medium",
	ImageBillingSize4K: "high",
}

// rewriteOpenAIImagesResponseBody 用合成 usage 替换响应体的 usage，
// 并补齐官方独有字段、抹除上游指纹。不触碰 data[].b64_json / data[].url。
func rewriteOpenAIImagesResponseBody(
	body []byte,
	model string,
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

	if !gjson.GetBytes(out, "background").Exists() {
		if !set("background", "opaque") {
			return body, false
		}
	}
	if !gjson.GetBytes(out, "output_format").Exists() {
		if !set("output_format", openAIImageOutputFormat(out)) {
			return body, false
		}
	}
	if quality, ok := openAIImageTierQuality[geom.Tier]; ok {
		if !set("quality", quality) {
			return body, false
		}
	}
	if !set("size", fmt.Sprintf("%dx%d", geom.Width, geom.Height)) {
		return body, false
	}
	if strings.TrimSpace(model) != "" {
		if !set("model", strings.TrimSpace(model)) {
			return body, false
		}
	}

	// 抹除 codex 管线特有的 revised_prompt
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

// openAIImageOutputFormat 由首张图像的实际编码推断，失败时回落 png。
func openAIImageOutputFormat(body []byte) string {
	encoded := strings.TrimSpace(gjson.GetBytes(body, "data.0.b64_json").String())
	if encoded == "" {
		return "png"
	}
	if idx := strings.Index(encoded, ","); strings.HasPrefix(encoded, "data:") && idx > 0 {
		encoded = encoded[idx+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "png"
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "png"
	}
	switch format {
	case "jpeg":
		return "jpeg"
	case "webp":
		return "webp"
	default:
		return "png"
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/service/ -run TestRewriteOpenAIImagesResponseBody -v
```

预期：全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 响应体改写为官方 gpt-image-2 结构"
```

---

### Task 8: 编排入口与一致性不变量

**Files:**
- Modify: `backend/internal/service/openai_images_usage_simulation.go`
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Consumes: Task 5/6/7 全部产物
- Produces: `func applyOpenAIImagesUsageSimulation(body []byte, model string, parsed *OpenAIImagesRequest) ([]byte, OpenAIUsage, bool)`

- [ ] **Step 1: 写失败测试**

```go
// 不变量：改写后的 body 再解析出的 usage，必须与用于计费的 usage 完全一致。
func TestApplyOpenAIImagesUsageSimulationConsistency(t *testing.T) {
	b64 := pngDataURLBase64(t, 2048, 2048)
	body := []byte(`{"created":1,"model":"gpt-image-2","data":[{"b64_json":"` + b64 + `"}],` +
		`"usage":{"input_tokens":304,"output_tokens":400,"total_tokens":704}}`)
	parsed := &OpenAIImagesRequest{Prompt: "a plain blue circle", Size: "2048x2048", N: 1}

	out, usage, ok := applyOpenAIImagesUsageSimulation(body, "gpt-image-2", parsed)
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
	if usage.ImageOutputTokens != 3568 {
		t.Errorf("ImageOutputTokens = %d, want 3568", usage.ImageOutputTokens)
	}
}

func TestApplyOpenAIImagesUsageSimulationDegrades(t *testing.T) {
	// 几何不可确定：无 size、无可解码 b64、请求也无 size
	body := []byte(`{"data":[{"url":"https://example.com/a.png"}],"usage":{"input_tokens":1,"output_tokens":2}}`)
	parsed := &OpenAIImagesRequest{Prompt: "x", N: 1}

	out, _, ok := applyOpenAIImagesUsageSimulation(body, "gpt-image-2", parsed)
	if ok {
		t.Errorf("expected applied=false")
	}
	if string(out) != string(body) {
		t.Errorf("body must be returned untouched on degrade")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/service/ -run TestApplyOpenAIImagesUsageSimulation -v
```

预期：编译失败 `undefined: applyOpenAIImagesUsageSimulation`。

- [ ] **Step 3: 实现**

```go
// applyOpenAIImagesUsageSimulation 串联几何归一、合成与响应体改写。
// 任一环节失败均返回原 body 与 applied=false，调用方沿用原 usage。
func applyOpenAIImagesUsageSimulation(
	body []byte,
	model string,
	parsed *OpenAIImagesRequest,
) ([]byte, OpenAIUsage, bool) {
	if len(body) == 0 || parsed == nil {
		return body, OpenAIUsage{}, false
	}
	geom, ok := resolveOpenAIImageGeometry(body, parsed.Size)
	if !ok {
		return body, OpenAIUsage{}, false
	}

	imageCount := len(gjson.GetBytes(body, "data").Array())
	if imageCount < 1 {
		return body, OpenAIUsage{}, false
	}

	textTokens := int(gjson.GetBytes(body, "usage.input_tokens_details.text_tokens").Int())
	if textTokens <= 0 {
		textTokens = estimateOpenAIImagePromptTokens(parsed.Prompt)
	}

	synth, ok := synthesizeOpenAIImagesUsage(geom, textTokens, openAIImagesInputDims(parsed), imageCount)
	if !ok {
		return body, OpenAIUsage{}, false
	}
	newBody, ok := rewriteOpenAIImagesResponseBody(body, model, synth, geom)
	if !ok {
		return body, OpenAIUsage{}, false
	}
	return newBody, synth.toOpenAIUsage(), true
}

// openAIImagesInputDims 取请求侧输入图的像素尺寸。
// data URL 与 multipart 上传可直接解码；http(s) URL 不额外下载，跳过。
func openAIImagesInputDims(parsed *OpenAIImagesRequest) [][2]int {
	dims := make([][2]int, 0, len(parsed.InputImageURLs)+len(parsed.Uploads))
	for _, imageURL := range parsed.InputImageURLs {
		if !strings.HasPrefix(strings.TrimSpace(imageURL), "data:") {
			continue
		}
		if w, h, ok := decodeImageDimensionsBase64(imageURL); ok {
			dims = append(dims, [2]int{w, h})
		}
	}
	for _, upload := range parsed.Uploads {
		// Uploads 的 Width/Height 来自 multipart 头部（parseOpenAIImageDimensions），
		// 客户端通常不带，故为 0；非 0 时可省一次解码。
		if upload.Width > 0 && upload.Height > 0 {
			dims = append(dims, [2]int{upload.Width, upload.Height})
			continue
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(upload.Data))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			continue
		}
		dims = append(dims, [2]int{cfg.Width, cfg.Height})
	}
	return dims
}

// estimateOpenAIImagePromptTokens 在上游未回传文本 token 时粗估：
// CJK 按 1 token/字，其余按 4 字符/token，下限 1。
func estimateOpenAIImagePromptTokens(prompt string) int {
	cjk := 0
	other := 0
	for _, r := range prompt {
		if (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3040 && r <= 0x30FF) || (r >= 0xAC00 && r <= 0xD7AF) {
			cjk++
			continue
		}
		other++
	}
	tokens := cjk + other/4
	if tokens < 1 {
		return 1
	}
	return tokens
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/service/ -run TestApplyOpenAIImagesUsageSimulation -v
```

预期：全部 PASS，尤其是一致性不变量那条。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 新增 usage 模拟编排入口与响应体/计费一致性保证"
```

---

### Task 9: 接入转发链路

**Files:**
- Modify: `backend/internal/service/openai_images.go:891`（`handleOpenAIImagesNonStreamingResponse`）
- Modify: `backend/internal/service/openai_images.go:728`（唯一调用点）
- Test: `backend/internal/service/openai_images_usage_simulation_test.go`

**Interfaces:**
- Consumes: `applyOpenAIImagesUsageSimulation`（Task 8）、`SupportsOpenAIImagesUsageSimulation`（Task 2）
- Produces: 无（终点任务）

- [ ] **Step 1: 写失败测试**

```go
func TestMaybeSimulateOpenAIImagesUsage(t *testing.T) {
	b64 := pngDataURLBase64(t, 1024, 1024)
	upstream := `{"created":1,"model":"gpt-image-2","data":[{"b64_json":"` + b64 + `"}],` +
		`"usage":{"input_tokens":304,"output_tokens":400,"total_tokens":704}}`

	cases := []struct {
		name          string
		account       *Account
		wantSimulated bool
	}{
		{"标记账号触发改写", &Account{Credentials: map[string]any{"openai_images_usage_simulation": true}}, true},
		{"未标记账号原样透传", &Account{Credentials: map[string]any{}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(upstream)
			parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "circle", Size: "1024x1024", N: 1}
			out, usage, applied := maybeSimulateOpenAIImagesUsage(body, tc.account, parsed)
			if applied != tc.wantSimulated {
				t.Fatalf("applied = %v, want %v", applied, tc.wantSimulated)
			}
			if !tc.wantSimulated {
				if string(out) != upstream {
					t.Errorf("body must be untouched for unmarked account")
				}
				return
			}
			if usage.ImageOutputTokens != 196 {
				t.Errorf("ImageOutputTokens = %d, want 196", usage.ImageOutputTokens)
			}
			if got := gjson.GetBytes(out, "quality").String(); got != "low" {
				t.Errorf("quality = %q, want low", got)
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd backend && go test ./internal/service/ -run TestMaybeSimulateOpenAIImagesUsage -v
```

预期：编译失败 `undefined: maybeSimulateOpenAIImagesUsage`。

- [ ] **Step 3: 实现**

在 `openai_images_usage_simulation.go` 增加账号门控的薄封装：

```go
// maybeSimulateOpenAIImagesUsage 仅对显式打了标记的账号执行模拟改写。
func maybeSimulateOpenAIImagesUsage(
	body []byte,
	account *Account,
	parsed *OpenAIImagesRequest,
) ([]byte, OpenAIUsage, bool) {
	if !account.SupportsOpenAIImagesUsageSimulation() {
		return body, OpenAIUsage{}, false
	}
	model := ""
	if parsed != nil {
		model = parsed.Model
	}
	return applyOpenAIImagesUsageSimulation(body, model, parsed)
}
```

在 `openai_images.go` 把 `handleOpenAIImagesNonStreamingResponse` 改为：

```go
func (s *OpenAIGatewayService) handleOpenAIImagesNonStreamingResponse(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	parsed *OpenAIImagesRequest,
) (OpenAIUsage, int, []string, error) {
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return OpenAIUsage{}, 0, nil, err
	}
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	contentType := "application/json"
	if s.cfg != nil && !s.cfg.Security.ResponseHeaders.Enabled {
		if upstreamType := resp.Header.Get("Content-Type"); upstreamType != "" {
			contentType = upstreamType
		}
	}

	// 模拟改写必须发生在写出之前，且响应体与计费 usage 同源。
	simulatedBody, simulatedUsage, simulated := maybeSimulateOpenAIImagesUsage(body, account, parsed)
	if simulated {
		body = simulatedBody
	}

	c.Data(resp.StatusCode, contentType, body)

	usage := simulatedUsage
	if !simulated {
		usage, _ = extractOpenAIUsageFromJSONBytes(body)
	}
	return usage, extractOpenAIImageCountFromJSONBytes(body), collectOpenAIResponseImageOutputSizesFromJSONBytes(body), nil
}
```

在唯一调用点（`openai_images.go:728`）补两个实参：

```go
		nonStreamUsage, nonStreamCount, nonStreamSizes, err := s.handleOpenAIImagesNonStreamingResponse(resp, c, account, parsed)
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd backend && go test ./internal/service/ -run TestMaybeSimulateOpenAIImagesUsage -v
```

预期：两个子用例均 PASS。

- [ ] **Step 5: 全量回归**

```bash
cd backend && go build ./... && go vet ./internal/service/
cd backend && go test ./internal/service/ ./internal/handler/ 2>&1 | tail -30
```

预期：编译与 vet 通过；失败用例与改动前基线一致（dev 分支已知 2 个预先存在的失败），无新增失败。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/service/openai_images.go \
        backend/internal/service/openai_images_usage_simulation.go \
        backend/internal/service/openai_images_usage_simulation_test.go
git commit -m "feat(images): 转发链路接入 usage 模拟（仅标记账号生效）"
```

---

## 上线前置事项

以下三项不属于代码任务，需在开启标记前完成：

- [ ] 用官方直连打一张 `{"size":"2880x2880","quality":"high"}`（约 $0.71），
      核实 Task 4 表中 4K 1:1 的 23658。偏差 >5% 则以实测值替换后补一次提交。
- [ ] 决定账号 1118（codex 管线）是否打标记。两点需一并权衡：
      ①它返回的是真实 token，打标记等于用模拟值覆盖真实值；
      ②**它的出图尺寸（`1672x941`、`1254x1254` 等）不被 16 整除**，
      而 `size` 字段回显的是真实尺寸，打了标记也仍能被下游识别为非官方。
      **默认建议：只给 adobe2api 账号（1115）打标记。**
- [ ] 在目标账号的 credentials 中加入 `"openai_images_usage_simulation": true`，
      灰度一个账号观察 `usage_logs.image_output_tokens` 与 `total_cost` 后再全量。

## 验收标准

- 打标记账号的响应体含 `background` / `output_format` / `quality` / `size` 四个官方字段，
  `usage` 为官方嵌套结构，`revised_prompt` 与 `gpt-image-2-codex` 不再出现。
- `usage_logs.image_output_tokens` 等于 Task 4 表对应格；
  `total_cost = image_output × 30/1M + text_input × 5/1M + image_input × 8/1M`。
- 改图请求的 `ImageInputTokens` 非 0 且等于 patch 公式结果。
- 未打标记账号的响应体与 usage 与改动前逐字节一致。
- `go build ./...`、`go vet ./internal/service/` 通过；测试无新增失败。
