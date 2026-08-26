package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type failingOpenAIImageWriter struct {
	gin.ResponseWriter
	failAfter int
	writes    int
}

type openAIImagesReadErrorBody struct {
	err error
}

func (b *openAIImagesReadErrorBody) Read([]byte) (int, error) { return 0, b.err }
func (b *openAIImagesReadErrorBody) Close() error             { return nil }

func (w *failingOpenAIImageWriter) Write(p []byte) (int, error) {
	if w.writes >= w.failAfter {
		return 0, errors.New("write failed: client disconnected")
	}
	w.writes++
	return w.ResponseWriter.Write(p)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_JSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"draw a cat","size":"1024x1024","quality":"high","stream":true}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "/v1/images/generations", parsed.Endpoint)
	require.Equal(t, "gpt-image-1.5", parsed.Model)
	require.Equal(t, "draw a cat", parsed.Prompt)
	require.True(t, parsed.Stream)
	require.Equal(t, "1024x1024", parsed.Size)
	require.Equal(t, "1K", parsed.SizeTier)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
	require.False(t, parsed.Multipart)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_QualityValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}
	parse := func(t *testing.T, body string) (*OpenAIImagesRequest, error) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = req
		return svc.ParseOpenAIImagesRequest(c, []byte(body))
	}

	// gpt-image models reject qualities outside the official vocabulary — the
	// simulation and the token table cannot price them, and the official API
	// itself 400s such values.
	for _, quality := range []string{"ultra", "4k"} {
		_, err := parse(t, `{"model":"gpt-image-2","prompt":"draw a cat","quality":"`+quality+`"}`)
		require.Error(t, err, "quality %q must be rejected for gpt-image-2", quality)
		require.Contains(t, err.Error(), "invalid quality")
	}
	// Legacy dall-e values are mapped onto gpt-image tiers instead of 400ing
	// (common in older clients); the mapped value is what gets forwarded.
	for legacy, want := range map[string]string{"hd": "high", "standard": "medium"} {
		parsed, err := parse(t, `{"model":"gpt-image-2","prompt":"draw a cat","quality":"`+legacy+`"}`)
		require.NoError(t, err, "legacy quality %q should be accepted", legacy)
		require.Equal(t, want, parsed.Quality)
	}
	parsed, err := parse(t, `{"model":"gpt-image-2","prompt":"draw a cat"}`)
	require.NoError(t, err)
	require.Empty(t, parsed.Quality)
	require.False(t, parsed.HasQuality)
	for _, quality := range []string{"auto", "low", "medium", "high", " HIGH "} {
		parsed, err := parse(t, `{"model":"gpt-image-2","prompt":"draw a cat","quality":"`+quality+`"}`)
		require.NoError(t, err, "quality %q must be accepted for gpt-image-2", quality)
		require.Equal(t, strings.ToLower(strings.TrimSpace(quality)), parsed.Quality)
		require.True(t, parsed.HasQuality)
	}
	for _, body := range []string{
		`{"model":"gpt-image-2","prompt":"draw a cat","quality":""}`,
		`{"model":"gpt-image-2","prompt":"draw a cat","quality":"   "}`,
		`{"model":"gpt-image-2","prompt":"draw a cat","quality":1}`,
	} {
		_, err := parse(t, body)
		require.Error(t, err)
		require.Contains(t, err.Error(), "quality")
	}
	// Non-gpt-image models keep their own vocabulary untouched.
	_, err = parse(t, `{"model":"grok-imagine","prompt":"draw a cat","quality":"hd"}`)
	require.NoError(t, err)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_GPTImage2RejectsUnsupportedOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}
	tests := []struct {
		name string
		body string
		want string
	}{
		// 注意：以下三条打的是 /v1/images/generations，命中的是"input_fidelity 只
		// 属于 edits 端点"这条通用规则，与模型无关。gpt-image-2 在 edits 上携带
		// 该参数已改为忽略，见 ..._GPTImage2IgnoresInputFidelityOnEdits。
		{name: "input fidelity on generations", body: `{"model":"gpt-image-2","prompt":"edit","input_fidelity":"low"}`, want: "images/edits"},
		{name: "snapshot input fidelity on generations", body: `{"model":"gpt-image-2-2026-04-21","prompt":"edit","input_fidelity":"high"}`, want: "images/edits"},
		{name: "empty input fidelity", body: `{"model":"gpt-image-2","prompt":"edit","input_fidelity":""}`, want: "input_fidelity"},
		{name: "empty background", body: `{"model":"gpt-image-2","prompt":"sticker","background":""}`, want: "background"},
		{name: "transparent jpeg", body: `{"model":"gpt-image-2","prompt":"sticker","background":"transparent","output_format":"jpeg"}`, want: "output_format"},
		{name: "snapshot transparent jpeg", body: `{"model":"gpt-image-2-2026-04-21","prompt":"sticker","background":"transparent","output_format":"jpeg"}`, want: "output_format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req

			_, err := svc.ParseOpenAIImagesRequest(c, []byte(tt.body))
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}

	compatibleBody := `{"model":"gpt-image-1.5","prompt":"edit","images":[{"image_url":"https://example.com/source.png"}],"quality":" HIGH ","input_fidelity":" HIGH ","background":" TRANSPARENT "}`
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(compatibleBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	parsed, err := svc.ParseOpenAIImagesRequest(c, []byte(compatibleBody))
	require.NoError(t, err)
	require.Equal(t, "high", parsed.Quality)
	require.Equal(t, "high", parsed.InputFidelity)
	require.Equal(t, "transparent", parsed.Background)

	for _, field := range []string{"background", "input_fidelity"} {
		body := `{"model":"gpt-image-1.5","prompt":"draw","` + field + `":""}`
		req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = req
		_, err := svc.ParseOpenAIImagesRequest(c, []byte(body))
		require.Error(t, err)
		require.Contains(t, err.Error(), field)
	}
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_AllowsNullableGPTImageOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw","quality":null,"background":null,"input_fidelity":null}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.False(t, parsed.HasQuality)
	require.False(t, parsed.HasBackground)
	require.False(t, parsed.HasInputFidelity)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_GPTImage2AllowsTransparentPNGAndWebP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, outputFormat := range []string{"", "png", "webp"} {
		t.Run("json_"+outputFormat, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2","prompt":"sticker","background":"transparent","output_format":"` + outputFormat + `"}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req

			parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			require.Equal(t, "transparent", parsed.Background)
		})
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "sticker"))
	require.NoError(t, writer.WriteField("background", "transparent"))
	require.NoError(t, writer.WriteField("output_format", "webp"))
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)
	require.Equal(t, "transparent", parsed.Background)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_RejectsInputFidelityOnGenerations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"draw","input_fidelity":"high"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	_, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.Error(t, err)
	require.Contains(t, err.Error(), "images/edits")
}

func TestRewriteOpenAIImagesRequest_CanonicalizesGPTImageOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"edit","images":[{"image_url":"https://example.com/source.png"}],"quality":" HIGH ","background":" TRANSPARENT ","input_fidelity":" HIGH "}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	rewritten, _, err := rewriteOpenAIImagesRequest(body, "application/json", parsed.Model, parsed)
	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(rewritten, "quality").String())
	require.Equal(t, "transparent", gjson.GetBytes(rewritten, "background").String())
	require.Equal(t, "high", gjson.GetBytes(rewritten, "input_fidelity").String())
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_MultipartRejectsExplicitEmptyGPTImage2Options(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, field := range []string{"quality", "background", "input_fidelity"} {
		t.Run(field, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			require.NoError(t, writer.WriteField("model", "gpt-image-2"))
			require.NoError(t, writer.WriteField("prompt", "draw a cat"))
			require.NoError(t, writer.WriteField(field, ""))
			require.NoError(t, writer.Close())

			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body.Bytes()))
			req.Header.Set("Content-Type", writer.FormDataContentType())
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req

			_, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body.Bytes())
			require.Error(t, err)
			require.Contains(t, err.Error(), field)
		})
	}
}

// A plain string mask (`"mask": "data:..."`) must populate MaskImageURL so
// mask input tokens are billed — not just the mask.image_url object form.
func TestOpenAIGatewayServiceParseOpenAIImagesRequest_StringMask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dataURL := "data:image/png;base64,AAAA"
	body := []byte(`{"model":"gpt-image-2","prompt":"edit","images":[{"image_url":"` + dataURL + `"}],"mask":"` + dataURL + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.True(t, parsed.HasMask)
	require.Equal(t, dataURL, parsed.MaskImageURL)
}

// gpt-image-2 上游恒以高保真处理输入图，input_fidelity 不可调。Codex CLI 的图片
// 编辑工具固定携带 input_fidelity=high，早期实现对此直接 400，线上一周拦掉 502 笔
// 本可正常出图的 /v1/images/edits 请求。现按 stream/partial_images 的先例【忽略】。
func TestOpenAIGatewayServiceParseOpenAIImagesRequest_GPTImage2IgnoresInputFidelityOnEdits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"gpt-image-2", "gpt-image-2-2026-04-21"} {
		for _, fidelity := range []string{"high", "low", " HIGH "} {
			t.Run(model+"/"+strings.TrimSpace(fidelity), func(t *testing.T) {
				body := []byte(`{"model":"` + model + `","prompt":"edit","images":[{"image_url":"https://example.com/source.png"}],"input_fidelity":"` + fidelity + `"}`)
				req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = req

				parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
				require.NoError(t, err)
				require.NotNil(t, parsed)
				require.Empty(t, parsed.InputFidelity)
				require.False(t, parsed.HasInputFidelity)

				// 忽略不等于放行：转发体里该字段必须真的消失，留空值会被下游 new-api
				// 当成非法参数直接 400。
				rewritten, _, err := rewriteOpenAIImagesRequest(body, "application/json", parsed.Model, parsed)
				require.NoError(t, err)
				require.False(t, gjson.GetBytes(rewritten, "input_fidelity").Exists())
			})
		}
	}
}

// multipart 分支同理：字段必须整个删掉，而不是留一个空值 part。
func TestRewriteOpenAIImagesRequest_GPTImage2DropsIgnoredMultipartFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace background"))
	require.NoError(t, writer.WriteField("input_fidelity", "high"))
	require.NoError(t, writer.WriteField("stream", "true"))
	require.NoError(t, writer.WriteField("partial_images", "2"))

	imageHeader := make(textproto.MIMEHeader)
	imageHeader.Set("Content-Disposition", `form-data; name="image"; filename="source.png"`)
	imageHeader.Set("Content-Type", "image/png")
	imagePart, err := writer.CreatePart(imageHeader)
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("source-image-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)
	require.Empty(t, parsed.InputFidelity)
	require.False(t, parsed.HasInputFidelity)
	require.False(t, parsed.Stream)
	require.Nil(t, parsed.PartialImages)

	rewritten, rewrittenType, err := rewriteOpenAIImagesRequest(body.Bytes(), writer.FormDataContentType(), parsed.Model, parsed)
	require.NoError(t, err)

	names := openAIImageMultipartFieldNames(t, rewritten, rewrittenType)
	require.NotContains(t, names, "input_fidelity")
	require.NotContains(t, names, "stream")
	require.NotContains(t, names, "partial_images")
	require.Contains(t, names, "prompt")
	require.Contains(t, names, "image")
	require.Equal(t, "gpt-image-2", openAIImageMultipartFieldValue(t, rewritten, rewrittenType, "model"))
}

// 返回转发体里出现过的全部 part 名；与 openAIImageMultipartFieldValue 不同，它能区分
// "字段不存在" 和 "字段存在但值为空"。
func openAIImageMultipartFieldNames(t *testing.T, body []byte, contentType string) []string {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	require.NoError(t, err)
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var names []string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return names
		}
		require.NoError(t, err)
		names = append(names, part.FormName())
		require.NoError(t, part.Close())
	}
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_MultipartEdit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace background"))
	require.NoError(t, writer.WriteField("size", "1536x1024"))
	part, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("fake-image-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "/v1/images/edits", parsed.Endpoint)
	require.True(t, parsed.Multipart)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, "replace background", parsed.Prompt)
	require.Equal(t, "1536x1024", parsed.Size)
	require.Equal(t, "2K", parsed.SizeTier)
	require.Len(t, parsed.Uploads, 1)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
}

func TestOpenAIImagesRequestModerationBody_JSONEditIncludesInputImageURLs(t *testing.T) {
	parsed := &OpenAIImagesRequest{
		Endpoint:       openAIImagesEditsEndpoint,
		Prompt:         "replace background",
		InputImageURLs: []string{"https://example.com/source.png"},
		MaskImageURL:   "https://example.com/mask.png",
	}

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIImages, parsed.ModerationBody())

	require.Equal(t, "replace background", input.Text)
	require.Equal(t, []string{"https://example.com/source.png", "https://example.com/mask.png"}, input.Images)
}

func TestOpenAIImagesRequestModerationBody_MultipartEditIncludesUploadsInMemory(t *testing.T) {
	parsed := &OpenAIImagesRequest{
		Endpoint: openAIImagesEditsEndpoint,
		Prompt:   "replace background",
		Uploads: []OpenAIImagesUpload{{
			FieldName:   "image",
			FileName:    "source.png",
			ContentType: "image/png",
			Data:        []byte("fake-image-bytes"),
		}},
		MaskUpload: &OpenAIImagesUpload{
			FieldName:   "mask",
			FileName:    "mask.png",
			ContentType: "image/png",
			Data:        []byte("fake-mask-bytes"),
		},
	}

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIImages, parsed.ModerationBody())

	require.Equal(t, "replace background", input.Text)
	require.Equal(t, []string{
		"data:image/png;base64,ZmFrZS1pbWFnZS1ieXRlcw==",
		"data:image/png;base64,ZmFrZS1tYXNrLWJ5dGVz",
	}, input.Images)

	log := (&ContentModerationService{}).buildLog(ContentModerationCheckInput{}, defaultContentModerationConfig(), ContentModerationActionAllow, false, "", 0, nil, input.ExcerptText(), nil, nil, "")
	require.Equal(t, "replace background", log.InputExcerpt)
	require.NotContains(t, log.InputExcerpt, "ZmFrZS")
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_NormalizesOfficialAndCustomSizes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		size     string
		wantTier string
	}{
		{size: "1024x1024", wantTier: "1K"},
		{size: "1536x1024", wantTier: "2K"},
		{size: "1024x1536", wantTier: "2K"},
		{size: "2048x2048", wantTier: "2K"},
		{size: "2048x1152", wantTier: "2K"},
		{size: "3840x2160", wantTier: "4K"},
		{size: "2160x3840", wantTier: "4K"},
		{size: "1024X768", wantTier: "1K"},
		{size: "1280x768", wantTier: "1K"},
		{size: "2560x1440", wantTier: "2K"},
		{size: "2560x1600", wantTier: "2K"},
		{size: "auto", wantTier: "2K"},
	}

	svc := &OpenAIGatewayService{}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","size":"` + tt.size + `"}`)

			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req

			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			require.NotNil(t, parsed)
			require.Equal(t, tt.size, parsed.Size)
			require.Equal(t, tt.wantTier, parsed.SizeTier)
		})
	}
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_UnknownSizesDoNotBlockPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		size     string
		wantTier string
	}{
		{size: "2048x1153", wantTier: "2K"},
		{size: "4096x1024", wantTier: "2K"},
		{size: "3840x1024", wantTier: "2K"},
		{size: "512x512", wantTier: "1K"},
		{size: "invalid", wantTier: "2K"},
		{size: "999999999999999999999999999x2", wantTier: "2K"},
	}

	svc := &OpenAIGatewayService{}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","size":"` + tt.size + `"}`)

			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req

			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			require.NotNil(t, parsed)
			require.Equal(t, tt.size, parsed.Size)
			require.Equal(t, tt.wantTier, parsed.SizeTier)
		})
	}
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_LegacyImageModelUnknownSizePassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"draw a cat","size":"2048x1152"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "2048x1152", parsed.Size)
	require.Equal(t, "2K", parsed.SizeTier)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_MultipartEditWithMaskAndNativeOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-1.5"))
	require.NoError(t, writer.WriteField("prompt", "replace foreground"))
	require.NoError(t, writer.WriteField("output_format", "png"))
	require.NoError(t, writer.WriteField("input_fidelity", "high"))
	require.NoError(t, writer.WriteField("output_compression", "80"))
	require.NoError(t, writer.WriteField("partial_images", "2"))

	imageHeader := make(textproto.MIMEHeader)
	imageHeader.Set("Content-Disposition", `form-data; name="image"; filename="source.png"`)
	imageHeader.Set("Content-Type", "image/png")
	imagePart, err := writer.CreatePart(imageHeader)
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("source-image-bytes"))
	require.NoError(t, err)

	maskHeader := make(textproto.MIMEHeader)
	maskHeader.Set("Content-Disposition", `form-data; name="mask"; filename="mask.png"`)
	maskHeader.Set("Content-Type", "image/png")
	maskPart, err := writer.CreatePart(maskHeader)
	require.NoError(t, err)
	_, err = maskPart.Write([]byte("mask-image-bytes"))
	require.NoError(t, err)

	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Len(t, parsed.Uploads, 1)
	require.NotNil(t, parsed.MaskUpload)
	require.True(t, parsed.HasMask)
	require.Equal(t, "png", parsed.OutputFormat)
	require.Equal(t, "high", parsed.InputFidelity)
	require.NotNil(t, parsed.OutputCompression)
	require.Equal(t, 80, *parsed.OutputCompression)
	require.NotNil(t, parsed.PartialImages)
	require.Equal(t, 2, *parsed.PartialImages)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_PromptOnlyDefaultsRemainBasic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"prompt":"draw a cat"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, OpenAIImagesCapabilityBasic, parsed.RequiredCapability)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_ExplicitSizeRequiresNativeCapability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"prompt":"draw a cat","size":"1024x1024"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_RejectsNonImageModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","prompt":"draw a cat"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.Nil(t, parsed)
	require.ErrorContains(t, err, `images endpoint requires an image model, got "gpt-5.4"`)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_AllowsGrokImageModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, model := range []string{"grok-imagine", "grok-imagine-image", "grok-imagine-image-quality", "grok-imagine-edit"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw a cat","response_format":"b64_json"}`, model))
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req

			svc := &OpenAIGatewayService{}
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			require.NotNil(t, parsed)
			require.Equal(t, model, parsed.Model)
			require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
		})
	}
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_JSONEditURLs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{
		"model":"gpt-image-1.5",
		"prompt":"replace the background",
		"images":[{"image_url":"https://example.com/source.png"}],
		"mask":{"image_url":"https://example.com/mask.png"},
		"input_fidelity":"high",
		"output_compression":90,
		"partial_images":2,
		"response_format":"url"
	}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, []string{"https://example.com/source.png"}, parsed.InputImageURLs)
	require.Equal(t, "https://example.com/mask.png", parsed.MaskImageURL)
	require.Equal(t, "high", parsed.InputFidelity)
	require.NotNil(t, parsed.OutputCompression)
	require.Equal(t, 90, *parsed.OutputCompression)
	require.NotNil(t, parsed.PartialImages)
	require.Equal(t, 2, *parsed.PartialImages)
	require.True(t, parsed.HasMask)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
}

func TestCollectOpenAIImagePointers_RecognizesDirectAssets(t *testing.T) {
	items := collectOpenAIImagePointers([]byte(`{
		"revised_prompt": "cat astronaut",
		"parts": [
			{"b64_json":"QUJD"},
			{"download_url":"https://files.example.com/image.png?sig=1"},
			{"asset_pointer":"file-service://file_123"}
		]
	}`))

	require.Len(t, items, 3)
	var sawBase64, sawURL, sawPointer bool
	for _, item := range items {
		if item.B64JSON == "QUJD" {
			sawBase64 = true
			require.Equal(t, "cat astronaut", item.Prompt)
		}
		if item.DownloadURL == "https://files.example.com/image.png?sig=1" {
			sawURL = true
		}
		if item.Pointer == "file-service://file_123" {
			sawPointer = true
		}
	}
	require.True(t, sawBase64)
	require.True(t, sawURL)
	require.True(t, sawPointer)
}

func TestResolveOpenAIImageBytes_PrefersInlineBase64(t *testing.T) {
	data, err := resolveOpenAIImageBytes(context.Background(), nil, nil, "", openAIImagePointerInfo{
		B64JSON: "data:image/png;base64,QUJD",
	}, openAIUpstreamErrorBodyReadLimit)
	require.NoError(t, err)
	require.Equal(t, []byte("ABC"), data)
}

func TestNewOpenAIImageStatusError_UsesProvidedReadLimit(t *testing.T) {
	padding := strings.Repeat("x", int(openAIUpstreamErrorBodyReadLimit)+1024)
	body := fmt.Sprintf(`{"error":{"padding":"%s","message":"diagnostic-marker"}}`, padding)
	resp := &req.Response{Response: &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}

	err := newOpenAIImageStatusError(resp, "download image bytes failed", int64(len(body)))
	require.Error(t, err)
	require.Equal(t, "diagnostic-marker", err.Error())

	var statusErr *openAIImageStatusError
	require.ErrorAs(t, err, &statusErr)
	require.Len(t, statusErr.ResponseBody, len(body))
}

func TestOpenAIUpstreamErrorBodyReadLimitForConfig_RespectsDiagnosticLimit(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		LogUpstreamErrorBody:         true,
		LogUpstreamErrorBodyMaxBytes: int(openAIUpstreamErrorBodyReadLimit) + 1024,
	}}

	require.Equal(t, int64(cfg.Gateway.LogUpstreamErrorBodyMaxBytes), openAIUpstreamErrorBodyReadLimitForConfig(cfg))
}

func TestAccountSupportsOpenAIImageCapability_OAuthSupportsNative(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	require.True(t, account.SupportsOpenAIImageCapability(OpenAIImagesCapabilityBasic))
	require.True(t, account.SupportsOpenAIImageCapability(OpenAIImagesCapabilityNative))
}

func TestAccountSupportsOpenAIImageCapability_EmptyRequirementDoesNotRejectGrok(t *testing.T) {
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
	}

	require.True(t, account.SupportsOpenAIImageCapability(""))
	require.False(t, account.SupportsOpenAIImageCapability(OpenAIImagesCapabilityBasic))
}

func TestAccountSupportsOpenAIEndpointCapability(t *testing.T) {
	t.Run("OpenAI APIKey 默认兼容 chat、embeddings 和 alpha search", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
		}

		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityEmbeddings))
		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityAlphaSearch))
	})

	t.Run("OpenAI OAuth 默认仅兼容 chat", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
		}

		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityAlphaSearch))
		require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityEmbeddings))
	})

	t.Run("alpha search 允许 OpenAI OAuth/PAT 与 APIKey 账号，拒绝 Grok", func(t *testing.T) {
		// OAuth/PAT 走 chatgpt.com Codex 端点，APIKey 走 {base_url}/v1/alpha/search，
		// 两类都能承接独立搜索（APIKey 被排除曾导致纯 APIKey 分组搜索失效的回归）。
		apiKey := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
		}
		oauth := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
		}
		grok := &Account{
			Platform: PlatformGrok,
			Type:     AccountTypeAPIKey,
		}

		require.True(t, apiKey.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityAlphaSearch))
		require.True(t, oauth.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityAlphaSearch))
		require.False(t, grok.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityAlphaSearch))
	})

	t.Run("显式列表支持同时声明 chat 和 embeddings", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"openai_capabilities": []any{"chat_completions", "embeddings"},
			},
		}

		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityEmbeddings))
	})

	t.Run("显式列表只声明 chat 时不支持 embeddings", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"openai_capabilities": []any{"chat_completions"},
			},
		}

		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
		// chat 能力隐含放行 alpha search（OAuth/APIKey 语义一致）。
		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityAlphaSearch))
		require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityEmbeddings))
	})

	t.Run("OAuth 显式列表沿用 chat 能力放行 alpha search", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"openai_capabilities": []any{"chat_completions"},
			},
		}

		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityAlphaSearch))
	})

	t.Run("显式 map 支持单独关闭 chat 并开启 embeddings", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"openai_capabilities": map[string]any{
					"chat_completions": false,
					"embeddings":       true,
				},
			},
		}

		require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityEmbeddings))
	})

	t.Run("未知能力不应默认放行", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
		}

		require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapability("unknown")))
	})

	t.Run("responses 能力：未探测的 APIKey 默认放行", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
		}

		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses))
	})

	t.Run("responses 能力：探测确认不支持的 APIKey 被排除", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Extra:    map[string]any{"openai_responses_supported": false},
		}

		require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses))
		// 非生图路径仍可选中（只要求 chat_completions）。
		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
	})

	t.Run("responses 能力：探测确认支持的 APIKey 放行", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Extra:    map[string]any{"openai_responses_supported": true},
		}

		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses))
	})

	t.Run("responses 能力：force_chat_completions 覆盖排除 APIKey", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Extra:    map[string]any{"openai_responses_mode": "force_chat_completions"},
		}

		require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses))
	})

	t.Run("responses 能力：OAuth 账号不受探测标记影响", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Extra:    map[string]any{"openai_responses_supported": false},
		}

		require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses))
	})

	t.Run("responses 能力：仍需通过 chat_completions 配置集校验", func(t *testing.T) {
		// 未探测（默认支持 responses），但显式能力集未声明 chat_completions。
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"openai_capabilities": []any{"embeddings"},
			},
		}

		require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses))
	})
}

func TestBuildOpenAIImagesURL_HandlesVersionedBaseURL(t *testing.T) {
	require.Equal(t,
		"https://image-upstream.example/v1/images/generations",
		buildOpenAIImagesURL("https://image-upstream.example/v1", openAIImagesGenerationsEndpoint),
	)
	require.Equal(t,
		"https://open.bigmodel.cn/api/paas/v4/images/generations",
		buildOpenAIImagesURL("https://open.bigmodel.cn/api/paas/v4", openAIImagesGenerationsEndpoint),
	)
	require.Equal(t,
		"https://image-upstream.example/v1/images/edits",
		buildOpenAIImagesURL("https://image-upstream.example/v1/", openAIImagesEditsEndpoint),
	)
	require.Equal(t,
		"https://image-upstream.example/v1/images/generations",
		buildOpenAIImagesURL("https://image-upstream.example", openAIImagesGenerationsEndpoint),
	)
	require.Equal(t,
		"https://image-upstream.example/v1/images/generations",
		buildOpenAIImagesURL("https://image-upstream.example/v1/images/generations", openAIImagesGenerationsEndpoint),
	)
}

type openAIImageTestSSEEvent struct {
	Name string
	Data string
}

func parseOpenAIImageTestSSEEvents(body string) []openAIImageTestSSEEvent {
	chunks := strings.Split(body, "\n\n")
	events := make([]openAIImageTestSSEEvent, 0, len(chunks))
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		var event openAIImageTestSSEEvent
		for _, line := range strings.Split(chunk, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				event.Name = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			case strings.HasPrefix(line, "data: "):
				event.Data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
		}
		if event.Name != "" || event.Data != "" {
			events = append(events, event)
		}
	}
	return events
}

func openAIImageMultipartFieldValue(t *testing.T, body []byte, contentType, fieldName string) string {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	require.NoError(t, err)
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return ""
		}
		require.NoError(t, err)
		data, err := io.ReadAll(part)
		require.NoError(t, err)
		require.NoError(t, part.Close())
		if part.FormName() == fieldName && part.FileName() == "" {
			return string(data)
		}
	}
}

func findOpenAIImageTestSSEEvent(events []openAIImageTestSSEEvent, name string) (openAIImageTestSSEEvent, bool) {
	for _, event := range events {
		if event.Name == name {
			return event, true
		}
	}
	return openAIImageTestSSEEvent{}, false
}

func TestOpenAIGatewayServiceForwardImages_OAuthPassesNAndReturnsAllImages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","size":"1024x1024","quality":"high","n":3}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set("api_key", &APIKey{ID: 42})

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_123"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"usage\":{\"input_tokens\":11,\"output_tokens\":22,\"input_tokens_details\":{\"cached_tokens\":3},\"output_tokens_details\":{\"image_tokens\":7}},\"tool_usage\":{\"image_gen\":{\"input_tokens\":46,\"output_tokens\":2459,\"output_tokens_details\":{\"image_tokens\":2459},\"images\":3}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2UtMQ==\",\"revised_prompt\":\"draw a cat 1\",\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\"},{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2UtMg==\",\"revised_prompt\":\"draw a cat 2\",\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\"},{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2UtMw==\",\"revised_prompt\":\"draw a cat 3\",\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\"}]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc.httpUpstream = upstream

	account := &Account{
		ID:       1,
		Name:     "openai-oauth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "token-123",
			"chatgpt_account_id": "acct-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gpt-image-2", result.Model)
	require.Equal(t, "gpt-image-2", result.UpstreamModel)
	require.Equal(t, 3, result.ImageCount)
	require.Equal(t, 46, result.Usage.InputTokens)
	require.Equal(t, 2459, result.Usage.OutputTokens)
	require.Equal(t, 2459, result.Usage.ImageOutputTokens)

	require.NotNil(t, upstream.lastReq)
	require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
	require.Equal(t, "chatgpt.com", upstream.lastReq.Host)
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "acct-123", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Empty(t, upstream.lastReq.Header.Get("OpenAI-Beta"))

	require.Equal(t, openAIImagesResponsesMainModel, gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "image_generation", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "generate", gjson.GetBytes(upstream.lastBody, "tools.0.action").String())
	require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.lastBody, "tools.0.model").String())
	require.Equal(t, "1024x1024", gjson.GetBytes(upstream.lastBody, "tools.0.size").String())
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "tools.0.quality").String())
	require.Equal(t, int64(3), gjson.GetBytes(upstream.lastBody, "tools.0.n").Int())
	require.Equal(t, "draw a cat", gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "gpt-image-2", gjson.Get(rec.Body.String(), "model").String())
	require.Len(t, gjson.Get(rec.Body.String(), "data").Array(), 3)
	require.Equal(t, "aW1hZ2UtMQ==", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
	require.Equal(t, "aW1hZ2UtMg==", gjson.Get(rec.Body.String(), "data.1.b64_json").String())
	require.Equal(t, "aW1hZ2UtMw==", gjson.Get(rec.Body.String(), "data.2.b64_json").String())
	require.Equal(t, "draw a cat 1", gjson.Get(rec.Body.String(), "data.0.revised_prompt").String())
	require.Equal(t, "draw a cat 3", gjson.Get(rec.Body.String(), "data.2.revised_prompt").String())
}

func TestParseOpenAIImagesSSEUsageBytes_ToolUsagePrecedenceAndFallback(t *testing.T) {
	svc := &OpenAIGatewayService{}
	fallback := OpenAIUsage{InputTokens: 3, OutputTokens: 4, ImageOutputTokens: 2}
	tests := []struct {
		name      string
		toolUsage string
		want      OpenAIUsage
	}{
		{
			name:      "valid tool usage takes atomic precedence",
			toolUsage: `{"input_tokens":4.6e1,"output_tokens":2459e0,"output_tokens_details":{"image_tokens":24590e-1}}`,
			want:      OpenAIUsage{InputTokens: 46, OutputTokens: 2459, ImageOutputTokens: 2459},
		},
		{name: "absent", want: fallback},
		{name: "malformed field", toolUsage: `{"input_tokens":"46","output_tokens":2459,"output_tokens_details":{"image_tokens":2459}}`, want: fallback},
		{name: "fractional field", toolUsage: `{"input_tokens":46,"output_tokens":2459.5,"output_tokens_details":{"image_tokens":2459}}`, want: fallback},
		{name: "negative field", toolUsage: `{"input_tokens":46,"output_tokens":2459,"output_tokens_details":{"image_tokens":-1}}`, want: fallback},
		{name: "overflow field", toolUsage: `{"input_tokens":46,"output_tokens":9223372036854775808,"output_tokens_details":{"image_tokens":2459}}`, want: fallback},
		{name: "incomplete object", toolUsage: `{"input_tokens":46,"output_tokens":2459}`, want: fallback},
		{name: "hostile huge exponent", toolUsage: `{"input_tokens":1e1000000000,"output_tokens":2459,"output_tokens_details":{"image_tokens":2459}}`, want: fallback},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolUsageField := ""
			if tt.toolUsage != "" {
				toolUsageField = `,"tool_usage":{"image_gen":` + tt.toolUsage + `}`
			}
			payload := []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":4,"output_tokens_details":{"image_tokens":2}}` + toolUsageField + `}}`)
			var got OpenAIUsage
			svc.parseOpenAIImagesSSEUsageBytes(payload, &got)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseOpenAIImagesSSEUsageBytes_MalformedCompletedDoesNotOverrideUsage(t *testing.T) {
	svc := &OpenAIGatewayService{}
	var usage OpenAIUsage

	svc.parseOpenAIImagesSSEUsageBytes([]byte(`{"type":"response.output_item.done","item":{"type":"image_generation_call","result":"aW1hZ2U="}}`), &usage)
	svc.parseOpenAIImagesSSEUsageBytes([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":4,"output_tokens_details":{"image_tokens":2}}}}`), &usage)
	svc.parseOpenAIImagesSSEUsageBytes([]byte(`{"type":"response.completed","response":{"tool_usage":{"image_gen":{"input_tokens":46,"output_tokens":2459,"output_tokens_details":{"image_tokens":2459}}}}} trailing`), &usage)

	require.Equal(t, OpenAIUsage{InputTokens: 3, OutputTokens: 4, ImageOutputTokens: 2}, usage)
}

func TestBoundedJSONNonNegativeInt(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
		ok   bool
	}{
		{name: "scale reduction before accumulation", raw: `10000000000000000000e-19`, want: 1, ok: true},
		{name: "decimal scale reduction", raw: `10000000000000000000.0e-19`, want: 1, ok: true},
		{name: "fractional after scale reduction", raw: `10000000000000000001e-19`, ok: false},
		{name: "overflow after scale reduction", raw: `92233720368547758080e-1`, ok: false},
		{name: "zero with negative exponent", raw: `0e-100`, want: 0, ok: true},
		{name: "zero beyond exponent bound", raw: `0e101`, want: 0, ok: true},
		{name: "zero padded decimal beyond exponent bound", raw: `0.000000e+000000000000000000000000000000000000000000000000101`, want: 0, ok: true},
		{name: "zero padded exponent", raw: `1e0000`, want: 1, ok: true},
		{name: "negative zero syntax", raw: `-0e101`, ok: false},
		{name: "hostile exponent", raw: `1e-1000`, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := boundedJSONNonNegativeInt(gjson.Parse(tt.raw))
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestOpenAIGatewayServiceForwardImages_OAuthUpstreamHTTPErrorSurfacesRealError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`)

	// The non-failover upstream error path is shared by /generations and /edits;
	// use /generations here so the request parses without an uploaded image.
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set("api_key", &APIKey{ID: 42})

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	svc.httpUpstream = &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusBadRequest,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"X-Request-Id": []string{"req_img_badreq"},
			},
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"message":"Invalid value for 'size': expected one of 1024x1024, 1536x1024.","type":"invalid_request_error","param":"size","code":"unknown_parameter"}}`,
			)),
		},
	}

	account := &Account{
		ID:       1,
		Name:     "openai-oauth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.Nil(t, result)

	var upstreamErr *OpenAIImagesUpstreamError
	require.ErrorAs(t, err, &upstreamErr)
	require.Equal(t, http.StatusBadRequest, upstreamErr.StatusCode)
	require.Equal(t, "invalid_request_error", upstreamErr.ErrorType)
	require.Equal(t, "unknown_parameter", upstreamErr.Code)

	// The client must receive the actual upstream status code and message instead
	// of a generic 502 "Upstream request failed".
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, "unknown_parameter", gjson.Get(rec.Body.String(), "error.code").String())
	require.Equal(t, "size", gjson.Get(rec.Body.String(), "error.param").String())
	require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), "Invalid value for 'size'")
}

func TestOpenAIGatewayServiceForwardImages_OAuthNonStreamModerationBlockedReturnsClientError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw blocked image","response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set("api_key", &APIKey{ID: 42})

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	svc.httpUpstream = &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_blocked"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000020}}\n\n" +
					"data: {\"type\":\"error\",\"error\":{\"type\":\"image_generation_user_error\",\"code\":\"moderation_blocked\",\"message\":\"Your request was rejected by the safety system. safety_violations=[sexual].\"}}\n\n" +
					"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_blocked\",\"status\":\"failed\",\"error\":{\"type\":\"image_generation_user_error\",\"code\":\"moderation_blocked\",\"message\":\"Your request was rejected by the safety system. safety_violations=[sexual].\"}}}\n\n",
			)),
		},
	}

	account := &Account{
		ID:       1,
		Name:     "openai-oauth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.Nil(t, result)
	var upstreamErr *OpenAIImagesUpstreamError
	require.ErrorAs(t, err, &upstreamErr)
	require.Equal(t, http.StatusBadRequest, upstreamErr.StatusCode)
	require.Equal(t, "moderation_blocked", upstreamErr.Code)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "image_generation_user_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, "moderation_blocked", gjson.Get(rec.Body.String(), "error.code").String())
	require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), "safety system")
}

func TestOpenAIGatewayServiceForwardImages_OAuthNonStreamServerErrorReturnsFailoverBeforeFlush(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{
		httpUpstream: &httpUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/event-stream"},
					"X-Request-Id": []string{"req_img_server_error"},
				},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000021}}\n\n" +
						"data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"The image service is temporarily unavailable.\"}}\n\n",
				)),
			},
		},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	account := &Account{
		ID:       21,
		Name:     "openai-oauth-server-error",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "temporarily unavailable")
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "failover", events[0].Kind)
	require.Equal(t, account.ID, events[0].AccountID)
	require.Equal(t, http.StatusBadGateway, events[0].UpstreamStatusCode)
}

func TestOpenAIGatewayServiceForwardImages_OAuthNonStreamRateLimitBlocksOrganization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{
		httpUpstream: &httpUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"type\":\"rate_limit_exceeded\",\"code\":\"rate_limit_exceeded\",\"message\":\"Rate limit reached for gpt-image-2-codex (for limit gpt-image) in organization org-BOvpEHVcDPTe8h4lZnwMO5Ly on input-images per min: Limit 4000, Used 4000, Requested 1. Please try again in 15ms.\"}}}\n\n",
				)),
			},
		},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	account := &Account{
		ID:       23,
		Name:     "openai-oauth-rate-limit",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")

	require.Nil(t, result)
	var upstreamErr *OpenAIImagesUpstreamError
	require.ErrorAs(t, err, &upstreamErr)
	require.Equal(t, http.StatusTooManyRequests, upstreamErr.StatusCode)
	require.True(t, svc.isOrgImageScheduleBlocked("org-BOvpEHVcDPTe8h4lZnwMO5Ly"))
}

func TestOpenAIImagesOAuthBodyReadTransportErrorFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"X-Request-Id": []string{"req_h2_read_failure"},
			"X-Upstream":   []string{"preserved"},
		},
		Body: &openAIImagesReadErrorBody{err: errors.New("stream error: stream ID 11; INTERNAL_ERROR; received from peer")},
	}
	account := &Account{ID: 5400, Name: "openai-oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{}

	_, _, _, readErr := svc.handleOpenAIImagesOAuthNonStreamingResponse(resp, c, "b64_json", "gpt-image-2")
	require.Error(t, readErr)
	err := svc.handleOpenAIImagesOAuthResponseError(context.Background(), c, account, "gpt-image-2", "https://api.openai.com/v1/responses", resp, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c), readErr)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.JSONEq(t, `{"error":{"type":"upstream_error","code":"upstream_http2_stream_error","message":"Upstream HTTP/2 stream failed"}}`, string(failoverErr.ResponseBody))
	require.Equal(t, "req_h2_read_failure", failoverErr.ResponseHeaders.Get("x-request-id"))
	require.Equal(t, "preserved", failoverErr.ResponseHeaders.Get("x-upstream"))
	resp.Header.Set("X-Upstream", "mutated")
	require.Equal(t, "preserved", failoverErr.ResponseHeaders.Get("x-upstream"))

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "failover", events[0].Kind)
	require.Equal(t, "req_h2_read_failure", events[0].UpstreamRequestID)
	require.Equal(t, "Upstream HTTP/2 stream failed", events[0].Message)
}

func TestOpenAIImagesOAuthBodyReadErrorsNotMisclassified(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "context canceled", err: context.Canceled},
		{name: "response too large", err: fmt.Errorf("%w: limit=1", ErrUpstreamResponseBodyTooLarge)},
		{name: "semantic error", err: &OpenAIImagesUpstreamError{StatusCode: http.StatusBadRequest, ErrorType: "invalid_request_error", Code: "invalid_value", Message: "bad image request"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}
			err := tt.err
			if tt.name != "semantic error" && shouldClassifyOpenAIUpstreamStreamReadError(err, c.Request.Context()) {
				err = newOpenAIUpstreamStreamReadError(err)
			}

			got := (&OpenAIGatewayService{}).handleOpenAIImagesOAuthResponseError(context.Background(), c, &Account{Platform: PlatformOpenAI}, "gpt-image-2", "", resp, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c), err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(got, &failoverErr))
			require.ErrorIs(t, got, tt.err)
		})
	}
}

func TestOpenAIImagesOAuthTransportErrorAfterDownstreamWriteDoesNotFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	before := OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c)
	_, writeErr := c.Writer.Write([]byte("downstream image bytes"))
	require.NoError(t, writeErr)
	classifiedErr := newOpenAIUpstreamStreamReadError(errors.New("unexpected EOF"))
	account := &Account{ID: 5401, Name: "openai-oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	resp := &http.Response{Header: http.Header{"X-Request-Id": []string{"req_after_write"}}}

	err := (&OpenAIGatewayService{}).handleOpenAIImagesOAuthResponseError(context.Background(), c, account, "gpt-image-2", "", resp, before, classifiedErr)

	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.ErrorIs(t, err, classifiedErr)
	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "retry_exhausted_failover", events[0].Kind)
}

func TestShouldClassifyOpenAIUpstreamStreamReadErrorTransportStrings(t *testing.T) {
	for _, message := range []string{"unexpected EOF", "connection reset by peer", "broken pipe", "use of closed network connection"} {
		t.Run(message, func(t *testing.T) {
			require.True(t, shouldClassifyOpenAIUpstreamStreamReadError(errors.New(message)))
		})
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, shouldClassifyOpenAIUpstreamStreamReadError(errors.New("unexpected EOF"), canceledCtx))
}

func TestOpenAIGatewayServiceForwardImages_OAuthStreamServerErrorAfterFlushDoesNotFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"draw a cat","stream":true,"response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{
		httpUpstream: &httpUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/event-stream"},
					"X-Request-Id": []string{"req_img_server_error_after_partial"},
				},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"cGFydGlhbA==\",\"partial_image_index\":0,\"output_format\":\"png\"}\n\n" +
						"data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"The image service failed after partial output.\"}}\n\n",
				)),
			},
		},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	account := &Account{
		ID:       22,
		Name:     "openai-oauth-partial-server-error",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	var upstreamErr *OpenAIImagesUpstreamError
	require.ErrorAs(t, err, &upstreamErr)
	require.True(t, IsOpenAIImagesRetryableUpstreamError(upstreamErr))
	require.True(t, c.Writer.Written())
	require.Contains(t, rec.Body.String(), "event: image_generation.partial_image")
	require.Contains(t, rec.Body.String(), "event: error")
	require.Contains(t, rec.Body.String(), "failed after partial output")

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "retry_exhausted_failover", events[0].Kind)
	require.Equal(t, account.ID, events[0].AccountID)
}

func TestOpenAIImagesSSEClientErrorsAreNotRetryable(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		wantStatus int
	}{
		{
			name:       "invalid request",
			payload:    `{"type":"error","error":{"type":"invalid_request_error","code":"invalid_value","message":"bad size"}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "content policy",
			payload:    `{"type":"error","error":{"type":"image_generation_user_error","code":"content_policy_violation","message":"blocked"}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "rate limit remains distinct from server error",
			payload:    `{"type":"error","error":{"type":"rate_limit_exceeded","code":"rate_limit_exceeded","message":"try again"}}`,
			wantStatus: http.StatusTooManyRequests,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstreamErr := openAIImagesUpstreamErrorFromSSEPayload([]byte(tt.payload))
			require.NotNil(t, upstreamErr)
			require.Equal(t, tt.wantStatus, upstreamErr.StatusCode)
			require.False(t, IsOpenAIImagesRetryableUpstreamError(upstreamErr))
		})
	}
}

func TestOpenAIGatewayServiceForwardImages_APIKeyGenerationUsesConfiguredV1BaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","quality":" HIGH ","response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
		httpUpstream: &httpUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"X-Request-Id": []string{"req_img_apikey"},
				},
				Body: io.NopCloser(strings.NewReader(`{"created":1710000007,"data":[{"b64_json":"aGVsbG8=","revised_prompt":"draw a cat"}]}`)),
			},
		},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.Equal(t, "high", parsed.Quality)

	account := &Account{
		ID:       6,
		Name:     "openai-apikey",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://image-upstream.example/v1",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, "gpt-image-2", result.Model)
	require.Equal(t, "gpt-image-2", result.UpstreamModel)

	upstream, ok := svc.httpUpstream.(*httpUpstreamRecorder)
	require.True(t, ok)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://image-upstream.example/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer test-api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
	require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "quality").String())
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "aGVsbG8=", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
}

func TestOpenAIGatewayServiceForwardImages_RevalidatesOptionsAfterModelMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name               string
		accountType        string
		credentials        map[string]any
		channelMappedModel string
		body               string
		want               string
		upstreamBody       string
		contentType        string
	}{
		{
			name:        "api key mapping rejects quality",
			accountType: AccountTypeAPIKey,
			credentials: map[string]any{
				"api_key":       "test-api-key",
				"model_mapping": map[string]any{"grok-imagine": "gpt-image-2"},
			},
			body:         `{"model":"grok-imagine","prompt":"draw a cat","quality":" ULTRA "}`,
			want:         "quality",
			upstreamBody: `{"data":[{"b64_json":"aGVsbG8="}]}`,
			contentType:  "application/json",
		},
		{
			name:        "api key mapping rejects non-image model",
			accountType: AccountTypeAPIKey,
			credentials: map[string]any{
				"api_key":       "test-api-key",
				"model_mapping": map[string]any{"grok-imagine": "gpt-5.4"},
			},
			body:         `{"model":"grok-imagine","prompt":"draw a cat"}`,
			want:         "image model",
			upstreamBody: `{"data":[{"b64_json":"aGVsbG8="}]}`,
			contentType:  "application/json",
		},
		{
			name:        "oauth account mapping rejects quality",
			accountType: AccountTypeOAuth,
			credentials: map[string]any{
				"access_token":  "token-123",
				"model_mapping": map[string]any{"grok-imagine": "gpt-image-2"},
			},
			body:         `{"model":"grok-imagine","prompt":"draw a cat","quality":"ultra"}`,
			want:         "quality",
			upstreamBody: "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\"}]}}\n\ndata: [DONE]\n\n",
			contentType:  "text/event-stream",
		},
		{
			name:        "api key mapping rejects transparent jpeg",
			accountType: AccountTypeAPIKey,
			credentials: map[string]any{
				"api_key":       "test-api-key",
				"model_mapping": map[string]any{"grok-imagine": "gpt-image-2"},
			},
			body:         `{"model":"grok-imagine","prompt":"draw a cat","background":"transparent","output_format":"jpeg"}`,
			want:         "output_format",
			upstreamBody: `{"data":[{"b64_json":"aGVsbG8="}]}`,
			contentType:  "application/json",
		},
		{
			name:               "oauth channel mapping rejects input fidelity",
			accountType:        AccountTypeOAuth,
			credentials:        map[string]any{"access_token": "token-123"},
			channelMappedModel: "gpt-image-2",
			body:               `{"model":"grok-imagine","prompt":"draw a cat","input_fidelity":"low"}`,
			want:               "input_fidelity",
			upstreamBody:       "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\"}]}}\n\ndata: [DONE]\n\n",
			contentType:        "text/event-stream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req

			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{tt.contentType}},
				Body:       io.NopCloser(strings.NewReader(tt.upstreamBody)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			originalQuality := parsed.Quality
			originalBackground := parsed.Background
			originalInputFidelity := parsed.InputFidelity
			account := &Account{ID: 12, Platform: PlatformOpenAI, Type: tt.accountType, Credentials: tt.credentials}

			result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, tt.channelMappedModel)
			require.Error(t, err)
			require.Nil(t, result)
			require.Contains(t, err.Error(), tt.want)
			var compatibilityErr *OpenAIImagesAccountCompatibilityError
			require.ErrorAs(t, err, &compatibilityErr)
			require.Nil(t, upstream.lastReq)
			require.Equal(t, originalQuality, parsed.Quality)
			require.Equal(t, originalBackground, parsed.Background)
			require.Equal(t, originalInputFidelity, parsed.InputFidelity)
		})
	}
}

func TestOpenAIGatewayServiceForwardImages_APIKeyStreamJSONResponseBillsImage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"draw a cat","stream":true,"response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
		httpUpstream: &httpUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"X-Request-Id": []string{"req_img_stream_json"},
				},
				Body: io.NopCloser(strings.NewReader(`{"created":1710000008,"usage":{"input_tokens":12,"output_tokens":21,"output_tokens_details":{"image_tokens":9}},"data":[{"b64_json":"aGVsbG8=","revised_prompt":"draw a cat"}]}`)),
			},
		},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	account := &Account{
		ID:       7,
		Name:     "openai-apikey",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://image-upstream.example/v1",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 21, result.Usage.OutputTokens)
	require.Equal(t, 9, result.Usage.ImageOutputTokens)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "aGVsbG8=", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
}

func TestOpenAIGatewayServiceForwardImages_APIKeyStreamRawJSONEventStreamFallbackBillsImage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"draw a cat","stream":true,"response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
		httpUpstream: &httpUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/event-stream"},
					"X-Request-Id": []string{"req_img_stream_json_mislabeled"},
				},
				Body: io.NopCloser(strings.NewReader(`{"created":1710000009,"usage":{"input_tokens":10,"output_tokens":18,"output_tokens_details":{"image_tokens":8}},"data":[{"b64_json":"ZmluYWw="}]}`)),
			},
		},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	account := &Account{
		ID:       8,
		Name:     "openai-apikey",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://image-upstream.example/v1",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 18, result.Usage.OutputTokens)
	require.Equal(t, 8, result.Usage.ImageOutputTokens)
	require.Equal(t, "ZmluYWw=", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
}

func TestOpenAIGatewayServiceForwardImages_APIKeyStreamMultilineSSEDataBillsImage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"draw a cat","stream":true,"response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
		httpUpstream: &httpUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/event-stream"},
					"X-Request-Id": []string{"req_img_stream_multiline"},
				},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"image_generation.completed\",\n" +
						"data: \"usage\":{\"input_tokens\":10,\"output_tokens\":18,\"output_tokens_details\":{\"image_tokens\":8}},\n" +
						"data: \"b64_json\":\"ZmluYWw=\",\"output_format\":\"png\"}\n\n" +
						"data: [DONE]\n\n",
				)),
			},
		},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	account := &Account{
		ID:       8,
		Name:     "openai-apikey",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "test-api-key",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 18, result.Usage.OutputTokens)
	require.Equal(t, 8, result.Usage.ImageOutputTokens)
}

func TestOpenAIGatewayServiceForwardImages_APIKeyStreamingEditMergesImageInputTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"make it night","images":[{"image_url":"https://example.com/input.png"}],"stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream_edit_usage"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"image_generation.completed\",\"usage\":{\"input_tokens\":1518,\"input_tokens_details\":{\"image_tokens\":1508,\"text_tokens\":10},\"output_tokens\":196,\"output_tokens_details\":{\"image_tokens\":196}},\"b64_json\":\"ZmluYWw=\",\"output_format\":\"png\"}\n\n" +
					"data: [DONE]\n\n",
			)),
		}},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key":  "test-api-key",
		"base_url": "https://image-upstream.example/v1",
	}}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1508, result.Usage.ImageInputTokens)
	require.Equal(t, 196, result.Usage.ImageOutputTokens)
	require.Equal(t, 1, result.ImageCount)
}

func TestExtractOpenAIImagesBillableCountFromJSONBytes_CompletedEvent(t *testing.T) {
	body := []byte(`{"type":"image_generation.completed","b64_json":"ZmluYWw=","usage":{"input_tokens":10,"output_tokens":18}}`)

	require.Equal(t, 1, extractOpenAIImagesBillableCountFromJSONBytes(body))
}

func TestOpenAIGatewayServiceForwardImages_APIKeyEditUsesConfiguredV1BaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace background"))
	require.NoError(t, writer.WriteField("quality", " HIGH "))
	imagePart, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("png-image-content"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
		httpUpstream: &httpUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"X-Request-Id": []string{"req_img_edit_apikey"},
				},
				Body: io.NopCloser(strings.NewReader(`{"created":1710000008,"data":[{"b64_json":"ZWRpdGVk","revised_prompt":"replace background"}]}`)),
			},
		},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)

	account := &Account{
		ID:       7,
		Name:     "openai-apikey",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://image-upstream.example/v1/",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body.Bytes(), parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)

	upstream, ok := svc.httpUpstream.(*httpUpstreamRecorder)
	require.True(t, ok)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://image-upstream.example/v1/images/edits", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer test-api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Contains(t, upstream.lastReq.Header.Get("Content-Type"), "multipart/form-data")
	require.Contains(t, string(upstream.lastBody), `name="model"`)
	require.Contains(t, string(upstream.lastBody), "gpt-image-2")
	require.Equal(t, "high", openAIImageMultipartFieldValue(t, upstream.lastBody, upstream.lastReq.Header.Get("Content-Type"), "quality"))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "ZWRpdGVk", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
}

func TestOpenAIGatewayServiceForwardImages_OAuthStreamingTransformsEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"draw a cat","stream":true,"response_format":"url"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000001,\"tools\":[{\"type\":\"image_generation\",\"model\":\"gpt-image-1.5\",\"background\":\"auto\",\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\"}]}}\n\n" +
					"data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"cGFydGlhbA==\",\"partial_image_index\":0,\"output_format\":\"png\",\"background\":\"auto\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000001,\"usage\":{\"input_tokens\":5,\"output_tokens\":9,\"output_tokens_details\":{\"image_tokens\":4}},\"tool_usage\":{\"image_gen\":{\"input_tokens\":46,\"output_tokens\":2459,\"output_tokens_details\":{\"image_tokens\":2459},\"images\":1}},\"tools\":[{\"type\":\"image_generation\",\"model\":\"gpt-image-1.5\",\"background\":\"auto\",\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\"}],\"output\":[{\"type\":\"image_generation_call\",\"result\":\"ZmluYWw=\",\"output_format\":\"png\"}]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc.httpUpstream = upstream

	account := &Account{
		ID:       2,
		Name:     "openai-oauth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, OpenAIUsage{InputTokens: 46, OutputTokens: 2459, ImageOutputTokens: 2459}, result.Usage)
	events := parseOpenAIImageTestSSEEvents(rec.Body.String())
	partial, ok := findOpenAIImageTestSSEEvent(events, "image_generation.partial_image")
	require.True(t, ok)
	require.Equal(t, "image_generation.partial_image", gjson.Get(partial.Data, "type").String())
	require.Equal(t, int64(1710000001), gjson.Get(partial.Data, "created_at").Int())
	require.Equal(t, "cGFydGlhbA==", gjson.Get(partial.Data, "b64_json").String())
	require.Equal(t, "data:image/png;base64,cGFydGlhbA==", gjson.Get(partial.Data, "url").String())
	require.Equal(t, "gpt-image-1.5", gjson.Get(partial.Data, "model").String())
	require.Equal(t, "png", gjson.Get(partial.Data, "output_format").String())
	require.Equal(t, "high", gjson.Get(partial.Data, "quality").String())
	require.Equal(t, "1024x1024", gjson.Get(partial.Data, "size").String())
	require.Equal(t, "auto", gjson.Get(partial.Data, "background").String())

	completed, ok := findOpenAIImageTestSSEEvent(events, "image_generation.completed")
	require.True(t, ok)
	require.Equal(t, "image_generation.completed", gjson.Get(completed.Data, "type").String())
	require.Equal(t, int64(1710000001), gjson.Get(completed.Data, "created_at").Int())
	require.Equal(t, "ZmluYWw=", gjson.Get(completed.Data, "b64_json").String())
	require.Equal(t, "data:image/png;base64,ZmluYWw=", gjson.Get(completed.Data, "url").String())
	require.Equal(t, "gpt-image-1.5", gjson.Get(completed.Data, "model").String())
	require.Equal(t, "png", gjson.Get(completed.Data, "output_format").String())
	require.Equal(t, "high", gjson.Get(completed.Data, "quality").String())
	require.Equal(t, "1024x1024", gjson.Get(completed.Data, "size").String())
	require.Equal(t, "auto", gjson.Get(completed.Data, "background").String())
	require.JSONEq(t, `{"input_tokens":46,"output_tokens":2459,"output_tokens_details":{"image_tokens":2459},"images":1}`, gjson.Get(completed.Data, "usage").Raw)
	require.False(t, gjson.Get(completed.Data, "revised_prompt").Exists())
}

func TestOpenAIGatewayServiceForwardImages_APIKeyStreamingDrainsAfterClientDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"draw a cat","stream":true}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Writer = &failingOpenAIImageWriter{ResponseWriter: c.Writer, failAfter: 1}

	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				ImageStreamDataIntervalTimeout: 1,
				ImageStreamKeepaliveInterval:   0,
			},
		},
		httpUpstream: &httpUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/event-stream"},
					"X-Request-Id": []string{"req_img_stream_disconnect_apikey"},
				},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"cGFydGlhbA==\"}\n\n" +
						"data: {\"type\":\"image_generation.completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":4,\"output_tokens_details\":{\"image_tokens\":2}},\"b64_json\":\"ZmluYWw=\",\"output_format\":\"png\"}\n\n" +
						"data: [DONE]\n\n",
				)),
			},
		},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	account := &Account{
		ID:       8,
		Name:     "openai-apikey",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "test-api-key",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 2, result.Usage.ImageOutputTokens)
}

func TestOpenAIGatewayServiceForwardImages_OAuthEditsMultipartUsesResponsesAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace background with aurora"))
	require.NoError(t, writer.WriteField("output_format", "webp"))
	require.NoError(t, writer.WriteField("quality", "high"))

	imageHeader := make(textproto.MIMEHeader)
	imageHeader.Set("Content-Disposition", `form-data; name="image"; filename="source.png"`)
	imageHeader.Set("Content-Type", "image/png")
	imagePart, err := writer.CreatePart(imageHeader)
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("png-image-content"))
	require.NoError(t, err)

	maskHeader := make(textproto.MIMEHeader)
	maskHeader.Set("Content-Disposition", `form-data; name="mask"; filename="mask.png"`)
	maskHeader.Set("Content-Type", "image/png")
	maskPart, err := writer.CreatePart(maskHeader)
	require.NoError(t, err)
	_, err = maskPart.Write([]byte("png-mask-content"))
	require.NoError(t, err)

	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set("api_key", &APIKey{ID: 100})

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_edit_123"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000002,\"usage\":{\"input_tokens\":13,\"output_tokens\":21,\"output_tokens_details\":{\"image_tokens\":8}},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"ZWRpdGVk\",\"revised_prompt\":\"replace background with aurora\",\"output_format\":\"webp\",\"quality\":\"high\"}]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc.httpUpstream = upstream

	account := &Account{
		ID:       3,
		Name:     "openai-oauth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body.Bytes(), parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.lastBody, "tools.0.model").String())
	require.Equal(t, "edit", gjson.GetBytes(upstream.lastBody, "tools.0.action").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "tools.0.input_fidelity").Exists())
	require.Equal(t, "webp", gjson.GetBytes(upstream.lastBody, "tools.0.output_format").String())
	require.True(t, strings.HasPrefix(gjson.GetBytes(upstream.lastBody, "input.0.content.1.image_url").String(), "data:image/png;base64,"))
	require.True(t, strings.HasPrefix(gjson.GetBytes(upstream.lastBody, "tools.0.input_image_mask.image_url").String(), "data:image/png;base64,"))
	require.Equal(t, "replace background with aurora", gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String())
	require.Equal(t, "ZWRpdGVk", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
	require.Equal(t, "replace background with aurora", gjson.Get(rec.Body.String(), "data.0.revised_prompt").String())
}

func TestOpenAIGatewayServiceForwardImages_OAuthEditsStreamingTransformsEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{
		"model":"gpt-image-1.5",
		"prompt":"replace background with aurora",
		"images":[{"image_url":"https://example.com/source.png"}],
		"mask":{"image_url":"https://example.com/mask.png"},
		"stream":true,
		"response_format":"url"
	}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000003,\"tools\":[{\"type\":\"image_generation\",\"model\":\"gpt-image-1.5\",\"background\":\"transparent\",\"output_format\":\"webp\",\"quality\":\"high\",\"size\":\"1024x1024\"}]}}\n\n" +
					"data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"cGFydGlhbA==\",\"partial_image_index\":0,\"output_format\":\"webp\",\"background\":\"transparent\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000003,\"usage\":{\"input_tokens\":7,\"output_tokens\":10,\"output_tokens_details\":{\"image_tokens\":5}},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"tools\":[{\"type\":\"image_generation\",\"model\":\"gpt-image-1.5\",\"background\":\"transparent\",\"output_format\":\"webp\",\"quality\":\"high\",\"size\":\"1024x1024\"}],\"output\":[{\"type\":\"image_generation_call\",\"result\":\"ZWRpdGVk\",\"revised_prompt\":\"replace background with aurora\",\"output_format\":\"webp\"}]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc.httpUpstream = upstream

	account := &Account{
		ID:       4,
		Name:     "openai-oauth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, "edit", gjson.GetBytes(upstream.lastBody, "tools.0.action").String())
	require.Equal(t, "https://example.com/source.png", gjson.GetBytes(upstream.lastBody, "input.0.content.1.image_url").String())
	require.Equal(t, "https://example.com/mask.png", gjson.GetBytes(upstream.lastBody, "tools.0.input_image_mask.image_url").String())
	events := parseOpenAIImageTestSSEEvents(rec.Body.String())
	partial, ok := findOpenAIImageTestSSEEvent(events, "image_edit.partial_image")
	require.True(t, ok)
	require.Equal(t, "image_edit.partial_image", gjson.Get(partial.Data, "type").String())
	require.Equal(t, int64(1710000003), gjson.Get(partial.Data, "created_at").Int())
	require.Equal(t, "cGFydGlhbA==", gjson.Get(partial.Data, "b64_json").String())
	require.Equal(t, "data:image/webp;base64,cGFydGlhbA==", gjson.Get(partial.Data, "url").String())
	require.Equal(t, "gpt-image-1.5", gjson.Get(partial.Data, "model").String())
	require.Equal(t, "webp", gjson.Get(partial.Data, "output_format").String())
	require.Equal(t, "high", gjson.Get(partial.Data, "quality").String())
	require.Equal(t, "1024x1024", gjson.Get(partial.Data, "size").String())
	require.Equal(t, "transparent", gjson.Get(partial.Data, "background").String())

	completed, ok := findOpenAIImageTestSSEEvent(events, "image_edit.completed")
	require.True(t, ok)
	require.Equal(t, "image_edit.completed", gjson.Get(completed.Data, "type").String())
	require.Equal(t, int64(1710000003), gjson.Get(completed.Data, "created_at").Int())
	require.Equal(t, "ZWRpdGVk", gjson.Get(completed.Data, "b64_json").String())
	require.Equal(t, "data:image/webp;base64,ZWRpdGVk", gjson.Get(completed.Data, "url").String())
	require.Equal(t, "gpt-image-1.5", gjson.Get(completed.Data, "model").String())
	require.Equal(t, "webp", gjson.Get(completed.Data, "output_format").String())
	require.Equal(t, "high", gjson.Get(completed.Data, "quality").String())
	require.Equal(t, "1024x1024", gjson.Get(completed.Data, "size").String())
	require.Equal(t, "transparent", gjson.Get(completed.Data, "background").String())
	require.JSONEq(t, `{"images":1}`, gjson.Get(completed.Data, "usage").Raw)
	require.False(t, gjson.Get(completed.Data, "revised_prompt").Exists())
}

func TestBuildOpenAIImagesResponsesRequest_PassesThroughNForMultiImageModels(t *testing.T) {
	parsed := &OpenAIImagesRequest{
		Endpoint: openAIImagesGenerationsEndpoint,
		Model:    "gpt-image-2",
		Prompt:   "draw a cat",
		N:        2,
	}

	body, err := buildOpenAIImagesResponsesRequest(parsed, "gpt-image-2")
	require.NoError(t, err)
	require.NotNil(t, body)
	require.Equal(t, int64(2), gjson.GetBytes(body, "tools.0.n").Int())
	require.Equal(t, "gpt-image-2", gjson.GetBytes(body, "tools.0.model").String())
	require.Equal(t, "draw a cat", gjson.GetBytes(body, "input.0.content.0.text").String())
}

func TestBuildOpenAIImagesResponsesRequest_DoesNotPassNForDallE3(t *testing.T) {
	parsed := &OpenAIImagesRequest{
		Endpoint: openAIImagesGenerationsEndpoint,
		Model:    "dall-e-3",
		Prompt:   "draw a cat",
		N:        2,
	}

	body, err := buildOpenAIImagesResponsesRequest(parsed, "dall-e-3")
	require.NoError(t, err)
	require.NotNil(t, body)
	require.False(t, gjson.GetBytes(body, "tools.0.n").Exists())
	require.Equal(t, "dall-e-3", gjson.GetBytes(body, "tools.0.model").String())
}

func TestBuildOpenAIImagesResponsesRequest_StripsInputFidelity(t *testing.T) {
	parsed := &OpenAIImagesRequest{
		Endpoint:      openAIImagesEditsEndpoint,
		Model:         "gpt-image-2",
		Prompt:        "replace background",
		InputFidelity: "high",
		InputImageURLs: []string{
			"https://example.com/source.png",
		},
	}

	body, err := buildOpenAIImagesResponsesRequest(parsed, "gpt-image-2")
	require.NoError(t, err)
	require.NotNil(t, body)
	require.False(t, gjson.GetBytes(body, "tools.0.input_fidelity").Exists())
	require.Equal(t, "edit", gjson.GetBytes(body, "tools.0.action").String())
}

func TestBuildOpenAIImagesResponsesRequest_PassesInputFidelityForGPTImage15Edit(t *testing.T) {
	parsed := &OpenAIImagesRequest{
		Endpoint:         openAIImagesEditsEndpoint,
		Model:            "gpt-image-1.5",
		Prompt:           "preserve the face",
		InputFidelity:    "high",
		HasInputFidelity: true,
		InputImageURLs:   []string{"https://example.com/source.png"},
	}

	body, err := buildOpenAIImagesResponsesRequest(parsed, "gpt-image-1.5")
	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(body, "tools.0.input_fidelity").String())
}

func TestBuildOpenAIImagesResponsesRequest_DoesNotPassInputFidelityForNonGPTImageModel(t *testing.T) {
	parsed := &OpenAIImagesRequest{
		Endpoint:         openAIImagesEditsEndpoint,
		Model:            "grok-imagine",
		Prompt:           "preserve the face",
		InputFidelity:    "high",
		HasInputFidelity: true,
		InputImageURLs:   []string{"https://example.com/source.png"},
	}

	body, err := buildOpenAIImagesResponsesRequest(parsed, "grok-imagine")
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "tools.0.input_fidelity").Exists())
}

// 图像生成请求体不得携带 reasoning/thinking：顶层 model 应为 gpt-5.4-mini、gpt-image 作为
// image_generation 工具执行。带 reasoning/include 会把请求归类到 Codex/推理路径，徒增推理
// 开销且无益于出图。此断言固化"不走 thinking"，防止后续回归重新注入。
func TestBuildOpenAIImagesResponsesRequest_OmitsReasoningAndInclude(t *testing.T) {
	parsed := &OpenAIImagesRequest{
		Endpoint: openAIImagesGenerationsEndpoint,
		Model:    "gpt-image-2",
		Prompt:   "draw a cat",
		N:        1,
	}

	body, err := buildOpenAIImagesResponsesRequest(parsed, "gpt-image-2")
	require.NoError(t, err)
	require.NotNil(t, body)
	require.False(t, gjson.GetBytes(body, "reasoning").Exists(), "image request must not carry reasoning/thinking")
	require.False(t, gjson.GetBytes(body, "include").Exists(), "image request must not request reasoning.encrypted_content")
	require.Equal(t, openAIImagesResponsesMainModel, gjson.GetBytes(body, "model").String())
	require.Equal(t, "gpt-image-2", gjson.GetBytes(body, "tools.0.model").String())
}

func TestCollectOpenAIImagesFromResponsesBody_FallsBackToOutputItemDone(t *testing.T) {
	body := []byte(
		"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000004}}\n\n" +
			"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_123\",\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\",\"revised_prompt\":\"draw a cat\",\"output_format\":\"png\",\"quality\":\"high\"}}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000004,\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[]}}\n\n" +
			"data: [DONE]\n\n",
	)

	results, createdAt, usageRaw, firstMeta, foundFinal, err := collectOpenAIImagesFromResponsesBody(body)
	require.NoError(t, err)
	require.True(t, foundFinal)
	require.Equal(t, int64(1710000004), createdAt)
	require.Len(t, results, 1)
	require.Equal(t, "aGVsbG8=", results[0].Result)
	require.Equal(t, "draw a cat", results[0].RevisedPrompt)
	require.Equal(t, "png", firstMeta.OutputFormat)
	require.JSONEq(t, `{"images":1}`, string(usageRaw))
}

func TestCollectOpenAIImagesFromResponsesBody_MultilineSSE(t *testing.T) {
	body := []byte(
		"data: {\"type\":\"response.completed\",\n" +
			"data: \"response\":{\"created_at\":1710000010,\"usage\":{\"input_tokens\":5,\"output_tokens\":9,\"output_tokens_details\":{\"image_tokens\":4}},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"ZmluYWw=\",\"output_format\":\"png\"}]}}\n\n" +
			"data: [DONE]\n\n",
	)

	results, createdAt, usageRaw, firstMeta, foundFinal, err := collectOpenAIImagesFromResponsesBody(body)
	require.NoError(t, err)
	require.True(t, foundFinal)
	require.Equal(t, int64(1710000010), createdAt)
	require.Len(t, results, 1)
	require.Equal(t, "ZmluYWw=", results[0].Result)
	require.Equal(t, "png", firstMeta.OutputFormat)
	require.JSONEq(t, `{"images":1}`, string(usageRaw))
}

func TestOpenAIGatewayServiceForwardImages_OAuthStreamingHandlesOutputItemDoneFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"draw a cat","stream":true,"response_format":"url"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream_output_item_done"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_123\",\"type\":\"image_generation_call\",\"result\":\"ZmluYWw=\",\"revised_prompt\":\"draw a cat\",\"output_format\":\"png\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000005,\"usage\":{\"input_tokens\":5,\"output_tokens\":9,\"output_tokens_details\":{\"image_tokens\":4}},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc.httpUpstream = upstream

	account := &Account{
		ID:       5,
		Name:     "openai-oauth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	events := parseOpenAIImageTestSSEEvents(rec.Body.String())
	completed, ok := findOpenAIImageTestSSEEvent(events, "image_generation.completed")
	require.True(t, ok)
	require.Equal(t, "image_generation.completed", gjson.Get(completed.Data, "type").String())
	require.Equal(t, int64(1710000005), gjson.Get(completed.Data, "created_at").Int())
	require.Equal(t, "ZmluYWw=", gjson.Get(completed.Data, "b64_json").String())
	require.Equal(t, "data:image/png;base64,ZmluYWw=", gjson.Get(completed.Data, "url").String())
	require.Equal(t, "gpt-image-1.5", gjson.Get(completed.Data, "model").String())
	require.JSONEq(t, `{"images":1}`, gjson.Get(completed.Data, "usage").Raw)
	require.NotContains(t, rec.Body.String(), "event: error")
}

func TestOpenAIGatewayServiceForwardImages_OAuthStreamingHandlesMultilineSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"draw a cat","stream":true,"response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	svc.httpUpstream = &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream_multiline_oauth"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\n" +
					"data: \"response\":{\"created_at\":1710000011,\"usage\":{\"input_tokens\":6,\"output_tokens\":10,\"output_tokens_details\":{\"image_tokens\":5}},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"TXVsdGlsaW5l\",\"output_format\":\"png\"}]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}

	account := &Account{
		ID:       11,
		Name:     "openai-oauth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 6, result.Usage.InputTokens)
	require.Equal(t, 10, result.Usage.OutputTokens)
	require.Equal(t, 5, result.Usage.ImageOutputTokens)
	events := parseOpenAIImageTestSSEEvents(rec.Body.String())
	completed, ok := findOpenAIImageTestSSEEvent(events, "image_generation.completed")
	require.True(t, ok)
	require.Equal(t, "TXVsdGlsaW5l", gjson.Get(completed.Data, "b64_json").String())
	require.JSONEq(t, `{"images":1}`, gjson.Get(completed.Data, "usage").Raw)
	require.NotContains(t, rec.Body.String(), "event: error")
}

func TestForwardOpenAIImagesSimulation_APIKeyGenerationIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	encoded := testPNGBase64(t, 1024, 1024)
	upstreamBody := `{"created":1710000020,"model":"gpt-image-2","data":[{"b64_json":"` + encoded + `","revised_prompt":"expanded"}],"usage":{"input_tokens":30,"output_tokens":400,"total_tokens":430}}`

	run := func(t *testing.T, marked bool, channelMappedModel, requestModel, upstream string) (*OpenAIForwardResult, *httptest.ResponseRecorder) {
		t.Helper()
		body := []byte(`{"model":"` + requestModel + `","prompt":"draw a cat","size":"1024x1024","n":1}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = req

		svc := &OpenAIGatewayService{
			cfg: &config.Config{},
			httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(upstream)),
			}},
		}
		parsed, err := svc.ParseOpenAIImagesRequest(ctx, body)
		require.NoError(t, err)
		credentials := map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://image-upstream.example/v1",
		}
		if marked {
			credentials[openAIImagesUsageSimulationCredentialKey] = true
		}
		account := &Account{ID: 20, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: credentials}
		result, err := svc.ForwardImages(context.Background(), ctx, account, body, parsed, channelMappedModel)
		require.NoError(t, err)
		return result, recorder
	}

	t.Run("marked account", func(t *testing.T) {
		result, recorder := run(t, true, "", "gpt-image-2", upstreamBody)
		require.True(t, result.ImageUsageSimulated)
		require.Equal(t, 196, result.Usage.ImageOutputTokens)
		require.Equal(t, 30, result.Usage.InputTokens)
		require.Equal(t, int64(196), gjson.Get(recorder.Body.String(), "usage.output_tokens_details.image_tokens").Int())
		require.Equal(t, "opaque", gjson.Get(recorder.Body.String(), "background").String())
		require.Equal(t, "png", gjson.Get(recorder.Body.String(), "output_format").String())
		require.Equal(t, "low", gjson.Get(recorder.Body.String(), "quality").String())
		require.Equal(t, "1024x1024", gjson.Get(recorder.Body.String(), "size").String())
		require.Equal(t, encoded, gjson.Get(recorder.Body.String(), "data.0.b64_json").String())
		require.False(t, gjson.Get(recorder.Body.String(), "data.0.revised_prompt").Exists())
	})

	t.Run("unmarked account", func(t *testing.T) {
		result, recorder := run(t, false, "", "gpt-image-2", upstreamBody)
		require.False(t, result.ImageUsageSimulated)
		require.Equal(t, 400, result.Usage.OutputTokens)
		require.Equal(t, upstreamBody, recorder.Body.String())
	})

	t.Run("effective upstream model is not covered", func(t *testing.T) {
		result, recorder := run(t, true, "gpt-image-1", "gpt-image-2", upstreamBody)
		require.False(t, result.ImageUsageSimulated)
		require.Equal(t, 400, result.Usage.OutputTokens)
		require.Equal(t, upstreamBody, recorder.Body.String())
	})

	// 表外出图尺寸（web 逆向 / 超分路径）改由官方公式计费，不再放弃模拟。
	// 1254x1254 low 经公式为 229，恰与 codex 线实测文生图输出一致。
	t.Run("off-table output size simulates via formula", func(t *testing.T) {
		offTableBody := `{"created":1710000021,"model":"gpt-image-2","data":[{"b64_json":"` + testPNGBase64(t, 1254, 1254) + `"}],"usage":{"input_tokens":30,"output_tokens":400,"total_tokens":430}}`
		result, recorder := run(t, true, "", "gpt-image-2", offTableBody)
		require.True(t, result.ImageUsageSimulated)
		require.Equal(t, 229, result.Usage.ImageOutputTokens)
		require.Equal(t, 30, result.Usage.InputTokens)
		require.Equal(t, int64(229), gjson.Get(recorder.Body.String(), "usage.output_tokens_details.image_tokens").Int())
		require.Equal(t, "1254x1254", gjson.Get(recorder.Body.String(), "size").String())
	})

	// spec 集成清单 16：请求模型不在白名单（非仅 channel 映射后的上游模型）不模拟。
	t.Run("request model is not covered", func(t *testing.T) {
		for _, model := range []string{"gpt-image-1", "grok-imagine"} {
			t.Run(model, func(t *testing.T) {
				result, recorder := run(t, true, "", model, upstreamBody)
				require.False(t, result.ImageUsageSimulated)
				require.Equal(t, 400, result.Usage.OutputTokens)
				require.Equal(t, upstreamBody, recorder.Body.String())
			})
		}
	})
}

func TestForwardOpenAIImagesSimulation_RemoteEditUsesUpstreamAggregate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"make it night","images":[{"image_url":"https://example.com/input.png"}],"n":1}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req
	encoded := testPNGBase64(t, 1024, 1024)
	upstreamBody := `{"data":[{"b64_json":"` + encoded + `"}],"usage":{"input_tokens":1518,"input_tokens_details":{"image_tokens":1508,"text_tokens":10},"output_tokens":400}}`
	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(upstreamBody)),
		}},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(ctx, body)
	require.NoError(t, err)
	account := &Account{ID: 21, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key":                                "test-api-key",
		"base_url":                               "https://image-upstream.example/v1",
		openAIImagesUsageSimulationCredentialKey: true,
	}}

	result, err := svc.ForwardImages(context.Background(), ctx, account, body, parsed, "")
	require.NoError(t, err)
	require.True(t, result.ImageUsageSimulated)
	require.Equal(t, 1508, result.Usage.ImageInputTokens)
	require.Equal(t, 1518, result.Usage.InputTokens)
	require.Equal(t, 196, result.Usage.ImageOutputTokens)
	require.Equal(t, int64(1508), gjson.Get(recorder.Body.String(), "usage.input_tokens_details.image_tokens").Int())
	require.Equal(t, int64(196), gjson.Get(recorder.Body.String(), "usage.output_tokens_details.image_tokens").Int())
}

func TestForwardOpenAIImagesSimulation_RecordUsageBillsSynthesizedTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"make it night","images":[{"image_url":"https://example.com/input.png"}],"n":1}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req

	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
	svc.cfg.Default.RateMultiplier = 1
	svc.billingService.pricingService = &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-image-2": {
			InputCostPerToken:       5e-6,
			InputCostPerImageToken:  8e-6,
			OutputCostPerToken:      1e-5,
			OutputCostPerImageToken: 3e-5,
		},
	}}
	groupID := int64(26)
	svc.resolver = newOpenAIImageChannelPricingResolverForTest(t, groupID, "gpt-image-2", 0.25)
	encoded := testPNGBase64(t, 1024, 1024)
	svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"req_image_usage_simulated"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"` + encoded + `"}],"usage":{"input_tokens":1518,"input_tokens_details":{"image_tokens":1508,"text_tokens":10},"output_tokens":400}}`)),
	}}
	parsed, err := svc.ParseOpenAIImagesRequest(ctx, body)
	require.NoError(t, err)
	account := &Account{ID: 23, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key":                                "test-api-key",
		"base_url":                               "https://image-upstream.example/v1",
		openAIImagesUsageSimulationCredentialKey: true,
	}}
	user := &User{ID: 24}
	apiKey := &APIKey{ID: 25, User: user, GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}}

	result, err := svc.ForwardImages(context.Background(), ctx, account, body, parsed, "")
	require.NoError(t, err)
	require.True(t, result.ImageUsageSimulated)
	require.Equal(t, 1508, result.Usage.ImageInputTokens)
	require.Equal(t, 196, result.Usage.ImageOutputTokens)

	err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result:  result,
		APIKey:  apiKey,
		User:    user,
		Account: account,
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.NotNil(t, usageRepo.lastLog.BillingMode)
	require.Equal(t, string(BillingModeToken), *usageRepo.lastLog.BillingMode)
	require.InDelta(t, 0.00005, usageRepo.lastLog.InputCost, 1e-12)       // 文本 10×5e-6
	require.InDelta(t, 0.012064, usageRepo.lastLog.ImageInputCost, 1e-12) // 图片 1508×8e-6，独立入 ImageInputCost
	require.InDelta(t, 0.00588, usageRepo.lastLog.ImageOutputCost, 1e-12)
	require.InDelta(t, 0.017994, usageRepo.lastLog.TotalCost, 1e-12)
	require.Equal(t, 1, userRepo.deductCalls)
	require.InDelta(t, 0.017994, userRepo.lastAmount, 1e-12)
}

func TestForwardOpenAIImagesSimulation_OutputSizeDrivesBillingTier(t *testing.T) {
	// adobe 会按 quality 升档出图：请求 1280x720（1K 档字符串）+ high 实际产 3840x2160。
	// 计费档位必须跟真实出图走（4K/output），而不是回退到请求尺寸（2K/input）。
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"a lighthouse","size":"1280x720","quality":"high","n":1}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req

	encoded := testPNGBase64(t, 3840, 2160)
	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"` + encoded + `"}],"usage":{"input_tokens":5,"output_tokens":400}}`)),
		}},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(ctx, body)
	require.NoError(t, err)
	require.Equal(t, ImageBillingSize1K, parsed.SizeTier, "请求尺寸 1280x720 是 16:9 的 1K 档")
	account := &Account{ID: 40, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key":                                "test-api-key",
		"base_url":                               "https://image-upstream.example/v1",
		openAIImagesUsageSimulationCredentialKey: true,
	}}

	result, err := svc.ForwardImages(context.Background(), ctx, account, body, parsed, "")
	require.NoError(t, err)
	require.True(t, result.ImageUsageSimulated)
	require.Equal(t, []string{"3840x2160"}, result.ImageOutputSizes, "真实出图尺寸应被采集")

	ApplyOpenAIImageBillingResolution(result)
	require.Equal(t, "3840x2160", result.ImageOutputSize)
	require.Equal(t, ImageBillingSize4K, result.ImageSize, "档位应按实际出图归类")
	require.Equal(t, ImageSizeSourceOutput, result.ImageSizeSource)
	require.Equal(t, 13342, result.Usage.ImageOutputTokens)
}

func TestForwardOpenAIImagesSimulation_ChannelTokenPricingBillsImageInputAtChannelInputPrice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"make it night","images":[{"image_url":"https://example.com/input.png"}],"n":1}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req

	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
	svc.cfg.Default.RateMultiplier = 1
	pricingSvc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-image-2": {
			InputCostPerToken:       5e-6,
			InputCostPerImageToken:  8e-6,
			OutputCostPerToken:      1e-5,
			OutputCostPerImageToken: 3e-5,
		},
	}}
	svc.billingService.pricingService = pricingSvc
	groupID := int64(27)
	// 渠道配置 flat token 定价（input 3e-6）：图片输入 token 必须按渠道 input 价计费，
	// 不得让 LiteLLM 的 input_cost_per_image_token（8e-6）穿透渠道定价。
	svc.resolver = newOpenAITokenChannelPricingResolverWithLiteLLMForTest(t, groupID, "gpt-image-2", pricingSvc, nil)
	encoded := testPNGBase64(t, 1024, 1024)
	svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"req_image_usage_simulated_channel_token"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"` + encoded + `"}],"usage":{"input_tokens":1518,"input_tokens_details":{"image_tokens":1508,"text_tokens":10},"output_tokens":400}}`)),
	}}
	parsed, err := svc.ParseOpenAIImagesRequest(ctx, body)
	require.NoError(t, err)
	account := &Account{ID: 28, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key":                                "test-api-key",
		"base_url":                               "https://image-upstream.example/v1",
		openAIImagesUsageSimulationCredentialKey: true,
	}}
	user := &User{ID: 29}
	apiKey := &APIKey{ID: 30, User: user, GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}}

	result, err := svc.ForwardImages(context.Background(), ctx, account, body, parsed, "")
	require.NoError(t, err)
	require.True(t, result.ImageUsageSimulated)
	require.Equal(t, 1508, result.Usage.ImageInputTokens)
	require.Equal(t, 196, result.Usage.ImageOutputTokens)

	err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result:  result,
		APIKey:  apiKey,
		User:    user,
		Account: account,
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.NotNil(t, usageRepo.lastLog.BillingMode)
	require.Equal(t, string(BillingModeToken), *usageRepo.lastLog.BillingMode)
	// 文本 10×3e-6 入 InputCost；图片 1508 未配图片价回退 input 价 3e-6，独立入 ImageInputCost；图片输出按渠道 15e-6。
	require.InDelta(t, 0.00003, usageRepo.lastLog.InputCost, 1e-12)
	require.InDelta(t, 0.004524, usageRepo.lastLog.ImageInputCost, 1e-12)
	require.InDelta(t, 0.00294, usageRepo.lastLog.ImageOutputCost, 1e-12)
	require.InDelta(t, 0.007494, usageRepo.lastLog.TotalCost, 1e-12)
}

func TestForwardOpenAIImagesSimulation_ChannelExplicitImageInputPrice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"make it night","images":[{"image_url":"https://example.com/input.png"}],"n":1}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req

	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
	svc.cfg.Default.RateMultiplier = 1
	pricingSvc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-image-2": {
			InputCostPerToken:       5e-6,
			InputCostPerImageToken:  8e-6,
			OutputCostPerToken:      1e-5,
			OutputCostPerImageToken: 3e-5,
		},
	}}
	svc.billingService.pricingService = pricingSvc
	groupID := int64(31)
	// 渠道显式配置图片输入价 8e-6：图片输入按 8e-6 计，文本仍按渠道 input 价 3e-6。
	imageInputPrice := 8e-6
	svc.resolver = newOpenAITokenChannelPricingResolverWithLiteLLMForTest(t, groupID, "gpt-image-2", pricingSvc, &imageInputPrice)
	encoded := testPNGBase64(t, 1024, 1024)
	svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"req_image_usage_simulated_channel_image_input"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"` + encoded + `"}],"usage":{"input_tokens":1518,"input_tokens_details":{"image_tokens":1508,"text_tokens":10},"output_tokens":400}}`)),
	}}
	parsed, err := svc.ParseOpenAIImagesRequest(ctx, body)
	require.NoError(t, err)
	account := &Account{ID: 32, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key":                                "test-api-key",
		"base_url":                               "https://image-upstream.example/v1",
		openAIImagesUsageSimulationCredentialKey: true,
	}}
	user := &User{ID: 33}
	apiKey := &APIKey{ID: 34, User: user, GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1}}

	result, err := svc.ForwardImages(context.Background(), ctx, account, body, parsed, "")
	require.NoError(t, err)
	require.True(t, result.ImageUsageSimulated)

	err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result:  result,
		APIKey:  apiKey,
		User:    user,
		Account: account,
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	// text 10×3e-6 入 InputCost；image 1508×8e-6 = 0.012064 独立入 ImageInputCost；图片输出 196×15e-6 = 0.00294。
	require.InDelta(t, 0.00003, usageRepo.lastLog.InputCost, 1e-12)
	require.InDelta(t, 0.012064, usageRepo.lastLog.ImageInputCost, 1e-12)
	require.InDelta(t, 0.00294, usageRepo.lastLog.ImageOutputCost, 1e-12)
	require.InDelta(t, 0.015034, usageRepo.lastLog.TotalCost, 1e-12)
}

func TestForwardOpenAIImagesSimulation_ResponseFormatURLPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"url","n":1}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req
	upstreamBody := `{"data":[{"url":"https://cdn.example.com/image.png"}],"usage":{"input_tokens":3,"output_tokens":4}}`
	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(upstreamBody)),
		}},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(ctx, body)
	require.NoError(t, err)
	account := &Account{ID: 22, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key":                                "test-api-key",
		"base_url":                               "https://image-upstream.example/v1",
		openAIImagesUsageSimulationCredentialKey: true,
	}}

	result, err := svc.ForwardImages(context.Background(), ctx, account, body, parsed, "")
	require.NoError(t, err)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, upstreamBody, recorder.Body.String())
}

func TestOpenAIGatewayServiceForwardImages_OAuthStreamingDrainsAfterClientDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-1.5","prompt":"draw a cat","stream":true,"response_format":"url"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Writer = &failingOpenAIImageWriter{ResponseWriter: c.Writer, failAfter: 1}

	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				ImageStreamDataIntervalTimeout: 1,
				ImageStreamKeepaliveInterval:   0,
			},
		},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_stream_disconnect_oauth"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"cGFydGlhbA==\",\"partial_image_index\":0,\"output_format\":\"png\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000009,\"usage\":{\"input_tokens\":5,\"output_tokens\":9,\"output_tokens_details\":{\"image_tokens\":4}},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"ZmluYWw=\",\"output_format\":\"png\"}]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc.httpUpstream = upstream

	account := &Account{
		ID:       9,
		Name:     "openai-oauth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 9, result.Usage.OutputTokens)
	require.Equal(t, 4, result.Usage.ImageOutputTokens)
}

// Oversized n is rejected up front with a clear 400 (without this the
// upstream rejection surfaces as an opaque 502). Streaming for gpt-image-2 is
// rejected by new-api (its only upstream), keeping sub2api's generic image
// streaming pipeline intact for deployments with real streaming upstreams.
func TestParseOpenAIImagesRequestRejectsOversizedN(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}
	parse := func(body string) error {
		req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = req
		_, err := svc.ParseOpenAIImagesRequest(c, []byte(body))
		return err
	}
	if err := parse(`{"model":"gpt-image-2","prompt":"x","n":11}`); err == nil {
		t.Fatal("n=11 must be rejected with a clear 400")
	}
	if err := parse(`{"model":"gpt-image-2","prompt":"x","n":10}`); err != nil {
		t.Fatalf("n=10 is within the official range: %v", err)
	}
}

// gpt-image-2 的转发体必须剥掉 stream/partial_images：下游 new-api 的超分
// 资格判定见 stream≠false 会判不具资格，令 2K/4K 请求丧失超分。
func TestRewriteOpenAIImagesRequestStripsStreamForGPTImage2(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"x","size":"2048x2048","stream":true,"partial_images":2}`)
	parsed := &OpenAIImagesRequest{Model: "gpt-image-2", HasQuality: false}
	out, _, err := rewriteOpenAIImagesRequest(body, "application/json", "gpt-image-2", parsed)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "stream").Exists() {
		t.Fatalf("stream must be stripped from forward body: %s", out)
	}
	if gjson.GetBytes(out, "partial_images").Exists() {
		t.Fatalf("partial_images must be stripped from forward body: %s", out)
	}
	// 非 gpt-image-2 保留 stream（真流式上游）。
	body2 := []byte(`{"model":"gpt-image-1.5","prompt":"x","stream":true}`)
	out2, _, err := rewriteOpenAIImagesRequest(body2, "application/json", "gpt-image-1.5", &OpenAIImagesRequest{Model: "gpt-image-1.5"})
	if err != nil {
		t.Fatal(err)
	}
	if !gjson.GetBytes(out2, "stream").Exists() {
		t.Fatal("gpt-image-1.5 stream must be preserved")
	}
}
