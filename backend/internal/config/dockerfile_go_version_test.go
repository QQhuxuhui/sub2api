package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Dockerfile 的 golang 基础镜像必须与 backend/go.mod 的 `go` 指令一致。
//
// 官方 golang 镜像内置 GOTOOLCHAIN=local：基础镜像版本低于 go.mod 要求时，
// `go mod download` 直接失败（不会自动下载更高版本的工具链），报
// `go.mod requires go >= X (running go Y; GOTOOLCHAIN=local)`。
//
// 这个不一致上游 v0.1.177 自己就有（go.mod 要 1.26.6、Dockerfile 写 1.26.5）。
// 它的 CI 用 `go-version-file: backend/go.mod` 自动取版本，所以 CI 全绿，
// 只有「自己 docker build 镜像」的人会撞上 —— 本仓正是这种用法，
// 而且是在部署当天才发现，属于最贵的时机。
//
// 用测试而不是改 .github/workflows/：那些文件是上游的，改了会增加每次同步的冲突面。
func TestDockerfileGolangImageMatchesGoMod(t *testing.T) {
	root := repoRoot(t)

	goMod, err := os.ReadFile(filepath.Join(root, "backend", "go.mod"))
	require.NoError(t, err)
	goDirective := regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)\s*$`).FindSubmatch(goMod)
	require.Len(t, goDirective, 2, "backend/go.mod 里找不到 `go X.Y.Z` 指令")
	want := string(goDirective[1])

	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	require.NoError(t, err)
	image := regexp.MustCompile(`(?m)^ARG GOLANG_IMAGE=golang:([^\s-]+)`).FindSubmatch(dockerfile)
	require.Len(t, image, 2, "Dockerfile 里找不到 `ARG GOLANG_IMAGE=golang:...`")
	got := string(image[1])

	require.Equal(t, want, got,
		"Dockerfile 的 golang 基础镜像(%s)与 backend/go.mod 要求(%s)不一致。\n"+
			"官方 golang 镜像内置 GOTOOLCHAIN=local，低于要求时 `go mod download` 会直接失败，\n"+
			"而 CI 用 go-version-file 自动取版本、察觉不到 —— 只有 docker build 会炸。\n"+
			"同步上游后请把 Dockerfile 的 ARG GOLANG_IMAGE 改成 golang:%s-alpine。",
		got, want, want)
}

// repoRoot 从当前包向上找到含 .git 的目录。
// 不写死相对层级：包挪位置时这条测试不该跟着坏。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir || strings.TrimSpace(parent) == "" {
			t.Fatalf("从 %s 向上找不到仓库根（含 .git 的目录）", dir)
		}
		dir = parent
	}
}
