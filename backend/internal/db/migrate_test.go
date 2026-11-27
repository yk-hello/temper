package db

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 数据库升级只加不删（D119）：升级到新版本以后，还能退回上一个版本接着用同一个库。所以建表、加字段可以，
// 不能删表、删字段、改字段类型、改名；新加的必填字段（not null）要有默认值，不然旧版往里写数据会失败。
// 真要去掉的：先在一个版本里停用，至少隔一个版本再删，并且在下面登记。
//
// 已有的几条是下线"分享链接 / 邀请"（D90）时的清理，在第一个版本 v0.1.0 之前，登记在这里。
var allowDrop = []string{
	"internal/highlight/store.go: alter table highlights drop column if exists token",
	"internal/highlight/store.go: alter table highlights drop column if exists kind",
	"internal/highlight/store.go: alter table highlights drop column if exists show_amount",
	"internal/highlight/store.go: alter table highlights drop column if exists hidden",
	"internal/highlight/store.go: execute 'alter table highlights drop column deleted'",
	"internal/highlight/store.go: drop table if exists invites",
	"internal/highlight/store.go: drop table if exists invite_codes",
}

func TestMigrationsOnlyAdd(t *testing.T) {
	root := filepath.Join("..", "..") // backend/
	destructive := regexp.MustCompile(`(?i)\b(drop\s+(table|column|constraint)|alter\s+column|rename\s+(to|column)|truncate\s+table)\b`)
	addCol := regexp.MustCompile(`(?i)\badd\s+column\b`)
	allowed := map[string]bool{}
	for _, a := range allowDrop {
		allowed[a] = true
	}
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))
		for i, line := range strings.Split(string(b), "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "//") {
				continue
			}
			if destructive.MatchString(l) {
				key := rel + ": " + strings.TrimRight(l, ";")
				seen[key] = true
				if !allowed[key] {
					t.Errorf("%s:%d 删除或改动了已有的表结构，退回上一个版本时会出问题（D119）：%s", rel, i+1, l)
				}
			}
			if addCol.MatchString(l) && regexp.MustCompile(`(?i)\bnot\s+null\b`).MatchString(l) && !regexp.MustCompile(`(?i)\bdefault\b`).MatchString(l) {
				t.Errorf("%s:%d 新加的必填字段没有默认值，旧版本往这张表写数据会失败（D119）：%s", rel, i+1, l)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range allowDrop {
		if !seen[a] {
			t.Errorf("登记的这一条在代码里已经找不到了，从 allowDrop 里删掉：%s", a)
		}
	}
}
