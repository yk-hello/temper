// Package buildinfo 这一份程序的版本（D119）。
//
// 版本号写在同目录的 VERSION 文件里，发布时由 tools/dev/release.sh 改：发布那一次是 "0.1.0"，
// 发布完马上改成 "0.1.0+dev"，表示"0.1.0 之后、还没发布的代码"。
// Commit 是构建时的提交号：发布的镜像由 release.yml 用 -ldflags "-X qs/internal/buildinfo.Commit=…" 填上；
// 自己用 docker compose 从源码构建时没有 .git，留空。
package buildinfo

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var raw string

// Commit 构建时的提交号（可能为空）。
var Commit string

// Version 版本号，比如 "0.1.0" 或 "0.1.0+dev"。
func Version() string { return strings.TrimSpace(raw) }

// Dev 是不是两次发布之间的开发版。
func Dev() bool { return strings.HasSuffix(Version(), "+dev") }
