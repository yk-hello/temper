package strategy

// 自助建策略的存储。需要数据库：QS_TEST_DB（会删 strategy_keys 表），不要指向正在用的库。

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/live"
)

func newStore(t *testing.T, master []byte) *Store {
	dsn := os.Getenv("QS_TEST_DB")
	if dsn == "" {
		t.Skip("没有设置 QS_TEST_DB，跳过数据库测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `drop table if exists strategy_keys`); err != nil {
		t.Fatal(err)
	}
	s := &Store{Pool: pool, C: live.NewStore(master), Reserved: func(id string) bool { return id == "env-one" }}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateRotateRevoke(t *testing.T) {
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	s := newStore(t, master)
	ctx := context.Background()
	for _, bad := range []string{"ab", "Has-Upper", "-lead", "with space", "demo-random"} {
		if _, err := s.Create(ctx, 1, bad, ""); !errors.Is(err, ErrBadID) {
			t.Fatalf("%q 应该不合格：%v", bad, err)
		}
	}
	if _, err := s.Create(ctx, 1, "env-one", ""); !errors.Is(err, ErrTaken) {
		t.Fatalf("环境变量里的名字不能用：%v", err)
	}
	k, err := s.Create(ctx, 1, "my-ma", "均线")
	if err != nil || len(k.Secret) != 64 {
		t.Fatalf("建：%v %+v", err, k)
	}
	if _, err := s.Create(ctx, 2, "my-ma", ""); !errors.Is(err, ErrTaken) {
		t.Fatalf("别人不能用同名：%v", err)
	}
	// 密钥加密存：库里没有原文；读回来能解开
	var box []byte
	_ = s.Pool.QueryRow(ctx, `select secret_enc from strategy_keys where key_id = $1`, k.KeyID).Scan(&box)
	if string(box) == k.Secret || len(box) < 40 {
		t.Fatal("密钥没加密")
	}
	act, err := s.Active(ctx)
	if err != nil || len(act) != 1 || act[0].Secret != k.Secret {
		t.Fatalf("读回来：%v %+v", err, act)
	}
	// 换密钥：旧的吊销、新的生效，还是同一个策略
	old, k2, err := s.Rotate(ctx, 1, "my-ma")
	if err != nil || old != k.KeyID || k2.KeyID == k.KeyID || k2.StrategyID != "my-ma" {
		t.Fatalf("换密钥：%v %s %+v", err, old, k2)
	}
	if _, _, err := s.Rotate(ctx, 2, "my-ma"); !errors.Is(err, ErrNotYour) {
		t.Fatalf("别人换不了：%v", err)
	}
	act, _ = s.Active(ctx)
	if len(act) != 1 || act[0].KeyID != k2.KeyID {
		t.Fatalf("换完只剩新的：%+v", act)
	}
	mine, _ := s.Mine(ctx, 1)
	if len(mine) != 1 || mine[0].Name != "均线" {
		t.Fatalf("我的策略：%+v", mine)
	}
	// 吊销：用户只能吊销自己的；管理员（user 0）都能
	if _, err := s.Revoke(ctx, 2, "my-ma"); !errors.Is(err, ErrNotYour) {
		t.Fatalf("别人吊销不了：%v", err)
	}
	if old, err := s.Revoke(ctx, 0, "my-ma"); err != nil || old != k2.KeyID {
		t.Fatalf("管理员吊销：%v %s", err, old)
	}
	if act, _ = s.Active(ctx); len(act) != 0 {
		t.Fatal("吊销后还在")
	}
	// 吊销了名字也不还：别人的跟随、记录还挂在这个名字上
	if _, err := s.Create(ctx, 2, "my-ma", ""); !errors.Is(err, ErrTaken) {
		t.Fatalf("吊销后别人还是不能用：%v", err)
	}
}

func TestNoMasterKey(t *testing.T) {
	s := newStore(t, nil)
	if _, err := s.Create(context.Background(), 1, "my-ma", ""); !errors.Is(err, ErrNoKey) {
		t.Fatalf("没有主密钥不能建：%v", err)
	}
}
