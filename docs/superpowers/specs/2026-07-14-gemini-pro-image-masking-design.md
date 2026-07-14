# Gemini pro 生图响应伪装（pro→flash 映射隐藏与计费修正）

日期：2026-07-14
状态：设计已确认，待写实现计划

## 背景与问题

运营方通过 sub2api 向下游供货，售卖 `gemini-3-pro-image-preview`，但上游（一个 new-api 实例）做了模型映射，把请求改写成 `gemini-3.1-flash-image` 出图。两个模型的图像观感接近，但**接口返回的 `usageMetadata` 结构不同**，导致两个后果：

1. **计费偏差（主要）**：NanoBanana 分组按 **token 计费**。pro 的价值几乎全在图像 token 上（`output_cost_per_image_token` 是文本 token 单价的 10 倍）。sub2api 的 `extractGeminiUsage` 从 `candidatesTokensDetails` 的 IMAGE 明细读取 `ImageOutputTokens`；而该 new-api 链路把明细抹平，返回扁平 usageMetadata（无 `candidatesTokensDetails`、无 `thoughtsTokenCount`），使 `ImageOutputTokens=0` → 图像部分几乎不收费。实测按 pro 2K 估算约**少收 80%**（应收 ~$0.138，实收 ~$0.026）。
2. **映射暴露（次要）**：sub2api 原生透传响应体，下游能从响应里的 `modelVersion: gemini-3.1-flash-image` 和 token 结构直接看出拿到的是 flash 而非 pro。

目标：**让映射后的响应在计费和响应体上都与真 pro 完全一致，下游不可分辨。**

## 实测基准（amutes 权威源，Gemini 原生 generateContent，每档 3 组样本）

pro 各档位 `usageMetadata` 特征：

| 档位 | IMAGE token（确定值） | candidates 文本部分 | thoughtsTokenCount（浮动） | serviceTier |
|---|---|---|---|---|
| 1K | **1120** | 78–90 | 115–140 | standard |
| 2K | **1120** | 80–100 | 145–165 | standard |
| 4K | **2000** | 92–112 | 150–170 | standard |

结构规律（真源确认）：
- `candidatesTokensDetails` 仅含一项 `{modality: "IMAGE", tokenCount}`（文本描述 token 混在 `candidatesTokenCount` 里，不单列）。
- `promptTokensDetails = [{modality: "TEXT", tokenCount: promptTokenCount}]`。
- `thoughtsTokenCount` 独立于 candidates。
- `totalTokenCount = promptTokenCount + candidatesTokenCount + thoughtsTokenCount`。
- `serviceTier = "standard"`。
- `promptTokenCount` 跟随输入文本（跨模型的文本 tokenization 基本一致）。

补充观察：flash 本身在正常上游（amutes）也返回带明细的 usageMetadata（2K 时 IMAGE=1680）。因此「明细缺失」是该 new-api 链路特有的，不是 flash 模型固有格式。故伪装逻辑不能只在「明细缺失」时触发，也要覆盖「明细存在但数值是 flash 的」情形——统一以 `modelVersion` 是否为真 pro 作为主判据。

## 方案（响应体改写 + 计费同源）

在 sub2api 把 Gemini 原生生图响应转发给下游之前拦截：检测到「下游请求的是 pro 生图模型，但响应不是真 pro」时，用一个合成器生成 pro 规范的 `usageMetadata`，**同一份数据**既替换响应体（`usageMetadata` + `modelVersion`），又驱动计费。下游看到的 usageMetadata 与其被计费的口径一致。

已否决的替代方案：
- 仅改计费、不改响应体 → 违背隐藏映射的要求（`modelVersion` 仍暴露）。
- 在上游 new-api 侧修 → 不在 sub2api 掌控内，用户要的是网关侧兜底。

### 生效范围

自动检测、全局生效，无需配置开关。真 pro 流量不受影响。

## 组件设计

新增文件 `backend/internal/service/gemini_pro_image_mask.go`，包含以下独立单元：

### 1. 模型识别 `isGeminiProImageModel(model string) bool`
- 匹配 `gemini-3-pro-image` 前缀（覆盖 `gemini-3-pro-image-preview`、`gemini-3-pro-image` 等变体）。
- 不匹配 flash 生图、pro 文本模型、其它平台模型。

### 2. 档位画像 `geminiProImageProfile(tier string) proImageProfile`
- 输入归一化档位字符串（"1K"/"2K"/"4K"，大小写不敏感）。
- 返回该档位的 `{imageTokens, textMin, textMax, thoughtsMin, thoughtsMax}`：
  - 1K：imageTokens=1120，text 78–92，thoughts 115–140
  - 2K：imageTokens=1120，text 80–100，thoughts 145–165
  - 4K：imageTokens=2000，text 92–112，thoughts 150–170
- 未知档位回落到 2K 画像（与 `NormalizeImageBillingTierOrDefault` 的默认一致）。

### 3. 合成器 `synthesizeGeminiProImageUsage(tier string, promptTokens int) geminiSynthUsage`
- 单一真源，产出结构化 usage：
  - `imageTokens` = 画像确定值。
  - `textTokens`、`thoughtsTokens` = 在画像区间内随机取值（每请求独立）。
  - `promptTokens` = 传入值（沿用上游返回的 `promptTokenCount`；缺失/为 0 时用小默认值，见错误处理）。
  - `candidatesTokenCount = imageTokens + textTokens`。
  - `totalTokenCount = promptTokens + candidatesTokenCount + thoughtsTokens`。
- 随机源做成**可注入的函数变量**（`var geminiProImageRand = ...`），测试用固定种子保证确定性；生产用随机种子。

### 4. 响应体改写 `maskGeminiProImageResponseBody(body []byte, model string, synth geminiSynthUsage) []byte`
- 用 sjson 将响应体的 `modelVersion` 设为 `model`（下游请求的 pro 名），并整体替换 `usageMetadata` 为合成对象：
  - `promptTokenCount`、`promptTokensDetails=[{TEXT, promptTokenCount}]`
  - `candidatesTokenCount`、`candidatesTokensDetails=[{IMAGE, imageTokens}]`
  - `thoughtsTokenCount`、`totalTokenCount`、`serviceTier="standard"`
- 不触碰 `candidates` 内的图像数据（`inlineData`）等其余字段。

### 5. 计费 usage 映射 `synthToClaudeUsage(synth geminiSynthUsage) *ClaudeUsage`
- 产出与响应体同源的 `ClaudeUsage`：
  - `InputTokens = promptTokens`
  - `OutputTokens = candidatesTokenCount + thoughtsTokens`
  - `ImageOutputTokens = imageTokens`
- 流入 `ForwardResult.Usage`；因 `ForwardResult.Model = originalModel`（pro），计费按 pro 单价计算，图像 token 恢复为 1120/2000。

### 6. 触发判定 `shouldMaskGeminiProImage(respBody []byte, model string) bool`
- 前置：`isGeminiProImageModel(model)` 为真。
- 判据：响应不是「真 pro」即触发。「真 pro」定义为 `modelVersion` 以 pro 模型名开头 **且** `candidatesTokensDetails` 含 IMAGE 明细。任一不满足 → 触发伪装。
- 效果：真 pro 响应（modelVersion 匹配 + 有 IMAGE 明细）不触发；flash 响应（无论明细是否被抹平，modelVersion 都是 flash）均触发。

## 数据流与落点

改写发生在原生透传路径 `ForwardNative`（`gemini_messages_compat_service.go:1112`）：

1. 在调用响应处理器前，`ForwardNative` 计算 `imageTier = normalizeOpenAIImageSizeTier(extractImageInputSize(body))`，并把伪装参数 `{model: originalModel, tier: imageTier, enabled: isGeminiProImageModel(originalModel)}` 传入下列处理器。
2. 三个写响应体的分支都需处理：
   - 非流式：`handleNativeNonStreamingResponse`（`:2503`）——读到 `respBody` 后、`c.Data` 写出前，若 `enabled && shouldMaskGeminiProImage(respBody, model)`：合成一次 → 改写 body → 写出改写后的 body → 返回同源 `ClaudeUsage`（而非 `extractGeminiUsage(respBody)`）。
   - 流式：`handleNativeStreamingResponse`（`:2540`）——对末个含 `usageMetadata` 的 SSE 分块做同样改写（`modelVersion` 在分块内也一并改）。
   - OAuth 聚合流：`ForwardNative` 内 `useUpstreamStream` 分支（`:1587`，`collectGeminiSSE` 后 `c.Data` 写出）——对聚合后的 JSON 同样改写。
3. `ForwardResult.Usage` 用合成 usage；`Model` 保持 `originalModel`。计费链路无需改动即按 pro 结算。

一致性保证：每请求只合成一次，响应体与计费共用，`extractGeminiUsage(改写后 body)` 应等于计费所用 usage。

## 错误处理与边界

- **档位未知/缺失**：`extractImageInputSize` 拿不到尺寸时 `normalizeOpenAIImageSizeTier` 默认 2K，画像随之走 2K。符合 Gemini 生图默认。
- **上游 `promptTokenCount` 缺失或为 0**：合成时给一个小默认值（如按请求文本估算或固定下限），避免 total 明显异常。
- **响应体非预期结构（无 `usageMetadata` 或解析失败）**：仍按「pro 请求」注入合成 usageMetadata（sjson set 幂等），保证下游看到 pro 结构；改写失败则回退为原 body + 原 usage（不因伪装导致请求失败）。
- **非生图 pro 请求**：`isGeminiProImageModel` 已隔离；`ImageCount` 仍由既有 `isImageGenerationModel` 判定，互不影响。

## 作用域限定（YAGNI）

- 仅覆盖**原生 Gemini 透传路径**（`ForwardNative`），即下游以 Gemini 格式请求、期望 Gemini `usageMetadata` 的场景——这正是当前流量形态。
- Anthropic-compat 路径（`Forward`，`:582`，把 Gemini 转 Claude 格式）不在本次范围：该路径下下游看到的是 Claude 结构、无 `modelVersion` 暴露问题；如未来需要按 pro 修正其计费，可复用合成器，作为后续项。
- 不覆盖 antigravity 路径（用户此场景走 new-api APIKey 账号）。

## 测试计划（TDD）

单元测试（`gemini_pro_image_mask_test.go`）：
1. `isGeminiProImageModel`：pro 生图各变体为真；flash 生图、pro 文本、其它模型为假。
2. `geminiProImageProfile`：1K/2K/4K 返回正确 imageTokens 与区间；未知档位回落 2K。
3. `synthesizeGeminiProImageUsage`（固定种子）：imageTokens 精确等于档位值；text/thoughts 落在区间；`total = prompt + candidates + thoughts`；candidates = image + text。
4. `shouldMaskGeminiProImage`：真 pro（modelVersion 匹配 + 有 IMAGE 明细）→ false；flash 抹平明细 → true；flash 带明细 → true；modelVersion 匹配但缺 IMAGE 明细 → true。
5. `maskGeminiProImageResponseBody`：给定 flash 响应 JSON，输出 `modelVersion` = pro 名、`usageMetadata` 为合成结构、`candidates` 图像数据原样保留。
6. `synthToClaudeUsage`：`ImageOutputTokens` = 档位值，`OutputTokens = candidates + thoughts`。
7. 一致性：`extractGeminiUsage(改写后 body)` 等于 `synthToClaudeUsage(同一 synth)`。
8. 计费口径（若可低成本构造）：映射后按 pro 单价结算，图像 token 计入。

## 验收标准

- 对 `gemini-3-pro-image-preview` 的映射请求，下游响应体的 `modelVersion` 为 pro 名，`usageMetadata` 具备 pro 的完整结构（含 IMAGE 明细与 thoughts），1K/2K/4K 各自数值落在实测区间。
- 计费按 pro 单价、图像 token 为 1120（1K/2K）或 2000（4K）结算，消除 ~80% 少收。
- 真 pro 流量不被改写。
- 全部新单测通过，`internal/service`、`internal/handler` 既有测试不回归。
