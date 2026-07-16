# Gemini 3.1 Flash Image usage 明细修复设计

日期：2026-07-15

## 背景

`sub2api -> new-api -> CLIProxyAPIPlus -> Antigravity/Windsurf` 链路返回的
`gemini-3.1-flash-image` 生图响应可能只包含
`promptTokenCount`、`candidatesTokenCount` 和 `totalTokenCount`，缺少
`candidatesTokensDetails[IMAGE]`。sub2api 因此把图像输出 token 当成文本输出
token 计价。

## 目标

对 direct flash 生图响应缺失的 IMAGE 子明细做确定性回填，使下游响应与内部计费
使用同一份 usage，并保证图像 token 不会同时按文本价和图像价重复收费。

## 适用范围

- 请求模型为 `gemini-3.1-flash-image`、preview 或其带版本后缀变体。
- action 为 `generateContent` 或 `streamGenerateContent`。
- 响应存在至少一个 `mimeType=image/*` 且 data 非空的 `inlineData` 图片。
- `usageMetadata` 存在，且没有有效的 IMAGE 明细。
- pro 映射伪装、2.5 flash、countTokens、文本/安全拦截响应不在本功能范围。

## 档位和 token

| imageSize | 单图 IMAGE token |
|---|---:|
| `0.5K` / `512px` | 747 |
| 空值 / `1K` | 1120 |
| `2K` | 1680 |
| `4K` | 2520 |

空值是 Gemini 3 图片模型的官方 1K 默认值。未知非空尺寸不回填，避免猜测收费。
多张图片按 `单图 token * 实际 inlineData 图片数` 汇总。

## 响应改写

仅在 `usageMetadata.candidatesTokensDetails` 末尾增加：

```json
{"modality":"IMAGE","tokenCount":1680}
```

已有其它 modality 明细必须保留。以下计数和身份字段一律不改：

- `promptTokenCount`
- `candidatesTokenCount`
- `totalTokenCount`
- `thoughtsTokenCount`
- `modelVersion`
- `candidates[].index`

根据 Google 官方 `UsageMetadata` 语义补全两个可推导字段：

- 仅当请求中所有输入 part 均为 TEXT、没有缓存上下文，且响应缺少
  `promptTokensDetails` 时，补 `[{"modality":"TEXT","tokenCount":promptTokenCount}]`。
  含图片、文件、函数结果或未知模态的输入不猜测拆分。
- 已有 `serviceTier` 原样保留；已有 Vertex 风格 `trafficType` 时不额外增加 tier；
  两者都缺时使用请求显式的 `standard`/`flex`/`priority`，请求未指定或为
  `unspecified` 时补官方默认语义 `serviceTier: "standard"`。

当 `candidatesTokenCount < 回填的 IMAGE token` 时拒绝回填，防止产生负文本 token
或不自洽账单。

## 计费不重叠不变量

改写后统一重新调用 `extractGeminiUsage`：

```text
OutputTokens = candidatesTokenCount + thoughtsTokenCount
ImageOutputTokens = candidatesTokensDetails[IMAGE]
TextOutputTokens = OutputTokens - ImageOutputTokens
```

计费层已有的 `TextOutputTokens` 扣减逻辑保持不变，因此 IMAGE 是 Output 的子集，
不会重复计费。不得把 IMAGE token 再加到 `OutputTokens` 或 `totalTokenCount`。

## 三条响应路径

1. 非流式：写给客户端前修复 body，再从修复后的 body 解析 usage。
2. 上游 SSE 聚合为非流式：聚合器保留最后一次原始 `usageMetadata`，再执行修复。
3. 客户端 SSE：流状态记录是否已见图片；任意后续带 usage 的分块都可修复，不要求
   `finishReason` 与 usage 同块。

## 失败策略

JSON 非法、尺寸未知、无实际图片、已有 IMAGE 明细、计数不自洽或 sjson 写入失败时，
原样透传并使用原始 usage，不做部分改写。

## 测试要求

- 0.5K、空值、1K、2K、4K 和未知尺寸。
- 单图、多图、无图、安全拦截、已有 IMAGE、保留其它 modality。
- `thoughtsTokenCount` 保留且参与 Output，但不与 IMAGE 重叠。
- 纯文本输入补 prompt TEXT 明细；图片/文件/缓存输入不补。
- serviceTier 请求值、默认 standard、已有 trafficType 和已有 tier 的优先级。
- SSE 的图片块与 usage 块分离。
- 聚合路径的图片块与 usage 块分离。
- pro、2.5 flash、countTokens 不触发。
