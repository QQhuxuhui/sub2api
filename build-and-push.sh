#!/bin/bash
# =============================================================================
# gptniubi 分支专用：构建并推送镜像到阿里云 ACR
#
#   镜像线  registry.cn-shanghai.aliyuncs.com/hxh_ai/gptniubi:vN（与 dev 分支的
#           hxh_ai/sub2api 镜像线完全独立，两条线的版本号互不相干）
#   版本号  .docker-version 记录已发布的最新版本，每次运行 +1 并在推送成功后写回
#   部署    OVH /opt/gptniubi（容器 gptniubi，127.0.0.1:3006），见脚本末尾提示
#
# 用法：
#   ./build-and-push.sh            交互式（确认 + 是否用缓存）
#   ./build-and-push.sh -y         免交互，使用缓存
#   ./build-and-push.sh -y --no-cache
#   BUILD_HTTP_PROXY=http://127.0.0.1:7890 ./build-and-push.sh   构建期走代理
# =============================================================================
set -e

REGISTRY="registry.cn-shanghai.aliyuncs.com"
NAMESPACE="hxh_ai"
IMAGE_NAME="gptniubi"
VERSION_FILE=".docker-version"
PUSH_RETRIES=3   # ACR 认证偶发 TLS 握手超时，推送失败自动重试

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

ASSUME_YES=false
USE_CACHE=true
for arg in "$@"; do
    case "$arg" in
        -y|--yes) ASSUME_YES=true ;;
        --no-cache) USE_CACHE=false ;;
        -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
        *) echo -e "${RED}未知参数: $arg${NC}"; exit 1 ;;
    esac
done

cd "$(dirname "$0")"

# 只允许在 gptniubi 分支打这条镜像线，避免把 dev 的代码推到新站
BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
if [ "$BRANCH" != "gptniubi" ]; then
    echo -e "${RED}当前分支是 ${BRANCH}，此脚本只用于 gptniubi 分支（镜像线 ${NAMESPACE}/${IMAGE_NAME}）${NC}"
    exit 1
fi
if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
    echo -e "${YELLOW}提示：工作区有未提交的改动，镜像里的 COMMIT 标记将对不上实际代码${NC}"
fi

if [ ! -f "$VERSION_FILE" ]; then
    echo "0" > "$VERSION_FILE"
    echo -e "${YELLOW}创建版本文件: $VERSION_FILE${NC}"
fi
CURRENT_VERSION=$(cat "$VERSION_FILE")
NEW_VERSION=$((CURRENT_VERSION + 1))
FULL_IMAGE="${REGISTRY}/${NAMESPACE}/${IMAGE_NAME}"
COMMIT_HASH=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")

echo ""
echo "================================"
echo "Docker 镜像构建与推送（gptniubi）"
echo "================================"
echo "分支:     ${BRANCH}"
echo "镜像仓库: ${FULL_IMAGE}"
echo "版本号:   v${CURRENT_VERSION} → v${NEW_VERSION}"
echo "Commit:   ${COMMIT_HASH}"
echo "缓存:     $([ "$USE_CACHE" = true ] && echo 使用 || echo 不使用)"
echo "================================"
echo ""

if [ "$ASSUME_YES" = false ]; then
    read -p "是否继续构建并推送? (y/n): " -n 1 -r; echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then echo -e "${RED}操作已取消${NC}"; exit 1; fi
    read -p "是否使用 Docker 缓存加速构建? (y/n, 默认 y): " -n 1 -r; echo
    if [[ $REPLY =~ ^[Nn]$ ]]; then USE_CACHE=false; fi
fi

# ---- [1/3] 构建 ----------------------------------------------------------
# 不写根目录 VERSION 文件（由 CI 维护 backend/cmd/server/VERSION），
# 通过 --build-arg VERSION/COMMIT 注入到二进制；GOPROXY/GOSUMDB 与 deploy/build_image.sh 一致。
echo -e "${GREEN}[1/3] 正在构建镜像...${NC}"
BUILD_ARGS=(
    --build-arg "GOPROXY=https://goproxy.cn,direct"
    --build-arg "GOSUMDB=sum.golang.google.cn"
    --build-arg "VERSION=v${NEW_VERSION}"
    --build-arg "COMMIT=${COMMIT_HASH}"
    -t "${FULL_IMAGE}:v${NEW_VERSION}"
    -t "${FULL_IMAGE}:latest"
)
DOCKER_NET_ARGS=()
if [ -n "$BUILD_HTTP_PROXY" ]; then
    echo -e "${GREEN}使用构建代理: ${BUILD_HTTP_PROXY}（--network=host）${NC}"
    BUILD_ARGS+=(
        --build-arg "HTTP_PROXY=${BUILD_HTTP_PROXY}"
        --build-arg "HTTPS_PROXY=${BUILD_HTTP_PROXY}"
        --build-arg "http_proxy=${BUILD_HTTP_PROXY}"
        --build-arg "https_proxy=${BUILD_HTTP_PROXY}"
        --build-arg "NO_PROXY=localhost,127.0.0.1,goproxy.cn,sum.golang.google.cn,.aliyuncs.com"
        --build-arg "no_proxy=localhost,127.0.0.1,goproxy.cn,sum.golang.google.cn,.aliyuncs.com"
    )
    DOCKER_NET_ARGS=(--network=host)
fi
if [ "$USE_CACHE" = false ]; then
    docker build --no-cache "${DOCKER_NET_ARGS[@]}" "${BUILD_ARGS[@]}" .
else
    docker build "${DOCKER_NET_ARGS[@]}" "${BUILD_ARGS[@]}" .
fi
echo -e "${GREEN}✓ 镜像构建成功${NC}"

# ---- [2/3] 登录（已有登录态则跳过，免交互）-----------------------------
echo ""
echo -e "${GREEN}[2/3] 检查阿里云镜像仓库登录态...${NC}"
if grep -q "\"${REGISTRY}\"" "${HOME}/.docker/config.json" 2>/dev/null; then
    echo "已有 ${REGISTRY} 的登录凭据，跳过 docker login"
else
    docker login "${REGISTRY}"
fi

# ---- [3/3] 推送（带重试）+ 校验 -----------------------------------------
echo ""
echo -e "${GREEN}[3/3] 正在推送镜像...${NC}"
push_with_retry() {
    local ref="$1" n=1
    until docker push "$ref"; do
        if [ "$n" -ge "$PUSH_RETRIES" ]; then
            echo -e "${RED}推送 ${ref} 失败（已重试 ${PUSH_RETRIES} 次）${NC}"; return 1
        fi
        n=$((n + 1)); echo -e "${YELLOW}推送失败，${n}/${PUSH_RETRIES} 次重试...${NC}"; sleep 5
    done
}
push_with_retry "${FULL_IMAGE}:v${NEW_VERSION}"
push_with_retry "${FULL_IMAGE}:latest"
docker manifest inspect "${FULL_IMAGE}:v${NEW_VERSION}" >/dev/null 2>&1 \
    && echo -e "${GREEN}✓ 仓库中已能查到 v${NEW_VERSION} 的 manifest${NC}" \
    || { echo -e "${RED}仓库中查不到 v${NEW_VERSION}，请检查推送${NC}"; exit 1; }

echo "$NEW_VERSION" > "$VERSION_FILE"

echo ""
echo "================================"
echo -e "${GREEN}✓ 完成！${NC}"
echo "================================"
echo "版本号已更新: v${CURRENT_VERSION} → v${NEW_VERSION}（记得提交 ${VERSION_FILE}）"
echo ""
echo "已推送的镜像:"
echo "  - ${FULL_IMAGE}:v${NEW_VERSION}"
echo "  - ${FULL_IMAGE}:latest"
echo ""
echo "OVH 上切换版本:"
echo "  ssh ovh 'cd /opt/gptniubi \\"
echo "    && cp docker-compose.yml backups/docker-compose.yml.pre-v${NEW_VERSION}-\$(date +%Y%m%d_%H%M%S) \\"
echo "    && docker pull ${FULL_IMAGE}:v${NEW_VERSION} \\"
echo "    && sed -i \"s#${NAMESPACE}/${IMAGE_NAME}:v[0-9]*#${NAMESPACE}/${IMAGE_NAME}:v${NEW_VERSION}#\" docker-compose.yml \\"
echo "    && docker compose up -d && sleep 20 && curl -s http://127.0.0.1:3006/health'"
echo "================================"
