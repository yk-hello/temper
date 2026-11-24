package live

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// D100：一条命令启动时主密钥自动生成、存进文件；重启读回同一把；写错的不悄悄换。
func TestMasterKeyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "master.key")
	t.Setenv("QS_MASTER_KEY", "")
	t.Setenv("QS_MASTER_KEY_FILE", "")
	if MasterFromEnv() != nil {
		t.Fatal("都没配：没有主密钥")
	}
	t.Setenv("QS_MASTER_KEY_FILE", path)
	a := MasterFromEnv()
	if len(a) != 32 {
		t.Fatalf("第一次：应该生成一把 32 字节的（%d）", len(a))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if dec, _ := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(raw))); !bytes.Equal(dec, a) {
		t.Fatal("文件里存的和返回的不一样")
	}
	if b := MasterFromEnv(); !bytes.Equal(a, b) {
		t.Fatal("第二次：要读回同一把（不然以前存的密钥解不开）")
	}
	// 文件坏了：不覆盖
	if err := os.WriteFile(path, []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if MasterFromEnv() != nil {
		t.Fatal("文件坏了应该返回 nil")
	}
	if raw, _ := os.ReadFile(path); string(raw) != "not-a-key" {
		t.Fatal("坏文件被覆盖了")
	}
	// 环境变量优先；写错了不退回文件
	want := bytes.Repeat([]byte{7}, 32)
	t.Setenv("QS_MASTER_KEY", base64.StdEncoding.EncodeToString(want))
	if got := MasterFromEnv(); !bytes.Equal(got, want) {
		t.Fatal("配了 QS_MASTER_KEY 就用它")
	}
	t.Setenv("QS_MASTER_KEY", "too-short")
	if MasterFromEnv() != nil {
		t.Fatal("QS_MASTER_KEY 写错了应该返回 nil")
	}
}
