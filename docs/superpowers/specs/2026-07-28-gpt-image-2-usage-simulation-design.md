# gpt-image-2 usage 模拟与响应对齐设计

日期：2026-07-28
状态：设计已确认，实现计划见 `docs/superpowers/plans/2026-07-28-gpt-image-2-usage-simulation.md`

参考实现：`docs/superpowers/specs/2026-07-14-gemini-pro-image-masking-design.md`（同类问题的既有方案）
数据来源：`docs/GPT_IMAGE_2_TOKEN_REFERENCE.md`（2026-07-28 实测）

## 背景与问题

运营方通过 sub2api 向下游供货 `gpt-image-2`，实际由三条不同上游承接：

| 链路 | 上游 | 响应特征 |
|---|---|---|
| A | 官方直连 API | `usage` 真实；带 `background`/`output_format`/`quality`/`size`；无 `model` |
| B | ChatGPT 账号代理（当前账号 1118） | `model: "gpt-image-2-codex"`、带 `revised_prompt`、`quality: "auto"`；出图尺寸不满足 16 整除 |
| C | adobe2api（Firefly 出图，当前账号 1115） | `usage` 为本地模拟，用的是 **gpt-image-1** 的 token 表；无 `background`/`output_format`/`quality`/`size` |

由此产生两个后果：

1. **计费偏差（主要）**：链路 C 的 `usage` 由 adobe2api 的 `build_image_usage` 按 gpt-image-1
   口径生成（方图 low/medium/high = 272/1056/4160），与 gpt-image-2 实际口径（196/1756/7024）
   数量级不符。`channel_model_pricing` 中 gpt-image 无计价行，sub2api 完全按上游返回的
   token 数结算 —— **上游填多少就收多少**，当前收费与官方口径脱节。
   另外 `openAIUsageFromGJSON` 不读取 `input_tokens_details.image_tokens`，
   `OpenAIUsage.ImageInputTokens` 恒为 0，改图的输入图像 token（$8/1M 档）完全未计费。
2. **上游暴露（次要）**：下游从响应体即可分辨自己被路由到了哪条链路
   —— 链路 B 的 `model: "gpt-image-2-codex"` 与 `revised_prompt`、链路 C 缺失的官方字段。

目标：**让链路 B/C 的响应在计费与响应体上都与官方 gpt-image-2 一致，下游不可分辨。**

## 实测基准

完整数据见 `docs/GPT_IMAGE_2_TOKEN_REFERENCE.md`。本设计直接依赖的结论：

**输出图像 token** 按 (比例, 档位) 查表，**不随像素线性变化**
（`3840x2160` 与 `2880x2880` 同为 8.29 MP，token 为 371 vs 659）。

**输入图像 token** 公式（官方直连与 codex 两条管线共 13 个实测点全部精确吻合）：

```
若 max(w,h) < 1024: 按 min(2.0, 1024/max(w,h)) 放大
patches = ceil(w/32) * ceil(h/32)
若 patches > 1536: 等比缩小直到 patches <= 1536
```

多张输入图 token 可加（实测 `704 + 1508 = 2212`）。

**质量倍率**：`medium ≈ low × 8.98`，`high = medium × 4.00`（精确），倍率与尺寸无关。

## 计费档位映射（已确认）

采用**按档位递进**：1K→low、2K→medium、4K→high。

> 这是自定义映射。官方的 `quality` 与 `size` 是两个正交维度，本方案把 adobe2api 的
> 尺寸档位借用为质量档位，以获得更陡的价格梯度。此偏离需在实现注释中写明。

最终 token 表（横版；竖版同值。**粗体为实测值**，其余由 low × 8.98 / × 35.9 推导）：

| 比例 | 1K（用 low） | 2K（用 medium） | 4K（用 high） |
|---|---|---|---|
| 1:1 | **196** | **3568** | 23658 |
| 5:4 | **157** | 2811 | 19027 |
| 4:3 | **144** | 2586 | 17232 |
| 3:2 | **134** | 2434 | 16119 |
| 16:9 | **106** | 1841 | **13342** |
| 21:9 | **82** | 1491 | 7898 |

对应单图成本（$30/1M）：1K $0.0025–0.0059、2K $0.0447–0.1070、4K $0.2369–0.7097。

> **上线前必须补测的一格**：4K 1:1（2880×2880）high = 23658 是外推链条最长的一格，
> 单图 $0.71，高于已实测的 4K 16:9（$0.40）。上线前用官方直连打一张
> `{"size":"2880x2880","quality":"high"}` 核实（成本约 $0.71），偏差 >5% 则以实测值替换。

## 方案（响应体改写 + 计费同源）

在 sub2api 把 images 响应写给下游之前拦截：命中触发条件时，用一个合成器生成官方规范的
`usage`，**同一份数据**既替换响应体又驱动计费，两者口径永远一致。

已否决的替代方案：

- 仅改计费、不改响应体 → 不满足隐藏上游的要求。
- 在 adobe2api 侧改 → 只能覆盖链路 C，链路 B 仍暴露；且计费真源应留在网关侧。
- 按像素折算 token → 实测证否（同像素不同比例 token 差 78%）。

### 生效范围（已确认：按账号配置开关）

新增账号 credentials 标记 `openai_images_usage_simulation`（布尔），仿照既有的
`openai_images_highres`（`backend/internal/service/account.go:93`）。

- 标记为真的账号：其 images 响应走模拟改写。
- 未标记账号（含官方直连）：完全不受影响，原样透传。
- 默认关闭。运维给 1115 / 1118 打标记后生效。

选择配置开关而非自动探测的理由：链路 B 返回的输入图像 token 是**真实**的，
自动探测容易误伤；由运维显式声明哪些账号需要伪装，边界清晰可回滚。

## 组件设计

新增文件 `backend/internal/service/openai_images_usage_simulation.go`。

### 1. 账号标记 `AccountEnablesOpenAIImagesUsageSimulation(a *Account) bool`

- 读取 `a.Credentials["openai_images_usage_simulation"]`，复用
  `account.go:1453` 附近既有的布尔解析方式（兼容 `true` / `"true"` / `1`）。
- `a == nil` 或缺失 → false。

### 2. 几何归一 `resolveOpenAIImageGeometry(body []byte, parsed *OpenAIImagesRequest) (openAIImageGeometry, bool)`

```go
type openAIImageGeometry struct {
    Width  int
    Height int
    Ratio  string // 归一后的横版比例键, 如 "16:9"
    Tier   string // "1K" / "2K" / "4K"
}
```

按优先级确定出图几何，任一步成功即继续：

1. 响应体 `size` 字段（形如 `"2048x2048"`）—— 链路 A/B 有，链路 C 无。
2. 解码 `data[0].b64_json` 的图像头取真实宽高（PNG 读 IHDR，JPEG/WebP 用
   `image.DecodeConfig`，只读头部不解全图）。
3. 请求侧 `parsed.Size`。
4. 全部失败 → 返回 `false`，放弃改写（见错误处理）。

得到 `Width/Height` 后：

- `Ratio` = 在 `{1:1, 5:4, 4:5, 4:3, 3:4, 3:2, 2:3, 16:9, 9:16, 21:9}` 中取与 `w/h`
  最接近者；竖版归一到对应横版键（4:5→5:4，3:4→4:3，2:3→3:2，9:16→16:9）。
- `Tier` = **按总像素**判定：`<= 1,600,000` → `1K`；`<= 4,500,000` → `2K`；其余 → `4K`。

> **为什么不按最长边**：21:9 的 2K 是 `3024x1296`，最长边 3024 会被任何合理的
> 「长边阈值」误判成 4K。已脚本验证：按最长边分档在 30 组尺寸中误判 1 组，
> 按总像素分档 **30/30 正确**。
>
> 三档的实测像素区间（间隔充裕，阈值不敏感）：
>
> | 档位 | 最小 | 最大 |
> |---|---|---|
> | 1K | 908,544（21:9 1456×624） | 1,048,576（1:1 1024×1024） |
> | 2K | 3,686,400（16:9 2560×1440） | 4,194,304（1:1 2048×2048） |
> | 4K | 5,854,464（21:9 3696×1584） | 8,294,400（1:1 2880×2880） |

### 3. token 表 `openAIImageOutputTokens(ratio, tier string) (int, bool)`

- 硬编码上文 6×3 表，键为归一后的横版比例 + 档位。
- 未命中 → `false`，放弃改写。

### 4. 输入图像 token `openAIImageInputTokens(w, h int) int`

- 实现上文 patch 公式。
- `w<=0 || h<=0` → 0。

### 5. 合成器 `synthesizeOpenAIImagesUsage(in openAIImagesUsageInput) openAIImagesSynthUsage`

输入：`{ratio, tier, textInputTokens, inputImageDims [][2]int, imageCount int}`
输出结构：

```go
type openAIImagesSynthUsage struct {
    TextInputTokens  int
    ImageInputTokens int   // sum(openAIImageInputTokens(w,h))
    ImageOutputTokens int  // openAIImageOutputTokens(ratio,tier) * imageCount
    InputTokens      int   // TextInputTokens + ImageInputTokens
    OutputTokens     int   // ImageOutputTokens
    TotalTokens      int   // InputTokens + OutputTokens
}
```

- `TextInputTokens` 沿用上游返回的 `input_tokens_details.text_tokens`；
  缺失时按提示词估算（CJK 1 token/字，其余 4 字符/token），下限 1。
- 输入图尺寸来自请求：JSON 形态取 `images[].image_url`（data URL 直接解码头部，
  http URL 不额外下载，用响应上游已返回的 `input_tokens_details.image_tokens`
  作为兜底）；multipart 形态取 `parsed.Uploads` 的图像头。

### 6. 响应体改写 `rewriteOpenAIImagesResponseBody(body []byte, model string, s openAIImagesSynthUsage, geom openAIImageGeometry) ([]byte, bool)`

用 sjson 就地改写，**不触碰 `data[].b64_json` / `data[].url`**：

- 整体替换 `usage` 为官方结构：
  ```json
  {
    "input_tokens": N, "input_tokens_details": {"image_tokens": N, "text_tokens": N},
    "output_tokens": N, "output_tokens_details": {"image_tokens": N, "text_tokens": 0},
    "total_tokens": N
  }
  ```
- 补齐官方字段：`background`（缺失时 `"opaque"`）、`output_format`（按实际图像
  MIME，缺失时 `"png"`）、`quality`（按 tier 映射：1K→`low`、2K→`medium`、4K→`high`）、
  `size`（`"WxH"`，取归一后的真实出图尺寸）。
- 抹除上游指纹：删除 `data[].revised_prompt`；`model` 设为**下游请求的模型名**。

> **已知差异 1（model 字段）**：官方 `/v1/images/generations` 响应**不含** `model` 字段，
> 本方案保留并设为请求模型名而非删除 —— 删除可能破坏依赖该字段的下游客户端，
> 收益不抵风险。该字段不再泄露上游身份，符合目标。
>
> **已知差异 2（codex 尺寸无法完全伪装）**：`size` 字段回显的是**真实出图尺寸**。
> 链路 C（adobe2api）的出图尺寸本就 100% 符合官方约束，无问题；但链路 B（codex）
> 会产出 `1672x941`、`1254x1254` 这类**不被 16 整除**的尺寸，回显即暴露上游非官方。
> 两种取舍都不完美：
>
> - 回显真实尺寸 → `size` 与客户端解码 b64 得到的像素一致，但暴露非官方尺寸；
> - 回显档位的规范尺寸 → 看起来官方，但与实际图像像素对不上，客户端一比对就穿帮。
>
> 本方案选**回显真实尺寸**（自洽优先）。因此：**建议只给 adobe2api 账号打标记**。
> 若确需给 codex 账号打标记，需接受 `size` 字段可被识别；要根治只能在网关侧
> 重采样图像到规范尺寸，成本与画质损失都不划算，不在本次范围。

### 7. 编排 `applyOpenAIImagesUsageSimulation(body []byte, model string, parsed *OpenAIImagesRequest) (newBody []byte, usage OpenAIUsage, applied bool)`

串起 2→6，返回改写后的 body 与同源 `OpenAIUsage`：

```go
usage = OpenAIUsage{
    InputTokens:       s.InputTokens,
    ImageInputTokens:  s.ImageInputTokens,
    OutputTokens:      s.OutputTokens,
    ImageOutputTokens: s.ImageOutputTokens,
}
```

任一环节失败返回 `applied=false`，调用方回退原 body + 原 usage。

## 数据流与落点

改写发生在 **`handleOpenAIImagesNonStreamingResponse`**
（`backend/internal/service/openai_images.go:891`）——
该函数是唯一同时持有「完整响应体」和「写出前时机」的位置：

```go
// 现状（:903-906）
c.Data(resp.StatusCode, contentType, body)
usage, _ := extractOpenAIUsageFromJSONBytes(body)
return usage, extractOpenAIImageCountFromJSONBytes(body), collect...(body), nil
```

改为：在 `c.Data` **之前**尝试改写，写出改写后的 body，并返回同源 usage。
函数需新增入参 `account *Account` 与 `parsed *OpenAIImagesRequest`
—— 该函数**只有一个调用方** `forwardOpenAIImagesAPIKey`（`openai_images.go:728`），
两个参数在调用点均已在作用域内，改签名成本极低。

`OpenAIForwardResult.Usage` 随之变为合成 usage，计费链路无需改动。

**一致性不变量**：`extractOpenAIUsageFromJSONBytes(newBody)` 必须等于返回的 usage。
该不变量需有专门单测覆盖。

### 附带修复：`ImageInputTokens` 从未被填充

`openAIUsageFromGJSON`（`openai_gateway_response_handling.go:747`）未读取
`input_tokens_details.image_tokens`，导致所有 images 流量的输入图像 token 不计费。
本方案在该函数中补上读取，使**官方直连链路也能正确计费输入图像**
（`billing_service.go:978` 已支持 `ImageInputTokens > 0` 的分单价路径）。

这是独立于模拟功能的既有缺陷修复，应作为单独一个任务先行落地。

## 错误处理与边界

- **账号未打标记** → 完全不进入本流程，零影响。
- **几何无法确定**（无 `size`、b64 解码失败、请求也无 size）→ 放弃改写，原样透传 + 原 usage。
- **token 表未命中**（比例/档位异常）→ 放弃改写。
- **`data` 为空或响应非 JSON** → 放弃改写。
- **sjson 写入失败** → 放弃改写，返回原 body。
- **多图（`n>1`）** → 输出 token 按单图值 × 实际图片数；图片数取 `data` 数组长度。
- **`response_format=url`** → `data[].url` 不动，仅改 usage 与官方字段。
- 任何失败路径都**不得使请求失败**，一律降级为透传。

## 作用域限定（YAGNI）

- **仅覆盖非流式路径**。adobe2api 与 codex 管线当前都不对 images 端点做 SSE 流式
  出图；`handleOpenAIImagesStreamingResponse` 不在本次范围。若将来需要，
  合成器可直接复用，只需在末个含 `usage` 的分块上改写。
- **不覆盖 `/v1/responses` 的 image_generation 工具路径**（`openai_images_responses.go`），
  该路径当前只服务 OAuth 账号，走的是官方口径。
- **不实现 partial images 的 +100 token/张**（官方文档所述，未实测，且当前无流式流量）。
- **不实现缓存图像输入**（$2/1M 档）的识别，触发条件未知。

## 测试计划（TDD）

单元测试 `openai_images_usage_simulation_test.go`：

1. `AccountEnablesOpenAIImagesUsageSimulation`：标记为 `true`/`"true"`/`1` 时为真；
   缺失、`false`、nil account 为假。
2. `openAIImageInputTokens`：覆盖参考文档 §6 的全部 11 个实测点，逐一断言精确相等。
3. `openAIImageOutputTokens`：6 个比例 × 3 档位命中；竖版比例归一到横版同值；
   未知比例/档位返回 false。
4. `resolveOpenAIImageGeometry`：
   - 响应含 `size` → 直接采用；
   - 无 `size` 但有 PNG b64 → 解出真实宽高；
   - 两者皆无但请求有 `size` → 用请求值；
   - 全无 → `ok=false`。
   - 档位判定：`1024x1024`→1K、`3024x1296`→2K、`3696x1584`→4K（验证三档不重叠）。
5. `synthesizeOpenAIImagesUsage`：`TotalTokens = InputTokens + OutputTokens`；
   `InputTokens = TextInputTokens + ImageInputTokens`；多张输入图 token 可加；
   `n=2` 时输出 token 翻倍。
6. `rewriteOpenAIImagesResponseBody`：
   - 输入一份链路 C（adobe2api）响应 → 输出含官方四字段、`usage` 为合成结构；
   - 输入一份链路 B（codex）响应 → `revised_prompt` 被删除、`model` 变为请求模型名；
   - `data[].b64_json` 原样保留（逐字节比对）。
7. **一致性不变量**：`extractOpenAIUsageFromJSONBytes(rewrite 后的 body)` 等于
   `applyOpenAIImagesUsageSimulation` 返回的 usage。
8. 降级路径：几何失败 / 表未命中 / 非法 JSON / 空 data → `applied=false` 且 body 原样。
9. `openAIUsageFromGJSON` 补丁：`input_tokens_details.image_tokens` 被正确读入
   `ImageInputTokens`；缺失时为 0（不回归既有 chat/vision 流量）。

集成测试（`openai_images_test.go` 既有夹具）：

10. 打标记账号 + adobe2api 形态响应 → 下游收到的 body 含官方字段，
    `OpenAIForwardResult.Usage.ImageOutputTokens` 等于表值。
11. 未打标记账号 → body 与 usage 均与改动前逐字节一致。

## 验收标准

- 打标记账号返回的 images 响应，下游看到的 `usage` 结构与官方 `/v1/images/generations`
  完全一致（字段名、嵌套层级、`output_tokens_details.text_tokens=0`）。
- `background` / `output_format` / `quality` / `size` 四个官方字段齐备；
  `revised_prompt` 与 `gpt-image-2-codex` 不再出现在响应体中。
- `usage_logs.image_output_tokens` 等于上文 token 表对应格；
  `total_cost` = `image_output_tokens × 30/1M + text_input × 5/1M + image_input × 8/1M`。
- 改图请求的 `ImageInputTokens` 非 0 且等于 patch 公式结果。
- 未打标记账号（含官方直连）行为零变化。
- 全部新单测通过；`internal/service`、`internal/handler` 既有测试不回归
  （注意 dev 分支存在 2 个预先存在的失败用例，需与基线对比而非绝对通过）。

## 上线前置事项

1. 补测 4K 1:1（2880×2880）high 的真实 token，校正表中 23658。
2. 确认 1115 / 1118 是否都打标记（1118 返回真实 token，打标记意味着用模拟值覆盖它）。
3. `docs/*` 在 `.gitignore` 中，本文件需 `git add -f` 入库。
