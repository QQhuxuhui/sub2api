# Gemini 3.1 Flash Image Usage Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 缺失 IMAGE usage 明细的 direct Gemini 3.1 Flash Image 响应按实际图片数和请求档位回填，并确保文本与图像 token 不重复计费。

**Architecture:** 新建独立的 flash usage repair 模块，提供纯函数响应修复和带状态的 SSE 分块修复。ForwardNative 的非流式、SSE 聚合和客户端 SSE 三条路径统一消费修复后的 body 与 `extractGeminiUsage` 结果。

**Tech Stack:** Go、gjson、sjson、Gin、标准库 testing、testify。

## Global Constraints

- 只处理 `gemini-3.1-flash-image` direct 请求和真正的生成 action。
- 空尺寸按 1K；0.5K/1K/2K/4K 分别为 747/1120/1680/2520 IMAGE token。
- 不修改 candidates/total/thoughts/model/tier/index，只增加缺失的 IMAGE 子明细。
- `OutputTokens` 包含 IMAGE，计费时通过减去 `ImageOutputTokens` 得到文本 token。
- 未实际出图、未知尺寸、已有明细和不自洽计数必须原样透传。

---

### Task 1: Flash usage 修复纯函数

**Files:**
- Create: `backend/internal/service/gemini_flash_image_usage_repair.go`
- Create: `backend/internal/service/gemini_flash_image_usage_repair_test.go`

**Interfaces:**
- Produces: `isGemini31FlashImageModel(string) bool`
- Produces: `gemini31FlashImageTokens(string) (int, bool)`
- Produces: `repairGemini31FlashImageUsage([]byte, string, string) ([]byte, *ClaudeUsage, bool)`

- [ ] **Step 1: Write failing table tests** covering model variants, all sizes, empty=1K, unknown refusal, actual-image requirement, existing IMAGE preservation, multiple images, other modality preservation, thoughts preservation, and `candidatesTokenCount < imageTokens` refusal.
- [ ] **Step 2: Run RED** with `go test ./internal/service -run 'TestGemini31Flash|TestRepairGemini31Flash' -count=1`; expect undefined function/build failures.
- [ ] **Step 3: Implement minimal helpers** using gjson for detection and all-or-nothing sjson writes. Return usage only by calling `extractGeminiUsage` on the final body.
- [ ] **Step 4: Run GREEN** with the same command; expect PASS.

### Task 2: 聚合和 SSE 状态处理

**Files:**
- Modify: `backend/internal/service/gemini_messages_compat_service.go`
- Modify: `backend/internal/service/gemini_messages_compat_service_test.go`
- Modify: `backend/internal/service/gemini_flash_image_usage_repair.go`
- Modify: `backend/internal/service/gemini_flash_image_usage_repair_test.go`

**Interfaces:**
- Produces: `geminiFlashImageStreamState.process([]byte, geminiImageUsageParams) ([]byte, *ClaudeUsage, bool)`
- Produces: `geminiImageUsageParams{Model, Tier, ProMaskEnabled, FlashRepairEnabled}`

- [ ] **Step 1: Write failing tests** where the image SSE chunk and terminal usage chunk are separate, plus an aggregated SSE response whose usage is in a metadata-only last chunk.
- [ ] **Step 2: Run RED** with focused test names; expect missing state/metadata behavior failures.
- [ ] **Step 3: Implement stateful repair** that remembers an observed image and repairs a later usage chunk without requiring `finishReason`.
- [ ] **Step 4: Preserve terminal usage during collection** by remembering the last parsed response containing `usageMetadata` and merging only that top-level object into the selected aggregate response.
- [ ] **Step 5: Run GREEN** for focused tests.

### Task 3: ForwardNative wiring and no-overlap regression

**Files:**
- Modify: `backend/internal/service/gemini_messages_compat_service.go`
- Modify: `backend/internal/service/gemini_messages_compat_service_test.go`
- Modify: `backend/internal/service/billing_service_test.go`

**Interfaces:**
- Consumes: Task 1 response repair and Task 2 stream state.

- [ ] **Step 1: Write failing handler tests** for non-streaming direct flash and generation-action gating.
- [ ] **Step 2: Write failing billing regression** with `OutputTokens=2000`, `ImageOutputTokens=1680`, text price `$3/MTok`, image price `$60/MTok`; assert text cost covers only 320 tokens and image cost only 1680 tokens.
- [ ] **Step 3: Run RED** and confirm failures are caused by missing wiring.
- [ ] **Step 4: Wire all three paths** so each writes the repaired body and returns usage parsed from that same body.
- [ ] **Step 5: Run focused GREEN** for repair, handler and billing tests.
- [ ] **Step 6: Run regression** with `go test ./internal/service ./internal/handler -count=1` and `go vet ./internal/service ./internal/handler`.

