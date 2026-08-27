package service

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// geminiImageOutputCounterKey 是请求级内联图片计数器挂在 gin.Context 上的键。
const geminiImageOutputCounterKey = "gemini_image_output_counter"

// geminiImageOutputCounter 记录一次转发里 Gemini 上游真正回吐的内联图片数量。
//
// 取「单个 payload 内的最大值」而不是累加：Gemini 兼容上游的 SSE 分片可能是
// 累积式的（同一份内容在后续 chunk 里重复整段回来——本文件同目录的
// computeGeminiTextDelta 就是为此存在的），逐 chunk 累加会把同一张图算很多次。
// 计费上宁可少算不可多算，所以用 max 兜底：
//   - 非流式：整份响应体只观测一次，max 即真实张数；
//   - 累积式流：最后一个 chunk 含全部图片，max 仍是真实张数；
//   - 增量式流且多图分散在不同 chunk：会低估到 1，与改动前的模型名启发式同值，
//     不构成回退。
type geminiImageOutputCounter struct {
	count int
}

// beginGeminiImageOutputObservation 在每次 Forward 开头重置计数器。
// failover 会拿同一个 gin.Context 重跑转发，不重置就会把上一个账号的图数带进来。
func beginGeminiImageOutputObservation(c *gin.Context) *geminiImageOutputCounter {
	if c == nil {
		return nil
	}
	counter := &geminiImageOutputCounter{}
	c.Set(geminiImageOutputCounterKey, counter)
	return counter
}

func geminiImageOutputCounterFromContext(c *gin.Context) *geminiImageOutputCounter {
	if c == nil {
		return nil
	}
	value, ok := c.Get(geminiImageOutputCounterKey)
	if !ok {
		return nil
	}
	counter, _ := value.(*geminiImageOutputCounter)
	return counter
}

// observeGeminiImageOutputs 观测一段上游响应（整份或单个 chunk）里的内联图片。
// 调用点与 upstreamResponseModelObserver.ObserveGemini 一一对应——那里拿得到
// 解包后的上游响应体，这里需要的是同一份字节。
func observeGeminiImageOutputs(c *gin.Context, payload []byte) {
	counter := geminiImageOutputCounterFromContext(c)
	if counter == nil {
		return
	}
	if count := countGeminiInlineImageOutputs(payload); count > counter.count {
		counter.count = count
	}
}

func observedGeminiImageOutputs(c *gin.Context) int {
	counter := geminiImageOutputCounterFromContext(c)
	if counter == nil {
		return 0
	}
	return counter.count
}

// observedGeminiImageOutputsForBilling 返回可用于计费判定的真实观测张数。
//
// 与 observedGeminiImageOutputs 的关键差别：区分「计数器没装」和「装了但数出 0」。
// 前者返回 nil —— 这条链路压根没观测，不能拿来当「上游没回图」的证据；后者返回指向 0
// 的指针，才是「确实一张都没回」。用户级空返回免费只认后者。
//
// billedImageCount<=0 时同样返回 nil：本次请求不按图计费，无所谓空不空。
func observedGeminiImageOutputsForBilling(c *gin.Context, billedImageCount int) *int {
	if billedImageCount <= 0 {
		return nil
	}
	counter := geminiImageOutputCounterFromContext(c)
	if counter == nil {
		return nil
	}
	observed := counter.count
	return &observed
}

// resolveGeminiImageCount 决定本次请求按几张图计费。
//
// 优先用上游真正返回的内联图片数：走 GeminiMessagesCompatService 的账号多是
// API Key + 自定义模型映射，客户端请求名和上游模型名都可能是站长自取的别名
// （issue #5358 里的 nana-banana-2），isImageGenerationModel 的白名单必然判不出，
// 于是 ImageCount=0，calculateRecordUsageCost 整条按次计费分支不触发，
// 生图请求全部记 $0。
//
// 只有响应里数不出图时（例如上游用 fileData 引用而非 inlineData 回图，或聚合
// 函数丢掉了图片 part）才退回既有的模型名启发式，保证老行为不回退；这里额外
// 也认映射后的上游模型名，与 shouldSkipCodexPlanGatedImageModelCooldown 对
// requestedModel / modelKey 双取的口径一致。
func resolveGeminiImageCount(c *gin.Context, originalModel, mappedModel string) int {
	if observed := observedGeminiImageOutputs(c); observed > 0 {
		return observed
	}
	if isImageGenerationModel(originalModel) || isImageGenerationModel(mappedModel) {
		return 1
	}
	return 0
}

// countGeminiInlineImageOutputs 统计 Gemini 响应中的图片 part。兼容 inlineData/fileData
// 及其 snake_case 变体；函数名保留以减少已有计费调用点的无关变更。
func countGeminiInlineImageOutputs(payload []byte) int {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return 0
	}
	count := 0
	gjson.GetBytes(payload, "candidates").ForEach(func(_, candidate gjson.Result) bool {
		candidate.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
			if geminiPartIsImageOutput(part) {
				count++
			}
			return true
		})
		return true
	})
	return count
}

func geminiPartIsImageOutput(part gjson.Result) bool {
	inline := part.Get("inlineData")
	if !inline.Exists() {
		inline = part.Get("inline_data")
	}
	if inline.Exists() {
		mimeType := inline.Get("mimeType")
		if !mimeType.Exists() {
			mimeType = inline.Get("mime_type")
		}
		if !isGeminiInlineImageMIMEType(strings.ToLower(strings.TrimSpace(mimeType.String()))) {
			return false
		}
		// 只认真的带上了 base64 数据的 part，空壳 part 不计费。
		return strings.TrimSpace(inline.Get("data").String()) != ""
	}

	fileData := part.Get("fileData")
	if !fileData.Exists() {
		fileData = part.Get("file_data")
	}
	if !fileData.Exists() {
		return false
	}
	mimeType := fileData.Get("mimeType")
	if !mimeType.Exists() {
		mimeType = fileData.Get("mime_type")
	}
	if !isGeminiInlineImageMIMEType(strings.ToLower(strings.TrimSpace(mimeType.String()))) {
		return false
	}
	return strings.TrimSpace(fileData.Get("fileUri").String()) != "" ||
		strings.TrimSpace(fileData.Get("file_uri").String()) != ""
}
