package buildinfo

import (
	"regexp"
	"testing"
)

// 版本号要是 X.Y.Z 或 X.Y.Z+dev：发布脚本、"系统"页、更新记录都按这个格式认
func TestVersionFormat(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(\+dev)?$`).MatchString(Version()) {
		t.Fatalf("VERSION 格式不对：%q", Version())
	}
}
