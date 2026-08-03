#!/bin/sh
# ainovel-cli 一键安装脚本
#
#   curl -fsSL https://raw.githubusercontent.com/voocel/ainovel-cli/main/scripts/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/voocel/ainovel-cli/main/scripts/install.sh | sh -s -- v1.2.3
#
# 自定义安装目录： AINOVEL_INSTALL_DIR=~/.local/bin curl -fsSL ... | sh
# 指定版本：AINOVEL_VERSION=v1.2.3 curl -fsSL ... | sh
set -e

REPO="voocel/ainovel-cli"
BIN="ainovel-cli"
AUDITOR="expansion-auditor"
COMPLETION_AUDITOR="manuscript-completion-auditor"
DEST="${AINOVEL_INSTALL_DIR:-/usr/local/bin}"
VERSION="${AINOVEL_VERSION:-${1:-latest}}"

for cmd in curl tar; do
	command -v "$cmd" >/dev/null 2>&1 || { echo "需要 $cmd，请先安装后重试"; exit 1; }
done

case "$(uname -s)" in
	Darwin) OS="Darwin" ;;
	Linux)  OS="Linux" ;;
	*) echo "不支持的系统 $(uname -s)；Windows 请到 https://github.com/$REPO/releases 手动下载"; exit 1 ;;
esac

case "$(uname -m)" in
	x86_64|amd64)  ARCH="x86_64" ;;
	arm64|aarch64) ARCH="arm64" ;;
	*) echo "不支持的架构 $(uname -m)"; exit 1 ;;
esac

if [ "$VERSION" = "latest" ] || [ -z "$VERSION" ]; then
	API="https://api.github.com/repos/$REPO/releases/latest"
	echo "查询最新版本..."
else
	case "$VERSION" in
		v*) TAG="$VERSION" ;;
		*) TAG="v$VERSION" ;;
	esac
	API="https://api.github.com/repos/$REPO/releases/tags/$TAG"
	echo "查询版本 $TAG..."
fi

RELEASE=$(curl -fsSL "$API")
TAG=$(printf '%s\n' "$RELEASE" | grep '"tag_name"' | head -1 | cut -d '"' -f 4)
URL=$(printf '%s\n' "$RELEASE" \
	| grep "browser_download_url" \
	| grep "_${OS}_${ARCH}.tar.gz" \
	| head -1 | cut -d '"' -f 4)
[ -n "$URL" ] || { echo "未找到 ${OS}_${ARCH} 安装包，请到 https://github.com/$REPO/releases 手动下载"; exit 1; }

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "下载 $URL"
curl -fsSL -o "$TMP/pkg.tar.gz" "$URL"
tar -xzf "$TMP/pkg.tar.gz" -C "$TMP"

echo "安装到 $DEST"
[ -d "$DEST" ] || mkdir -p "$DEST" 2>/dev/null || sudo mkdir -p "$DEST"
if [ -w "$DEST" ]; then
	mv "$TMP/$BIN" "$DEST/$BIN"
	mv "$TMP/$AUDITOR" "$DEST/$AUDITOR"
	mv "$TMP/$COMPLETION_AUDITOR" "$DEST/$COMPLETION_AUDITOR"
else
	echo "需要管理员权限写入 $DEST"
	sudo mv "$TMP/$BIN" "$DEST/$BIN"
	sudo mv "$TMP/$AUDITOR" "$DEST/$AUDITOR"
	sudo mv "$TMP/$COMPLETION_AUDITOR" "$DEST/$COMPLETION_AUDITOR"
fi
chmod +x "$DEST/$BIN" "$DEST/$AUDITOR" "$DEST/$COMPLETION_AUDITOR"

# The authority root and immutable installation anchor live outside user config.
# The parent and installation anchor stay administrator-owned so the service
# account cannot delete both siblings and bootstrap a replacement trust domain.
AUTHORITY_BASE=/var/lib/ainovel
AUTHORITY_ROOT=$AUTHORITY_BASE/publication-authority-v1
AUTHORITY_INSTALLATION=$AUTHORITY_BASE/publication-authority-installation-v1
SERVICE_USER=${AINOVEL_SERVICE_USER:-$(id -un)}
SERVICE_GROUP=${AINOVEL_SERVICE_GROUP:-$(id -gn)}
echo "配置 release-managed authority：service=$SERVICE_USER:$SERVICE_GROUP"
sudo install -d -o root -g root -m 0755 "$AUTHORITY_BASE"
sudo install -d -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0700 "$AUTHORITY_ROOT"
sudo install -d -o root -g root -m 0755 "$AUTHORITY_INSTALLATION"

if [ ! -f "$AUTHORITY_INSTALLATION/trust-pin.json" ]; then
	echo "执行一次性管理员 authority bootstrap"
	sudo "$DEST/$BIN" authority init
	sudo chown -R "$SERVICE_USER:$SERVICE_GROUP" "$AUTHORITY_ROOT"
	sudo chmod 0700 "$AUTHORITY_ROOT"
else
	echo "保留现有 installation anchor；安装程序不会删除或重建 trust pin"
fi

# 二进制未签名，macOS 首次运行会被 Gatekeeper 拦，解除隔离
[ "$OS" = "Darwin" ] && xattr -d com.apple.quarantine "$DEST/$BIN" 2>/dev/null || true
[ "$OS" = "Darwin" ] && xattr -d com.apple.quarantine "$DEST/$AUDITOR" 2>/dev/null || true
[ "$OS" = "Darwin" ] && xattr -d com.apple.quarantine "$DEST/$COMPLETION_AUDITOR" 2>/dev/null || true

echo "✓ 安装完成：$DEST/$BIN"
echo "✓ 独立扩写审核组件：$DEST/$AUDITOR"
echo "✓ 独立完稿审核组件：$DEST/$COMPLETION_AUDITOR"
[ -n "$TAG" ] && echo "版本：$TAG"
command -v "$BIN" >/dev/null 2>&1 || echo "提示：$DEST 不在 PATH 中，请将其加入 PATH"
echo "authority 已由管理员预置；普通 service account 不得运行 authority init"
echo "请备份 /var/lib/ainovel/publication-authority-v1 与 publication-authority-installation-v1；容器必须复用同一受管 volume。"
echo "运行 $BIN 开始使用"
