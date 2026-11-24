package follow

import (
	"context"

	"qs/internal/auth"
)

// LiveRule 交易所账户（真钱）能跟哪些策略（D99）。返回拒绝的代码，空 = 可以：
//   - 自己建的策略：可以
//   - 部署时在配置里写的策略：算部署的人（所有者）的，只有所有者可以
//   - 别人的策略：不行，只能跟到模拟账户——别人的信号直接操作自己的真钱账户就是带单（D90 不做）
//   - 内置示例策略（随机信号）：开着示例时可以接币安模拟交易和测试用的交易所，不能接真钱账户
type LiveRule struct {
	DemoID   string                                              // 内置示例策略的编号
	DemoOn   bool                                                // 这次部署开没开示例策略
	Reserved func(id string) bool                                // 配置里写的策略
	Owner    func(ctx context.Context, id string) (int64, error) // 自己建的策略是谁建的；没有返回 0
	Env      func(ctx context.Context, acct int64) string        // 交易所账户的环境：main（真钱）、testnet、fake
}

const (
	codeNotOwn   = "LIVE_OWN_STRATEGY"
	codeDemoReal = "LIVE_DEMO_REAL"
)

func (r LiveRule) Check(ctx context.Context, u *auth.User, acct int64, id string) string {
	if u == nil {
		return codeNotOwn
	}
	if id == r.DemoID && r.DemoID != "" {
		if !r.DemoOn {
			return codeNotOwn
		}
		if r.Env == nil {
			return codeDemoReal
		}
		if env := r.Env(ctx, acct); env == "" || env == "main" { // 查不到环境也当真钱
			return codeDemoReal
		}
		return ""
	}
	if r.Reserved != nil && r.Reserved(id) {
		return "" // 部署时在配置里写的策略：这台是部署的人自己用的（D104），他的账号都能接
	}
	if r.Owner == nil {
		return codeNotOwn
	}
	if owner, err := r.Owner(ctx, id); err != nil || owner == 0 || owner != u.ID {
		return codeNotOwn
	}
	return ""
}
