// Package ledger 是复式记账的账本（决策 D09）：只追加、不修改，金额一律 Decimal。
// 每一笔分录的各行金额相加必须等于 0，否则拒绝写入。
//
// 账户约定（USDT 计价）：
//
//	cash             现金（保证金余额）
//	equity:capital   初始资金的来源
//	pnl:realized     已实现盈亏的对方账户（盈利时为负，亏损时为正）
//	expense:fee      手续费
//	pnl:funding      资金费的对方账户
//
// 持仓数量不是钱，不在账本里；由 portfolio 维护，并能从成交记录完整重放。
package ledger

import (
	"errors"
	"fmt"
	"sync"

	"github.com/shopspring/decimal"
)

const (
	Cash        = "cash"
	Capital     = "equity:capital"
	RealizedPnL = "pnl:realized"
	Fee         = "expense:fee"
	Funding     = "pnl:funding"
)

type Line struct {
	Account string          `json:"account"`
	Amount  decimal.Decimal `json:"amount"`
}

type Entry struct {
	Seq       int64  `json:"seq"`
	Time      int64  `json:"time"`
	Portfolio string `json:"portfolio"`
	Ref       string `json:"ref,omitempty"` // 关联的信号 id 或订单 id
	Memo      string `json:"memo"`
	Lines     []Line `json:"lines"`
	Trade     *Trade `json:"trade,omitempty"` // 成交分录带上成交明细：重启时按顺序重放即可还原持仓
}

// Trade 是一笔成交的明细。Qty 带符号：正 = 买入，负 = 卖出。
type Trade struct {
	Instrument string          `json:"instrument"`
	Qty        decimal.Decimal `json:"qty"`
	Px         decimal.Decimal `json:"px"`
	Fee        decimal.Decimal `json:"fee"`
	Maker      bool            `json:"maker"`
}

var ErrUnbalanced = errors.New("ledger: 分录不平衡")

// Ledger 线程安全。Persist 不为空时，每笔分录先持久化成功才算写入。
type Ledger struct {
	mu       sync.Mutex
	entries  []Entry
	balances map[string]map[string]decimal.Decimal // portfolio → account → balance
	unsynced []Entry                               // 还没写进数据库的分录（按顺序）
	Persist  func(Entry) error
}

func New() *Ledger {
	return &Ledger{balances: map[string]map[string]decimal.Decimal{}}
}

// Post 写入一笔分录。金额为 0 的行会被去掉；全为 0 时什么也不做。
func (l *Ledger) Post(e Entry) (Entry, error) {
	var lines []Line
	sum := decimal.Zero
	for _, ln := range e.Lines {
		if ln.Amount.IsZero() {
			continue
		}
		sum = sum.Add(ln.Amount)
		lines = append(lines, ln)
	}
	if len(lines) == 0 && e.Trade == nil {
		return e, nil
	}
	if !sum.IsZero() {
		return e, fmt.Errorf("%w：合计 %s", ErrUnbalanced, sum)
	}
	e.Lines = lines
	l.mu.Lock()
	defer l.mu.Unlock()
	e.Seq = int64(len(l.entries) + 1)
	// 先记进内存（成交已经发生，账必须记上），再持久化；持久化失败的放进待写队列，由 Flush 按顺序重试
	l.entries = append(l.entries, e)
	b := l.balances[e.Portfolio]
	if b == nil {
		b = map[string]decimal.Decimal{}
		l.balances[e.Portfolio] = b
	}
	for _, ln := range lines {
		b[ln.Account] = b[ln.Account].Add(ln.Amount)
	}
	if l.Persist != nil {
		if len(l.unsynced) > 0 {
			l.unsynced = append(l.unsynced, e)
		} else if err := l.Persist(e); err != nil {
			l.unsynced = append(l.unsynced, e)
			return e, err
		}
	}
	return e, nil
}

// Flush 重试写入之前没写进数据库的分录。返回还剩几笔没写进去。
func (l *Ledger) Flush() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for len(l.unsynced) > 0 && l.Persist != nil {
		if l.Persist(l.unsynced[0]) != nil {
			break
		}
		l.unsynced = l.unsynced[1:]
	}
	return len(l.unsynced)
}

func (l *Ledger) Balance(portfolio, account string) decimal.Decimal {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.balances[portfolio][account]
}

// Entries 返回某个组合的全部分录（副本）。
func (l *Ledger) Entries(portfolio string) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Entry
	for _, e := range l.entries {
		if e.Portfolio == portfolio {
			out = append(out, e)
		}
	}
	return out
}

// Load 从数据库恢复全部分录（按 Seq 顺序），不再触发 Persist。
func (l *Ledger) Load(entries []Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range entries {
		sum := decimal.Zero
		for _, ln := range e.Lines {
			sum = sum.Add(ln.Amount)
		}
		if !sum.IsZero() {
			return fmt.Errorf("%w：第 %d 笔合计 %s", ErrUnbalanced, e.Seq, sum)
		}
		l.entries = append(l.entries, e)
		b := l.balances[e.Portfolio]
		if b == nil {
			b = map[string]decimal.Decimal{}
			l.balances[e.Portfolio] = b
		}
		for _, ln := range e.Lines {
			b[ln.Account] = b[ln.Account].Add(ln.Amount)
		}
	}
	return nil
}

// HasPortfolio 账本里是否已有这个组合（有就说明初始资金已记过）。
func (l *Ledger) HasPortfolio(p string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.balances[p]
	return ok
}

// Check 校验整本账：所有账户余额相加为 0（复式记账的基本恒等式）。
func (l *Ledger) Check() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for p, b := range l.balances {
		sum := decimal.Zero
		for _, v := range b {
			sum = sum.Add(v)
		}
		if !sum.IsZero() {
			return fmt.Errorf("组合 %s 账不平：%s", p, sum)
		}
	}
	return nil
}
