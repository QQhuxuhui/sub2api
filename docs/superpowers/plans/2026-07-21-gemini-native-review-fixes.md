# Gemini Native Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve every generated image when Gemini SSE is aggregated, use the Gemini native fallback catalog for `/v1beta/models`, and remove the accidental npm lock file.

**Architecture:** Extend `collectGeminiSSE` with a narrowly scoped per-candidate image accumulator that distinguishes cumulative image snapshots from incremental chunks, then merge the accumulated images into the aggregate response before existing text merging and usage repair. Keep generic `/v1/models` behavior unchanged by adding an endpoint-specific fallback-ID helper in the Gemini native handler.

**Tech Stack:** Go, Gin, standard-library JSON/reflect helpers, gjson, testify, pnpm repository conventions.

## Global Constraints

- Aggregate only non-empty `inlineData` parts whose MIME type starts with `image/`.
- Preserve existing text aggregation, non-image parts, candidate metadata, terminal usage metadata, and streaming pass-through behavior.
- Identify candidates by explicit Gemini `index`, falling back to array position.
- Treat previous-prefix-of-incoming as cumulative, incoming-prefix-of-previous as duplicate/rewind, and all other sequences as incremental.
- Use `gemini.DefaultModels()` only for Gemini native fallback filtering; do not change generic `/v1/models` defaults.
- Keep `frontend/pnpm-lock.yaml` as the only frontend lock file.
- Do not modify unrelated dirty-worktree files or preview assets.

---

### Task 1: Aggregate Gemini SSE Images Across Chunks

**Files:**
- Modify: `backend/internal/service/gemini_flash_image_usage_repair_test.go`
- Modify: `backend/internal/service/gemini_messages_compat_service.go:2289-2454`

**Interfaces:**
- Consumes: `collectGeminiSSE(io.Reader, bool) (map[string]any, *ClaudeUsage, error)` and `repairGemini31FlashImageUsage([]byte, string, string, geminiFlashUsageRepairOptions)`.
- Produces: `geminiSSEImageAccumulator.observe(map[string]any)` and `geminiSSEImageAccumulator.mergeInto(map[string]any) map[string]any`.

- [ ] **Step 1: Add failing incremental and cumulative SSE regression tests**

Append these tests to `backend/internal/service/gemini_flash_image_usage_repair_test.go`:

```go
func TestCollectGeminiSSEPreservesImagesAcrossSeparateChunks(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/jpeg","data":"BBBB"}}]}}]}`,
		`data: {"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":4000,"totalTokenCount":4008},"modelVersion":"gemini-3.1-flash-image"}`,
		`data: [DONE]`,
		"",
	}, "\n")

	collected, _, err := collectGeminiSSE(strings.NewReader(stream), false)
	require.NoError(t, err)
	body, err := json.Marshal(collected)
	require.NoError(t, err)

	var images []string
	gjson.GetBytes(body, "candidates.0.content.parts").ForEach(func(_, part gjson.Result) bool {
		if data := part.Get("inlineData.data").String(); data != "" {
			images = append(images, data)
		}
		return true
	})
	require.Equal(t, []string{"AAAA", "BBBB"}, images)

	repairedBody, usage, repaired := repairGemini31FlashImageUsage(
		body,
		"gemini-3.1-flash-image",
		"2K",
		geminiFlashUsageRepairOptions{},
	)
	require.True(t, repaired)
	require.Equal(t, int64(3360), gjson.GetBytes(repairedBody, "usageMetadata.candidatesTokensDetails.0.tokenCount").Int())
	require.Equal(t, 3360, usage.ImageOutputTokens)
}

func TestCollectGeminiSSEDoesNotDuplicateCumulativeImages(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"index":0,"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}]}`,
		`data: {"candidates":[{"index":0,"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}},{"inlineData":{"mimeType":"image/jpeg","data":"BBBB"}}]}}]}`,
		`data: {"candidates":[{"index":0,"content":{"parts":[{"inlineData":{"mimeType":"image/webp","data":"CCCC"}}]}}]}`,
		`data: {"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":4000,"totalTokenCount":4008}}`,
		`data: [DONE]`,
		"",
	}, "\n")

	collected, _, err := collectGeminiSSE(strings.NewReader(stream), false)
	require.NoError(t, err)
	body, err := json.Marshal(collected)
	require.NoError(t, err)

	var images []string
	gjson.GetBytes(body, "candidates.0.content.parts").ForEach(func(_, part gjson.Result) bool {
		if data := part.Get("inlineData.data").String(); data != "" {
			images = append(images, data)
		}
		return true
	})
	require.Equal(t, []string{"AAAA", "BBBB", "CCCC"}, images)
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
cd backend
go test ./internal/service -run 'TestCollectGeminiSSE(PreservesImagesAcrossSeparateChunks|DoesNotDuplicateCumulativeImages)$' -count=1
```

Expected: both tests fail. The incremental test returns only `BBBB`; the cumulative-plus-incremental test returns only `CCCC` instead of `AAAA`, `BBBB`, `CCCC`.

- [ ] **Step 3: Implement the image accumulator and merge it before text aggregation**

Add `reflect` to the imports in `backend/internal/service/gemini_messages_compat_service.go`, then add the following immediately before `collectGeminiSSE`:

```go
type geminiSSEImageCandidateState struct {
	template     map[string]any
	images       []any
	lastSnapshot []any
}

type geminiSSEImageAccumulator struct {
	candidates map[int]*geminiSSEImageCandidateState
	order      []int
}

func (a *geminiSSEImageAccumulator) observe(response map[string]any) {
	if a == nil || response == nil {
		return
	}
	candidates, ok := response["candidates"].([]any)
	if !ok {
		return
	}
	if a.candidates == nil {
		a.candidates = make(map[int]*geminiSSEImageCandidateState)
	}
	for position, rawCandidate := range candidates {
		candidate, ok := rawCandidate.(map[string]any)
		if !ok {
			continue
		}
		images := geminiCandidateImageParts(candidate)
		if len(images) == 0 {
			continue
		}
		identity := geminiCandidateIdentity(candidate, position)
		state := a.candidates[identity]
		if state == nil {
			state = &geminiSSEImageCandidateState{}
			a.candidates[identity] = state
			a.order = append(a.order, identity)
		}
		state.template = candidate
		switch {
		case geminiPartsPrefix(state.lastSnapshot, images):
			state.images = append(state.images, images[len(state.lastSnapshot):]...)
		case geminiPartsPrefix(images, state.lastSnapshot):
			// Duplicate or rewind: retain the complete sequence already observed.
		default:
			state.images = append(state.images, images...)
		}
		state.lastSnapshot = images
	}
}

func (a *geminiSSEImageAccumulator) mergeInto(response map[string]any) map[string]any {
	if a == nil || len(a.candidates) == 0 || response == nil {
		return response
	}
	candidates, _ := response["candidates"].([]any)
	seen := make(map[int]struct{}, len(candidates))
	for position, rawCandidate := range candidates {
		candidate, ok := rawCandidate.(map[string]any)
		if !ok {
			continue
		}
		identity := geminiCandidateIdentity(candidate, position)
		state := a.candidates[identity]
		if state == nil {
			continue
		}
		candidates[position] = geminiCandidateWithImages(candidate, state.images)
		seen[identity] = struct{}{}
	}
	for _, identity := range a.order {
		if _, ok := seen[identity]; ok {
			continue
		}
		state := a.candidates[identity]
		candidates = append(candidates, geminiCandidateWithImages(state.template, state.images))
	}
	response["candidates"] = candidates
	return response
}

func geminiCandidateIdentity(candidate map[string]any, fallback int) int {
	if index, ok := asInt(candidate["index"]); ok {
		return index
	}
	return fallback
}

func geminiCandidateImageParts(candidate map[string]any) []any {
	content, _ := candidate["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	images := make([]any, 0, len(parts))
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if ok && isGeminiOutputImagePart(part) {
			images = append(images, rawPart)
		}
	}
	return images
}

func isGeminiOutputImagePart(part map[string]any) bool {
	inlineData, ok := part["inlineData"].(map[string]any)
	if !ok {
		return false
	}
	mimeType, _ := inlineData["mimeType"].(string)
	data, _ := inlineData["data"].(string)
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "image/") && data != ""
}

func geminiPartsPrefix(prefix, sequence []any) bool {
	if len(prefix) > len(sequence) {
		return false
	}
	for index := range prefix {
		if !reflect.DeepEqual(prefix[index], sequence[index]) {
			return false
		}
	}
	return true
}

func geminiCandidateWithImages(candidate map[string]any, images []any) map[string]any {
	out := make(map[string]any, len(candidate))
	for key, value := range candidate {
		out[key] = value
	}
	content, _ := candidate["content"].(map[string]any)
	contentCopy := make(map[string]any, len(content)+1)
	for key, value := range content {
		contentCopy[key] = value
	}
	parts, _ := content["parts"].([]any)
	merged := make([]any, 0, len(parts)+len(images))
	inserted := false
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if ok && isGeminiOutputImagePart(part) {
			if !inserted {
				merged = append(merged, images...)
				inserted = true
			}
			continue
		}
		merged = append(merged, rawPart)
	}
	if !inserted {
		merged = append(merged, images...)
	}
	contentCopy["parts"] = merged
	out["content"] = contentCopy
	return out
}
```

In `collectGeminiSSE`, initialize and consume the accumulator:

```go
	imageAccumulator := &geminiSSEImageAccumulator{}
	finish := func() (map[string]any, *ClaudeUsage, error) {
		result := pickGeminiCollectResult(last, lastWithParts)
		mergeGeminiTerminalMetadata(result, lastWithUsage)
		result = imageAccumulator.mergeInto(result)
		return mergeCollectedTextParts(result, collectedTextParts), usage, nil
	}
```

Inside the existing `if parsed != nil` block, call the observer before assigning `last`:

```go
					imageAccumulator.observe(parsed)
					last = parsed
```

- [ ] **Step 4: Run focused tests and verify GREEN**

Run:

```bash
cd backend
gofmt -w internal/service/gemini_messages_compat_service.go internal/service/gemini_flash_image_usage_repair_test.go
go test ./internal/service -run 'TestCollectGeminiSSE(PreservesImagesAcrossSeparateChunks|DoesNotDuplicateCumulativeImages|PreservesUsageFromMetadataOnlyFinalChunk)$' -count=1
```

Expected: all three tests pass.

- [ ] **Step 5: Run the service package regression suite**

Run:

```bash
cd backend
go test ./internal/service -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit the SSE fix without staging unrelated worktree files**

```bash
git add backend/internal/service/gemini_messages_compat_service.go backend/internal/service/gemini_flash_image_usage_repair_test.go
git commit -m "fix(gemini): preserve images across aggregated SSE chunks"
```

### Task 2: Use the Gemini Native Fallback Catalog

**Files:**
- Modify: `backend/internal/handler/gemini_v1beta_handler_test.go`
- Modify: `backend/internal/handler/gemini_v1beta_handler.go:96-110`

**Interfaces:**
- Consumes: `gemini.DefaultModels() []gemini.Model` and `defaultModelIDsForPlatform(string) []string`.
- Produces: `geminiV1BetaFallbackModelIDs(string) []string`.

- [ ] **Step 1: Add a failing native-only model regression test**

Add this subtest inside `TestGeminiCustomModelsListResponse`:

```go
	t.Run("Gemini原生回退保留native-only模型", func(t *testing.T) {
		group := &service.Group{
			Platform: service.PlatformGemini,
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{"gemini-3.1-pro-preview-customtools"},
			},
		}
		resp, ok := geminiCustomModelsListResponse(service.PlatformGemini, nil, group)
		require.True(t, ok)
		require.Len(t, resp.Models, 1)
		require.Equal(t, "models/gemini-3.1-pro-preview-customtools", resp.Models[0].Name)
	})
```

- [ ] **Step 2: Run the tagged handler test and verify RED**

Run:

```bash
cd backend
go test -tags=unit ./internal/handler -run 'TestGeminiCustomModelsListResponse/Gemini原生回退保留native-only模型' -count=1
```

Expected: FAIL because `resp.Models` is empty.

- [ ] **Step 3: Implement the endpoint-specific fallback helper**

Change `geminiCustomModelsListResponse` to call:

```go
	fallbackModels := geminiV1BetaFallbackModelIDs(platform)
```

Add this helper immediately after `geminiCustomModelsListResponse`:

```go
func geminiV1BetaFallbackModelIDs(platform string) []string {
	if platform != service.PlatformGemini {
		return defaultModelIDsForPlatform(platform)
	}
	models := gemini.DefaultModels()
	ids := make([]string, 0, len(models))
	for _, model := range models {
		id := strings.TrimPrefix(strings.TrimSpace(model.Name), "models/")
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
```

- [ ] **Step 4: Run the tagged handler tests and verify GREEN**

Run:

```bash
cd backend
gofmt -w internal/handler/gemini_v1beta_handler.go internal/handler/gemini_v1beta_handler_test.go
go test -tags=unit ./internal/handler -run 'TestGeminiCustomModelsListResponse' -count=1
```

Expected: PASS, including the native-only model subtest.

- [ ] **Step 5: Commit the model-list fix without staging unrelated worktree files**

```bash
git add backend/internal/handler/gemini_v1beta_handler.go backend/internal/handler/gemini_v1beta_handler_test.go
git commit -m "fix(gemini): use native fallback catalog for v1beta models"
```

### Task 3: Remove the Accidental npm Lock and Verify the Full Change

**Files:**
- Delete: `frontend/package-lock.json`

**Interfaces:**
- Consumes: repository package-manager convention in `Makefile` and `frontend/pnpm-lock.yaml`.
- Produces: a single frontend lock-file source of truth.

- [ ] **Step 1: Delete only the npm lock file**

```bash
rm frontend/package-lock.json
```

The file is generated and untracked; it can be regenerated with npm, while `frontend/pnpm-lock.yaml` remains untouched.

- [ ] **Step 2: Verify the lock-file state**

Run:

```bash
test -f frontend/pnpm-lock.yaml
test ! -e frontend/package-lock.json
git status --short
```

Expected: both `test` commands exit zero, and `frontend/package-lock.json` is absent from status. Existing unrelated `.docker-version`, preview HTML, and `_transparent_test` entries remain unchanged.

- [ ] **Step 3: Run focused and package-level Go regressions**

Run:

```bash
cd backend
go test ./internal/service -run 'TestCollectGeminiSSE|TestGeminiFlashImage|TestRepairGemini31Flash' -count=1
go test -tags=unit ./internal/handler -run 'TestGeminiCustomModelsListResponse' -count=1
go test ./internal/service ./internal/handler -count=1
go test -tags=unit ./internal/service ./internal/handler -count=1
```

Expected: all commands pass.

- [ ] **Step 4: Run static checks**

Run:

```bash
cd backend
go vet ./internal/service ./internal/handler
go vet -tags=unit ./internal/service ./internal/handler
cd ..
git diff --check
```

Expected: all commands exit zero with no diagnostics.

- [ ] **Step 5: Review the final scoped diff**

Run:

```bash
git diff -- backend/internal/service/gemini_messages_compat_service.go backend/internal/service/gemini_flash_image_usage_repair_test.go backend/internal/handler/gemini_v1beta_handler.go backend/internal/handler/gemini_v1beta_handler_test.go
git status --short
```

Expected: the diff contains only the approved service and handler fixes; unrelated pre-existing worktree changes remain present but untouched.

- [ ] **Step 6: Commit any remaining approved cleanup**

No commit is required for `frontend/package-lock.json` because it was untracked. If Task 1 and Task 2 were intentionally kept in one commit instead of their task commits, stage only their four explicit Go files and commit:

```bash
git add backend/internal/service/gemini_messages_compat_service.go backend/internal/service/gemini_flash_image_usage_repair_test.go backend/internal/handler/gemini_v1beta_handler.go backend/internal/handler/gemini_v1beta_handler_test.go
git commit -m "fix(gemini): address native response review findings"
```
