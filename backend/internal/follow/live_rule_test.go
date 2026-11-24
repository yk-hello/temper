package follow

import (
	"context"
	"errors"
	"testing"

	"qs/internal/auth"
)

// D99：交易所账户只能跟自己的策略；示例策略不能接真钱账户。
func TestLiveRule(t *testing.T) {
	owners := map[string]int64{"mine": 1, "theirs": 2}
	r := LiveRule{
		DemoID:   "demo-random",
		DemoOn:   true,
		Reserved: func(id string) bool { return id == "from-config" },
		Owner: func(_ context.Context, id string) (int64, error) {
			if id == "broken" {
				return 0, errors.New("db down")
			}
			return owners[id], nil
		},
		Env: func(_ context.Context, acct int64) string {
			return map[int64]string{10: "main", 11: "testnet", 12: "fake"}[acct]
		},
	}
	me := &auth.User{ID: 1}
	other := &auth.User{ID: 9}
	ctx := context.Background()
	for _, c := range []struct {
		name string
		u    *auth.User
		acct int64
		id   string
		want string
	}{
		{"自己建的策略", me, 10, "mine", ""},
		{"别人建的策略", me, 10, "theirs", codeNotOwn},
		{"没人建过的策略", me, 10, "nobody", codeNotOwn},
		{"查不到主人", me, 10, "broken", codeNotOwn},
		{"配置里的策略：这台的账号都能接（D104 独立使用）", me, 10, "from-config", ""},
		{"配置里的策略：另一个账号也能接", other, 10, "from-config", ""},
		{"示例策略接真钱账户", me, 10, "demo-random", codeDemoReal},
		{"示例策略接币安模拟交易", me, 11, "demo-random", ""},
		{"示例策略接测试交易所", me, 12, "demo-random", ""},
		{"示例策略：查不到环境当真钱", me, 99, "demo-random", codeDemoReal},
		{"没登录", nil, 11, "mine", codeNotOwn},
	} {
		if got := r.Check(ctx, c.u, c.acct, c.id); got != c.want {
			t.Errorf("%s：%q，应该是 %q", c.name, got, c.want)
		}
	}
	r.DemoOn = false
	if got := r.Check(ctx, me, 11, "demo-random"); got != codeNotOwn {
		t.Errorf("没开示例策略：%q", got)
	}
}
