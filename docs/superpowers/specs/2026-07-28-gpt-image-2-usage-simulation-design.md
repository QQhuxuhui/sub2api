# gpt-image-2 usage 模拟与响应对齐设计

日期：2026-07-28（v2，采纳代码评审的 5 项 P1 后重写）
状态：设计已确认，实现计划见 `docs/superpowers/plans/2026-07-28-gpt-image-2-usage-simulation.md`

参考实现：`docs/superpowers/specs/2026-07-14-gemini-pro-image-masking-design.md`
数据来源：`docs/GPT_IMAGE_2_TOKEN_REFERENCE.md`

## 背景与问题

运营方通过 sub2api 向下游供货 `gpt-image-2`，实际由三条不同上游承接：

| 链路 | 上游 | 响应特征 |
|---|---|---|
| A | 官方直连 API | `usage` 真实；带 `background`/`output_format`/`quality`/`size`；无 `model` |
| B | ChatGPT 账号代理（账号 1118） | `model: "gpt-image-2-codex"`、带 `revised_prompt`、`quality: "auto"`；出图尺寸不满足 16 整除 |
| C | adobe2api（Firefly 出图，账号 1115） | `usage` 为本地模拟，用的是 **gpt-image-1** 的 token 表；无官方四字段 |

后果：

1. **计费偏差**：链路 C 的 `usage` 按 gpt-image-1 口径生成，与 gpt-image-2 实际口径数量级不符。
   `channel_model_pricing` 中 gpt-image 无计价行，sub2api 完全按上游返回的 token 数结算。
   另外 `openAIUsageFromGJSON` 不读 `input_tokens_details.image_tokens`，
   改图的输入图像 token（$8/1M 档）**从未计费**。
2. **上游暴露**：下游可从响应体分辨路由到了哪条链路。

目标：**让被标记账号的响应在计费与响应体结构上与官方 gpt-image-2 一致。**

## 核心口径：三维 token 表

官方的 `size` 与 `quality` 是**两个正交维度**，token 由二者共同决定。
早期草案把尺寸档位借用为质量档位（1K→low、2K→medium、4K→high），会导致
「只传 `size=2048x2048` 不传 quality」这类请求被按 medium 计费 —— 官方此时默认 low，
差 9 倍。**本设计改为真实三维查表**：

```
tokens = TABLE[ratio][sizeTier][quality]
```

- `ratio` / `sizeTier`：由**实际出图尺寸**精确匹配得出（见下）。
- `quality`：取**请求值**，归一后为 `low` / `medium` / `high`；缺省或 `auto` → `low`。

`auto → low` 已实测确认：`1024x1024` + `quality:"auto"` 返回 196 token，
且响应回显 `quality: "low"`（官方自身即做此归一）。

## 实测基准

**base（low 档）token 表**，18 格全部实测；竖版与横版同值（已验证 720×1280 = 1280×720）：

| 比例 | 1K | 2K | 4K |
|---|---|---|---|
| 1:1 | 1024×1024 **196** | 2048×2048 **397** | 2880×2880 **659** |
| 5:4 | 1120×896 **157** | 2240×1792 **313** | 3200×2560 **530** |
| 4:3 | 1152×864 **144** | 2304×1728 **288** | 3264×2448 **480** |
| 3:2 | 1248×832 **134** | 2496×1664 **271** | 3504×2336 **449** |
| 16:9 | 1280×720 **106** | 2560×1440 **205** | 3840×2160 **371** |
| 21:9 | 1456×624 **82** | 3024×1296 **166** | 3696×1584 **220** |

**medium / high 档同样 18 格全部实测**（2026-07-28 补齐，共 54 格无推导值）：

| 比例 | 1K low/med/high | 2K low/med/high | 4K low/med/high |
|---|---|---|---|
| 1:1 | 196 / 1756 / 7024 | 397 / 3568 / 14272 | 659 / 5930 / 23719 |
| 5:4 | 157 / 1370 / 5551 | 313 / 2743 / 11115 | 530 / 4648 / 18835 |
| 4:3 | 144 / 1294 / 5176 | 288 / 2584 / 10336 | 480 / 4316 / 17264 |
| 3:2 | 134 / 1167 / 4667 | 271 / 2363 / 9452 | 449 / 3912 / 15645 |
| 16:9 | 106 / 947 / 3787 | 205 / 1843 / 7370 | 371 / 3336 / 13342 |
| 21:9 | 82 / 733 / 2863 | 166 / 1492 / 5825 | 220 / 1980 / 7729 |

> **倍率按比例恒定、跨档位不变**，可用作数据自洽性校验（不用于计费）：
> med/low 组内差 <0.7%，high/med 组内差 <0.05%；但跨比例差异显著
> （med/low 从 3:2 的 8.71 到 21:9 的 9.00，high/med 从 21:9 的 3.905 到 5:4 的 4.052）。
> 这正说明**不可跨比例外推** —— 早期用全局倍率推导时，21:9 的 1K high
> 推得 2944 而实测 2863（−2.8%）。现全表实测，该风险已消除。

**输入图像 token 公式**（官方直连与 codex 两条管线共 11 个实测点全部精确吻合）：

```
若 max(w,h) < 1024: 按 min(2.0, 1024/max(w,h)) 放大
patches = ceil(w/32) * ceil(h/32)
若 patches > 1536: 等比缩小直到 patches <= 1536
```

多张输入图 token 可加（实测 `704 + 1508 = 2212`）。

## 生效边界（四道闸门，全部通过才模拟）

早期草案只有账号标记一道闸门，会误伤同账号上的其它模型、异常尺寸与未验证参数。
现改为四道，任一不满足即 `applied=false` 原样透传：

### 闸门 1：账号标记

新增 credentials 标记 `openai_images_usage_simulation`（布尔），仿照既有的
`openai_images_highres`（`backend/internal/service/account.go:93`）。默认关闭。

### 闸门 2：模型白名单

`isOpenAIImageGenerationModel`（`openai_images.go:457`）放行的是
**所有 `gpt-image-*` 前缀 + 全部 Grok 生图模型**。若账号同时承载 `gpt-image-1`
或 `grok-imagine`，会被错误套用 gpt-image-2 的表。

故显式白名单：仅 `gpt-image-2` 及确认过的版本别名（`gpt-image-2-<date>` 形态）。
其余一律不模拟。

### 闸门 3：请求能力门控

参考数据明确未覆盖的形态一律不模拟（`docs/GPT_IMAGE_2_TOKEN_REFERENCE.md` §12）：

| 条件 | 理由 |
|---|---|
| `parsed.Stream == true` | 流式与 partial images 的 token 规则未实测 |
| `parsed.PartialImages != nil` | 官方称每张 partial 额外 +100 token，未实测 |
| `parsed.N > 1` 或 `data` 长度 ≠ 1 | 多图 token 是否线性未实测 |
| `parsed.HasMask == true` | mask 作为额外输入图的计法未实测 |
| `parsed.Background` 非空且 ≠ `"opaque"` | transparent 未实测；且不得把用户要的 transparent 改写成 opaque |
| `parsed.OutputFormat` 非空且 ≠ `"png"` | JPEG/WebP 未实测 |
| `parsed.OutputCompression != nil` / `InputFidelity` 非空 | 未实测 |

### 闸门 4：几何精确匹配

**不做最近比例吸附**。早期草案对任意正尺寸都返回最近比例与某一档位，
使降级路径形同虚设 —— 1254×1254（codex 出图）会被吸附成 1:1/1K 拿 196，
而该尺寸的真实 token 未知；10:1 这类比例会被吸附成 21:9。

改为：把实际出图尺寸按 `"WxH"` 在**已知 30 组精确尺寸**（10 比例 × 3 档，含横竖版）
中查表；命中才继续，未命中直接 `applied=false`。

出图尺寸的取值优先级：

1. 响应体 `size` 字段；
2. 解码首张 `b64_json` 的图像头（**单次 base64 解码同时取宽高与格式**，
   不重复解码，避免 4K 并发下的内存放大）；
3. 请求侧 `parsed.Size`。

## 输入图像 token 的取数与兜底

`openai_images.go:283` 明确支持远程 `images[].image_url`，网关侧**不会**下载这些图。
因此：

1. 逐张尝试取尺寸：data URL 与 multipart 上传可本地解码；
   `parsed.Uploads` 的 `Width`/`Height` 来自 multipart 头部，非 0 时可省一次解码。
2. **只要有任意一张输入图尺寸未知**（典型：远程 http URL），
   放弃逐张求和，改用上游响应的 `usage.input_tokens_details.image_tokens` 聚合值。
3. 上游也没有可信聚合值（缺失或为 0）且确实存在输入图 → `applied=false`。

纯文生图（无输入图）不受此约束。

## 组件设计

新增文件 `backend/internal/service/openai_images_usage_simulation.go`：

| 函数 | 职责 |
|---|---|
| `(a *Account) SupportsOpenAIImagesUsageSimulation() bool` | 闸门 1 |
| `isSimulatableOpenAIImagesModel(model string) bool` | 闸门 2 |
| `openAIImagesRequestSimulatable(parsed *OpenAIImagesRequest) bool` | 闸门 3 |
| `lookupOpenAIImageSize(w, h int) (openAIImageGeometry, bool)` | 闸门 4，30 组精确表 |
| `decodeImageMeta(encoded string) (w, h int, format string, ok bool)` | 单次解码取宽高+格式 |
| `normalizeOpenAIImageQuality(raw string) (string, bool)` | `auto`/空 → `low`；未知值 → false |
| `openAIImageOutputTokens(ratio, tier, quality string) (int, bool)` | 三维查表 |
| `openAIImageInputTokens(w, h int) int` | patch 公式 |
| `resolveOpenAIImagesInputTokens(body []byte, parsed *OpenAIImagesRequest) (int, bool)` | 逐张求和 + 上游兜底 |
| `synthesizeOpenAIImagesUsage(...) (openAIImagesSynthUsage, bool)` | 合成 |
| `rewriteOpenAIImagesResponseBody(...) ([]byte, bool)` | 响应体改写 |
| `applyOpenAIImagesUsageSimulation(...)` / `maybeSimulateOpenAIImagesUsage(...)` | 编排与门控 |

### 响应体改写规则

- 整体替换 `usage` 为官方结构（`input_tokens` / `input_tokens_details{image,text}` /
  `output_tokens` / `output_tokens_details{image,text:0}` / `total_tokens`）。
- 补齐官方字段：
  - `background`：**沿用上游值**；上游缺失且请求也未指定时才写 `"opaque"`。
    绝不覆盖用户显式要求（闸门 3 已挡掉非 opaque 请求，此处是双保险）。
  - `output_format`：由单次解码得到的实际图像格式填写。
  - `quality`：写**归一后的请求 quality**（与计费同源），而非由尺寸档位反推。
  - `size`：写真实出图尺寸。
- 抹除上游指纹：删除 `data[].revised_prompt`。

> **已知差异 1（model 字段）**：官方响应**不含** `model`，本方案保留并设为请求模型名
> 而非删除 —— 删除可能破坏依赖该字段的下游客户端。故目标表述为
> **「结构与官方一致」而非「逐字节不可分辨」**。
>
> **已知差异 2（codex 尺寸）**：链路 B 的出图尺寸（`1672x941` 等）不被 16 整除，
> 闸门 4 会因查不到而直接 `applied=false`。即**codex 账号即使打了标记也基本不会被模拟**，
> 这是预期行为。建议只给 adobe2api 账号打标记。

## 数据流与落点

改写发生在 `handleOpenAIImagesNonStreamingResponse`（`openai_images.go:891`）——
唯一同时持有完整响应体与写出前时机的函数，且**只有一个调用方**
`forwardOpenAIImagesAPIKey`（`openai_images.go:728`），`account` 与 `parsed` 在调用点均在作用域内。

在 `c.Data` **之前**尝试改写，写出改写后的 body，并返回同源 usage。
`OpenAIForwardResult.Usage` 随之变为合成 usage，计费链路无需改动。

**一致性不变量**：`extractOpenAIUsageFromJSONBytes(newBody)` 必须等于返回的 usage，
由专门单测覆盖。

### 附带修复：`ImageInputTokens` 从未被填充

`openAIUsageFromGJSON`（`openai_gateway_response_handling.go:747`）未读取
`input_tokens_details.image_tokens`，导致所有 images 流量的输入图像 token 不计费
（官方直连链路同样受影响）。作为独立任务先行落地。

## 作用域限定（YAGNI）

- 仅覆盖**非流式**路径（闸门 3 已排除 stream）。
- 不覆盖 `/v1/responses` 的 image_generation 工具路径。
- 不实现 partial images、transparent、JPEG/WebP、`n>1`、缓存图像输入 —— 全部由闸门 3 挡掉。

## 测试计划（TDD）

单元测试 `openai_images_usage_simulation_test.go`（账号标记测试也放这里；
**注意仓库没有 `account_test.go`**，highres 的同类测试在 `openai_images_highres_test.go`）：

1. 账号标记解析：bool / 字符串 / 数字 / 缺失 / nil。
2. 模型白名单：`gpt-image-2` 及日期别名为真；`gpt-image-1`、`gpt-image-3`、
   `grok-imagine`、`grok-imagine-edit`、空串为假。
3. 请求能力门控：逐项构造 stream / partial / n>1 / mask / transparent /
   jpeg / compression / fidelity，均应为 false；干净请求为 true。
4. `openAIImageInputTokens`：11 个实测点逐一精确断言。
5. `lookupOpenAIImageSize`：30 组已知尺寸全部命中且档位正确；
   `1254x1254`、`1672x941`、`1000x100`、`0x0` 均未命中。
6. `normalizeOpenAIImageQuality`：空/`auto`→low；low/medium/high 原样；
   未知值（如 `hd`、`4k`）→ false。
7. `openAIImageOutputTokens`：抽取的实测格逐一精确断言；未知比例/档位/quality 返回 false。
8. `resolveOpenAIImagesInputTokens`：全部本地可解码 → 逐张求和；
   含远程 URL → 用上游聚合值；含远程 URL 且上游无值 → false；无输入图 → 0,true。
9. `rewriteOpenAIImagesResponseBody`：官方字段齐备、`quality` 等于请求归一值、
   `background` 沿用上游、`revised_prompt` 被删、`b64_json` 逐字节保留。
10. **一致性不变量**：`extractOpenAIUsageFromJSONBytes(改写后 body)` == 计费 usage。
11. 降级路径：四道闸门各自触发时 body 逐字节不变。

集成测试（复用 `openai_images_test.go` 既有夹具）：

12. 打标记账号 + adobe2api 形态响应 → 下游 body 含官方字段，
    `OpenAIForwardResult.Usage.ImageOutputTokens` 等于表值。
13. 未打标记账号 → body 与 usage 与改动前逐字节一致。
14. 远程 `images[].image_url` 的 edits → 走上游聚合值，不为 0。
15. 未知尺寸（1254×1254）→ 不模拟。
16. 非 gpt-image-2 模型（`gpt-image-1`、`grok-imagine`）→ 不模拟。

## 验收标准

- 打标记账号的响应 `usage` 结构与官方一致；`background`/`output_format`/`quality`/`size` 齐备；
  `revised_prompt` 与 `gpt-image-2-codex` 不再出现。
- `quality` 回显等于请求归一值；只传 `size` 不传 quality 时按 **low** 计费（与官方一致）。
- `usage_logs.image_output_tokens` 等于三维表对应格；
  `total_cost = image_output × 30/1M + text_input × 5/1M + image_input × 8/1M`。
- 远程 URL 改图的 `ImageInputTokens` 非 0。
- 四道闸门任一不满足时，响应体与 usage 与改动前逐字节一致。
- 全部新单测通过；既有测试无新增失败（dev 分支已知 2 个预先存在的失败）。
