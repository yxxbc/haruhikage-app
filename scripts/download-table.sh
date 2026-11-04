#!/usr/bin/env bash
# Prints the Markdown table of downloads, for the notes of a release: a row
# per platform, each linked to its file of the release, with badges of the
# version and the license on top.
#
#   scripts/download-table.sh 0.1.21 > table.md
#
# The file names are the ones `mygo build -upload` writes under the tag
# v<version>, so the links are known before anything is built: dashes
# between the slug, the version and the platform, except the deb's, which
# use underscores.
set -euo pipefail

version=${1:?usage: download-table.sh <version>}
repo=$(jq -r .updates.github mygo.json)
slug=$(jq -r .slug mygo.json)
tag="v$version"
base="https://github.com/$repo/releases/download/$tag"

# dashed and underlined name the file of the release for a target, as the
# tables link to it.
dashed() { printf '%s/%s-%s-%s' "$base" "$slug" "$version" "$1"; }
underlined() { printf '%s/%s_%s_%s' "$base" "$slug" "$version" "$1"; }

cat <<EOF
## 下载

[![version](https://img.shields.io/badge/version-${version}-blue?style=flat-square)](https://github.com/${repo}/releases/tag/${tag})
[![license](https://img.shields.io/badge/license-AGPL--3.0-green?style=flat-square)](https://github.com/${repo}/blob/main/LICENSE)

| 平台 | 安装包 |
| --- | --- |
| macOS（Apple 芯片） | [DMG]($(dashed arm64.dmg)) |
| macOS（Intel） | [DMG]($(dashed amd64.dmg)) |
| Windows | [安装包](${base}/${slug}-setup-${version}-amd64.exe) · [ARM64](${base}/${slug}-setup-${version}-arm64.exe) |
| Linux（Debian / Ubuntu） | [deb amd64]($(underlined amd64.deb)) · [deb arm64]($(underlined arm64.deb)) |
| Linux（其他发行版） | [tar.gz amd64]($(dashed linux-amd64.tar.gz)) · [tar.gz arm64]($(dashed linux-arm64.tar.gz)) |

macOS 的包是 ad hoc 签名的，第一次打开要右键「打开」，或者执行：

\`\`\`sh
xattr -dr com.apple.quarantine /Applications/春日影.app
\`\`\`

命令行安装：

\`\`\`sh
curl -fsSL https://github.com/${repo}/releases/download/${tag}/install.sh | sh
\`\`\`
EOF
