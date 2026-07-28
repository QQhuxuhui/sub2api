# gpt-image-2 Token 与计费参考

> 采集日期：2026-07-28
> 目的：为 adobe2api（Firefly 出图，无原生 token 数据）建立与官方口径一致的 usage 模拟依据。
> 本文所有 token 数值均为实测（§4 共 54 格全测），不含外推。

---

## 1. 三条链路的区别

本次共对比三条产出 `gpt-image-2` 的链路，**它们的返回结构和 token 口径互不相同**，混用会得出错误结论。

| 链路 | 入口 | 实际模型 | 出图尺寸 | token 口径 |
|---|---|---|---|---|
| **A. 官方直连 API** | new-api 中转 → OpenAI API | `gpt-image-2` | 精确等于请求值，两边 16 整除 | 真实、确定性 |
| **B. 账号代理（codex 管线）** | sub2api 账号 1118 | `gpt-image-2-codex` | 1254×1254、1533×1026、1672×941 等，**不满足 16 整除** | 与 A 不同，见 §7 |
| **C. adobe2api** | sub2api 账号 1115 | Firefly `gpt-image` | 见 §4 的 30 组尺寸 | 本地模拟，见 §8 |

链路 B 的响应带 `revised_prompt`（上游会重写提示词）且 `quality` 恒为 `auto`，是 ChatGPT 账号侧的图像管线，**不是 API 版 gpt-image-2**，其 usage 不可作为对齐基准。

---

## 2. 官方单价

| 项目 | 单价 |
|---|---|
| 文本输入 | $5.00 / 1M |
| 图像输入 | $8.00 / 1M |
| 图像输入（缓存命中） | $2.00 / 1M |
| **图像输出** | **$30.00 / 1M** |

**验算（实测）**：sub2api `usage_logs` 中一条 1K 记录 `image_output_tokens=229`、`input_tokens=36`，
`total_cost = 0.0070500000`。

```
229 × 30/1e6 + 36 × 5/1e6 = 0.00687 + 0.00018 = 0.00705   ✅ 完全一致
```

`channel_model_pricing` 中 **gpt-image 没有任何按模型的计价行** —— sub2api 完全按上游返回
的 usage token 数 × 上述单价计费。**即：上游 usage 里填多少 token，就收多少钱。**

---

## 3. 官方尺寸约束

gpt-image-2 与 gpt-image-1 不同，**支持任意尺寸直到 4K**，需同时满足：

- 宽高均可被 **16 整除**
- 最长边 ≤ **3840**
- 长短边比 ≤ **3:1**
- 总像素在 **655,360 ~ 8,294,400** 之间

档位约定：1K（长边 1280）/ 2K（长边 2048）/ 4K（长边 3840）。官方将 4K 标注为 experimental。

**不传 `quality` 时，官方默认档位为 `low`**（实测：`1456x624` 不传 quality 返回 82 token，
等于 low 档实测值）。

---

## 4. 输出图像 token 全表（链路 A 实测）

**54 格全部实测**，横版；竖版同值（已验证 `720x1280` = `1280x720` = 106）。
无任何推导值。

| 比例 | 档位 | 尺寸 | low | medium | high | low $ | med $ | high $ |
|---|---|---|---|---|---|---|---|---|
| 1:1 | 1K | 1024×1024 | 196 | 1756 | 7024 | 0.0059 | 0.0527 | 0.2107 |
| 1:1 | 2K | 2048×2048 | 397 | 3568 | 14272 | 0.0119 | 0.1070 | 0.4282 |
| 1:1 | 4K | 2880×2880 | 659 | 5930 | 23719 | 0.0198 | 0.1779 | 0.7116 |
| 5:4 | 1K | 1120×896 | 157 | 1370 | 5551 | 0.0047 | 0.0411 | 0.1665 |
| 5:4 | 2K | 2240×1792 | 313 | 2743 | 11115 | 0.0094 | 0.0823 | 0.3335 |
| 5:4 | 4K | 3200×2560 | 530 | 4648 | 18835 | 0.0159 | 0.1394 | 0.5651 |
| 4:3 | 1K | 1152×864 | 144 | 1294 | 5176 | 0.0043 | 0.0388 | 0.1553 |
| 4:3 | 2K | 2304×1728 | 288 | 2584 | 10336 | 0.0086 | 0.0775 | 0.3101 |
| 4:3 | 4K | 3264×2448 | 480 | 4316 | 17264 | 0.0144 | 0.1295 | 0.5179 |
| 3:2 | 1K | 1248×832 | 134 | 1167 | 4667 | 0.0040 | 0.0350 | 0.1400 |
| 3:2 | 2K | 2496×1664 | 271 | 2363 | 9452 | 0.0081 | 0.0709 | 0.2836 |
| 3:2 | 4K | 3504×2336 | 449 | 3912 | 15645 | 0.0135 | 0.1174 | 0.4693 |
| 16:9 | 1K | 1280×720 | 106 | 947 | 3787 | 0.0032 | 0.0284 | 0.1136 |
| 16:9 | 2K | 2560×1440 | 205 | 1843 | 7370 | 0.0062 | 0.0553 | 0.2211 |
| 16:9 | 4K | 3840×2160 | 371 | 3336 | 13342 | 0.0111 | 0.1001 | 0.4003 |
| 21:9 | 1K | 1456×624 | 82 | 733 | 2863 | 0.0025 | 0.0220 | 0.0859 |
| 21:9 | 2K | 3024×1296 | 166 | 1492 | 5825 | 0.0050 | 0.0448 | 0.1747 |
| 21:9 | 4K | 3696×1584 | 220 | 1980 | 7729 | 0.0066 | 0.0594 | 0.2319 |

竖版尺寸（4:5 / 3:4 / 2:3 / 9:16）与对应横版 token 相同。

**这 18 行覆盖 adobe2api `core/models/payloads.py::gpt_image_pixels_from_ratio` 的全部
30 组尺寸**（横竖对称合并后）。已脚本校验：那 30 组 100% 满足 §3 的官方约束。

### ⚠️ token 不随像素线性变化

`3840x2160` 与 `2880x2880` 同为 8.29 MP，token 却是 **371 vs 659**。
**越接近方形，token 越多。** 必须按 (比例, 档位) 查表，不可用像素折算。

---

## 5. 质量维度

**`quality` 与 `size` 是两个正交维度。** 不传 `quality` 时官方默认 **low**，
且 `quality:"auto"` 也归一为 low —— 实测 `1024x1024` + `quality:"auto"`
返回 196 token，**响应回显 `quality: "low"`**。

### 倍率结构（按比例恒定，跨档位不变）

| 比例 | med/low（1K / 2K / 4K） | high/med（1K / 2K / 4K） |
|---|---|---|
| 1:1 | 8.959 / 8.987 / 8.998 | 4.000 / 4.000 / 4.000 |
| 5:4 | 8.726 / 8.764 / 8.770 | 4.052 / 4.052 / 4.052 |
| 4:3 | 8.986 / 8.972 / 8.992 | 4.000 / 4.000 / 4.000 |
| 3:2 | 8.709 / 8.720 / 8.713 | 3.999 / 4.000 / 3.999 |
| 16:9 | 8.934 / 8.990 / 8.992 | 3.999 / 3.999 / 3.999 |
| 21:9 | 8.939 / 8.988 / 9.000 | 3.906 / 3.904 / 3.904 |

**倍率对同一比例在三个档位上高度稳定**（med/low 组内差 <0.7%，high/med 组内差 <0.05%），
但**跨比例差异显著**（med/low 从 3:2 的 8.71 到 21:9 的 9.00；
high/med 从 21:9 的 3.905 到 5:4 的 4.052）。

这个内部一致性是数据可信度的交叉验证 —— 若测量含随机噪声，组内不会如此整齐。

**推论：不可跨比例外推。** 早期用全局倍率 ×8.98 / ×35.9 推导时，
21:9 的 1K high 推得 2944，实测 **2863**（−2.8%）。§4 表现已全部实测，不再有此风险。

交叉验证：官方计算器公布的 1024×1024 每图价格 $0.006 / $0.053 / $0.211
反推为 200 / 1767 / 7033，与实测 196 / 1756 / 7024 吻合（偏差 <1%）。

**单图成本区间**：low $0.0025（21:9 1K）~ high $0.7116（1:1 4K）。

---

## 6. 输入图像 token 公式（13/13 实测吻合）

```python
def input_image_tokens(w: int, h: int) -> int:
    L = max(w, h)
    if L < 1024:                                  # 最长边不足 1024 才放大，放大上限 2 倍
        f = min(2.0, 1024 / L)
        w, h = w * f, h * f
    patches = lambda a, b: ceil(a / 32) * ceil(b / 32)
    if patches(w, h) > 1536:                      # patch 数上限 1536，超出则等比缩小
        f = sqrt(1536 / patches(w, h))
        w, h = w * f, h * f
        while patches(w, h) > 1536:
            w, h = w * 0.99, h * 0.99
    return patches(w, h)
```

**该公式在链路 A 与链路 B 上均精确成立**（共 13 个实测点，无一偏差）：

| 输入尺寸 | 实测 | 公式 | 来源链路 |
|---|---|---|---|
| 256×256 | 256 | 256 | A、B |
| 512×512 | 1024 | 1024 | A |
| 512×1024 | 512 | 512 | B |
| 550×368 | 704 | 704 | B |
| 768×768 | 1024 | 1024 | A |
| 1024×1024 | 1024 | 1024 | A、B |
| 1280×720 | 920 | 920 | A |
| 1536×1536 | 1521 | 1521 | A |
| 2048×1152 | 1508 | 1508 | B |
| 2048×2048 | 1521 | 1521 | A |
| 3840×2160 | 1508 | 1508 | A |

**多张输入图的 token 可加**（实测：`704 + 1508 = 2212`，与两图同时提交时的返回值精确一致）。

---

## 7. 完整返回数据结构

以下为实际抓取的响应，`b64_json` 已截断，其余字段原样保留。

### 7.1 链路 A —— 官方直连 `/v1/images/generations`

请求：`{"model":"gpt-image-2","prompt":"...","size":"1456x624","quality":"low","n":1}`

```json
{
  "created": 1785216905,
  "background": "opaque",
  "data": [
    { "b64_json": "iVBORw0KGgoAAAANSUhEUgAA...<截断>" }
  ],
  "output_format": "png",
  "quality": "low",
  "size": "1456x624",
  "usage": {
    "input_tokens": 15,
    "input_tokens_details": { "image_tokens": 0, "text_tokens": 15 },
    "output_tokens": 82,
    "output_tokens_details": { "image_tokens": 82, "text_tokens": 0 },
    "total_tokens": 97
  }
}
```

字段说明：

| 字段 | 说明 |
|---|---|
| `background` | `opaque` / `transparent` / `auto` |
| `output_format` | `png` / `jpeg` / `webp` |
| `quality` | 回显实际使用的档位（不传时回显 `low`） |
| `size` | 回显实际出图尺寸，与 `data[0]` 解码后的像素一致 |
| `usage.output_tokens_details.image_tokens` | **计费依据**，即 §4 表中的数值 |
| — | 官方直连响应**没有** `model` 字段 |

### 7.2 链路 A —— 官方直连 `/v1/images/edits`（multipart）

请求：`-F image=@3840x2160.png -F prompt="make it night" -F size=1024x1024 -F quality=low`

```json
{
  "created": 1785217137,
  "background": "opaque",
  "data": [
    { "b64_json": "iVBORw0KGgoAAAANSUhEUgAA...<截断>" }
  ],
  "output_format": "png",
  "quality": "low",
  "size": "1024x1024",
  "usage": {
    "input_tokens": 1518,
    "input_tokens_details": { "image_tokens": 1508, "text_tokens": 10 },
    "output_tokens": 196,
    "output_tokens_details": { "image_tokens": 196, "text_tokens": 0 },
    "total_tokens": 1714
  }
}
```

`input_tokens_details.image_tokens = 1508` 即 §6 公式对 3840×2160 的计算结果。

### 7.3 链路 B —— 账号代理 codex 管线（sub2api 账号 1118）

```json
{
  "created": 1785211098,
  "data": [
    {
      "b64_json": "iVBORw0KGgoAAAANSUhEUgAA...<截断>",
      "revised_prompt": "Edit the provided image to transform the scene from sunset/daylight into nighttime. Keep the same composition, road, van, signpost, ocean, mountains, and clouds. ..."
    }
  ],
  "background": "auto",
  "output_format": "png",
  "quality": "auto",
  "size": "1672x941",
  "model": "gpt-image-2-codex",
  "usage": {
    "input_tokens": 2327,
    "input_tokens_details": { "image_tokens": 2212, "text_tokens": 115 },
    "output_tokens": 1158,
    "output_tokens_details": { "image_tokens": 1158, "text_tokens": 0 },
    "total_tokens": 3485
  }
}
```

与链路 A 的差异：

- 多出 `model: "gpt-image-2-codex"` 与 `data[].revised_prompt`
- `quality` 恒为 `auto`，**传入的 `quality` 参数不生效**（low / medium / high 各测 2 次，
  输出 token 全部相同）
- `size` 为 `1672x941` 等**不满足 16 整除**的值
- 纯文生图恒定输出 `1254x1254` / `out_img = 229`（≥13 次采样均为 229，另有 1 次异常为 2058）
- 输入图 token 遵循 §6 公式（已验证），但**输出 token 与官方表无对应关系**

### 7.4 链路 C —— adobe2api（sub2api 账号 1115）

```json
{
  "created": 1785202879,
  "model": "gpt-image-2",
  "data": [
    { "b64_json": "iVBORw0KGgoAAAANSUhEUgAA...<截断>" }
  ],
  "usage": {
    "input_tokens": 304,
    "output_tokens": 400,
    "total_tokens": 704,
    "input_tokens_details": { "image_tokens": 300, "text_tokens": 4 },
    "output_tokens_details": { "image_tokens": 400, "text_tokens": 0 }
  }
}
```

`usage` 由 adobe2api `core/models/resolver.py::build_image_usage` 本地生成。
无 `background` / `output_format` / `quality` / `size` 字段。
`input_tokens_details.image_tokens = 300` 是写死的常量（`INPUT_IMAGE_TOKENS = 300`）。

`response_format: "url"` 时 `data[0]` 变为 `{"url": "..."}`；当前未配置
`ADOBE_PUBLIC_BASE_URL` 时会返回内网地址 `http://adobe2api:6001/generated/<job_id>.png`，
外部不可访问 —— 详见 adobe2api `app.py::_public_generated_url`。

---

## 8. 错误响应结构

各层错误信封不同，可据此判断故障出在哪一层。

**sub2api 本地参数校验**（未触达上游，响应快、无 `code`）：

```json
{"error":{"message":"prompt is required","type":"invalid_request_error"}}
```

**sub2api 调度失败**（如显式 2K/4K 尺寸但无高清账号可用，HTTP 503）：

```json
{"error":{"message":"No available compatible accounts","type":"api_error"}}
```

**OpenAI / codex 管线错误**（带 `code` 与 `param`）：

```json
{"error":{"code":"invalid_value","message":"Error while downloading file. Upstream status code: 404.","param":"url","type":"invalid_request_error"}}
```

```json
{"error":{"code":"content_policy_violation","message":"$image_generation (prompt: \"...\") ...","type":"invalid_request_error"}}
```

**adobe2api 错误**（带 `ERR-` 前缀的内部错误码，便于关联日志）：

```json
{"error":{"code":"ERR-AFB0F7F2A3","message":"Failed to fetch image_url: 404","type":"invalid_request_error"}}
```

**new-api 中转错误**（带 `request id`）：

```json
{"error":{"code":"model_not_found","message":"No available channel for model gpt-image-2 under group xxx (request id: 2026...)","type":"new_api_error","param":""}}
```

```json
{"error":{"code":"","message":"Invalid token (request id: 2026...)","type":"new_api_error"}}
```

---

## 9. sub2api 侧计费与调度

**usage_logs 关键字段**（一条 1K 记录示例）：

| 字段 | 值 |
|---|---|
| `model` / `requested_model` | `gpt-image-2` |
| `image_count` | 1 |
| `image_size` | `1K`（档位，非像素） |
| `image_size_source` | `input` |
| `image_output_tokens` | 229 |
| `input_tokens` / `output_tokens` | 36 / 229 |
| `total_cost` | 0.00705 |
| `inbound_endpoint` / `upstream_endpoint` | `/v1/images/edits` |

**高清路由门**：`openAIImageSizeRequiresHighRes()`（`backend/internal/service/openai_images.go`）
判定**显式传入**的 `size` 若落在 2K/4K 档，则只调度到 `credentials.openai_images_highres`
为真的账号；无匹配账号时返回 503 `No available compatible accounts`。

- `auto` 或不传 `size` 不触发该门
- **仅用 `quality` 控制清晰度不会触发该门**
- 采集日当前只有账号 1115 带该标记

**响应头**：`x-request-id` 可用于关联 `ops_error_logs` / `usage_logs` 定位单次请求落到哪个账号。

---

## 10. 对 adobe2api 的改造建议

现状（`adobe2api/core/models/resolver.py`）：

- `_GPT_IMAGE_OUTPUT_TOKENS` 使用的是 **gpt-image-1** 的 3×3 表（质量 × 朝向），
  方图 low/medium/high = 272 / 1056 / 4160，与 gpt-image-2 数量级不符
- `_RES_TO_QUALITY` 把 `1K/2K/4K` 映射为 `low/medium/high`，**把官方的两个正交维度
  （尺寸、质量）压成了一个**
- `INPUT_IMAGE_TOKENS = 300` 为写死常量

建议：

1. 用 §4 的 18 行表替换 `_GPT_IMAGE_OUTPUT_TOKENS`，按 **(比例, 档位)** 查表。
   adobe2api 在构造 usage 时已同时持有 `ratio` 与 `output_resolution`，可直接命中。
2. 质量倍率单独作为一个可配置系数（`1` / `8.98` / `35.9`），与尺寸档位解耦。
   选哪一档是**定价决策**，直接决定收入：

   | 计费质量档 | 4K 16:9 单图 | 1K 1:1 单图 |
   |---|---|---|
   | low（官方默认） | $0.011 | $0.006 |
   | medium | $0.100 | $0.053 |
   | high（官方满价） | $0.400 | $0.211 |

3. `INPUT_IMAGE_TOKENS` 常量替换为 §6 公式，并保持多图可加。

---

## 11. 复现方法

```bash
# 输出 token —— 换 size / quality 即可复现 §4、§5
curl -sS https://<官方直连网关>/v1/images/generations \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"gpt-image-2","prompt":"a plain blue circle centered on a white background",
       "size":"3840x2160","quality":"low","n":1}' \
| python3 -c "import sys,json;u=json.load(sys.stdin)['usage'];print(u['output_tokens_details']['image_tokens'])"

# 输入图 token —— 换输入图尺寸即可复现 §6
curl -sS https://<官方直连网关>/v1/images/edits \
  -H "Authorization: Bearer $KEY" \
  -F model=gpt-image-2 -F image=@input.png -F prompt="make it night" \
  -F size=1024x1024 -F quality=low -F n=1 \
| python3 -c "import sys,json;u=json.load(sys.stdin)['usage'];print(u['input_tokens_details']['image_tokens'])"
```

提示词内容不影响输出 token（已用简单提示词与复杂提示词各测，均为 196）。

---

## 12. 数据可信度与局限

**可直接引用**：

- §2 单价（已与 `usage_logs.total_cost` 验算一致）
- §3 尺寸约束
- §4 表中 **全部 54 格**（low / medium / high 三列均为实测，无推导值）
- §5 倍率结构（由 §4 实测值直接计算）
- §6 公式（13 个实测点全中）
- §7 全部响应结构（实抓）

**推导值**：无。本文档不再包含任何外推数值。

**未覆盖**：

- 竖版尺寸仅验证了 `720x1280` 一组，其余竖版按对称性推定
- 未测 `background=transparent`、`output_format=jpeg/webp`、`n>1`、流式 partial images
  （官方文档称每张 partial image 额外 +100 输出 token，未实测）
- 未测缓存图像输入（$2/1M 档）的触发条件
- 链路 B 的 2058 token 异常样本仅出现 1 次（≥14 次采样），成因未查明
