package live

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/binance"
	"qs/internal/trading"
)

// 对账差异告警（18 第 6.4 条，D10"对不上就报警，并以交易所为准"）。
// 界面上的数一直以交易所为准；这里只负责发现"和我们知道的对不上"，告诉用户：
//   - external：交易所上有一笔不是从这里下的成交（币安 App 上自己下的，或者密钥被别人用了）
//   - liq / adl：交易所强平了、自动减仓了
//   - money：钱包变了，交易、手续费、资金费解释不了（划转、强平清算费……），用交易所的资金流水说明
//   - fail / ok：对账连续失败（密钥被删、IP 白名单、权限变了），以及恢复
// 提醒走通知（站内、Telegram、Webhook），算和钱有关的。

var (
	reconTick   = newKnob(15 * time.Second) // 没有推送时多久对一次账
	suspectWait = newKnob(2 * time.Second)  // 不是从这里下的成交，等这么久再判断（条件单触发后的单号要等 ALGO_UPDATE）
	failAfter   = newKnob(2 * time.Minute)  // 对账连续失败这么久才提醒（网络抖一下不算）
	driftMin    = decimal.NewFromInt(1)
)

// knob 一个时长设置：测试里会在别的账户还在跑的时候改它，所以用原子读写。
type knob struct{ v atomic.Int64 }

func newKnob(d time.Duration) *knob { k := &knob{}; k.set(d); return k }
func (k *knob) get() time.Duration  { return time.Duration(k.v.Load()) }
func (k *knob) set(d time.Duration) { k.v.Store(int64(d)) }

type suspect struct {
	order       int64
	sym, side   string
	qty, px     decimal.Decimal // 数量累加，价格按数量加权
	first       time.Time
	client, why string
}

type recon struct {
	suspects   map[int64]*suspect
	ours       map[int64]bool // 我们的条件单触发后在交易所生成的单号
	alerted    map[int64]bool // 已经提醒过的单号（强平、外部单）
	base       decimal.Decimal
	baseSet    bool
	baseAt     time.Time
	failSince  time.Time
	failSent   bool
	lastErrMsg string
}

func (d *Desks) alert(k *desk, what string, kv map[string]string) {
	if d.OnAlert == nil {
		return
	}
	p := map[string]string{"what": what, "account": k.a.Name}
	for a, b := range kv {
		p[a] = b
	}
	d.OnAlert(k.a.User, p)
}

// liqKind 币安自己下的单：强平是 autoclose-，自动减仓是 adl_autoclose。
func liqKind(client string) string {
	switch {
	case strings.HasPrefix(client, "adl_autoclose"):
		return "adl"
	case strings.HasPrefix(client, "autoclose-"):
		return "liq"
	}
	return ""
}

// noteTrade 推送来的每一笔成交：从这里下的不管；强平、自动减仓马上提醒；别的先记下来，过一会儿再判断。
func (d *Desks) noteTrade(k *desk, u *binance.OrderUpdate) {
	if strings.HasPrefix(u.ClientOrderID, "qs") {
		return
	}
	qty, px := dec(u.LastQty), dec(u.LastPrice)
	k.mu.Lock()
	defer k.mu.Unlock()
	r := &k.rc
	if r.suspects == nil {
		r.suspects, r.ours, r.alerted = map[int64]*suspect{}, map[int64]bool{}, map[int64]bool{}
	}
	s := r.suspects[u.OrderID]
	if s == nil {
		s = &suspect{order: u.OrderID, sym: u.Symbol, side: sideLow(u.Side), first: time.Now(), client: u.ClientOrderID, why: liqKind(u.ClientOrderID)}
		r.suspects[u.OrderID] = s
	}
	if n := s.qty.Add(qty); n.Sign() > 0 {
		s.px = s.px.Mul(s.qty).Add(px.Mul(qty)).Div(n)
		s.qty = n
	}
}

// noteAlgo ALGO_UPDATE：我们的条件单触发后，交易所下的那张单的编号记下来（那张单的客户端编号可能不是我们的）。
func (d *Desks) noteAlgo(k *desk, raw json.RawMessage) {
	var ev struct {
		O struct {
			Client string          `json:"caid"`
			Actual json.RawMessage `json:"ai"`
		} `json:"o"`
	}
	if json.Unmarshal(raw, &ev) != nil || !strings.HasPrefix(ev.O.Client, "qs") {
		return
	}
	id, err := strconv.ParseInt(strings.Trim(string(ev.O.Actual), `"`), 10, 64)
	if err != nil || id == 0 {
		return
	}
	k.mu.Lock()
	if k.rc.ours == nil {
		k.rc.suspects, k.rc.ours, k.rc.alerted = map[int64]*suspect{}, map[int64]bool{}, map[int64]bool{}
	}
	k.rc.ours[id] = true
	k.mu.Unlock()
}

// checkTrades 等够时间的可疑成交：是我们的条件单触发的就放过，否则提醒（一张单只提醒一次）。
func (d *Desks) checkTrades(ctx context.Context, k *desk) {
	now := time.Now()
	k.mu.Lock()
	var due []*suspect
	for id, s := range k.rc.suspects {
		if s.why != "" || now.Sub(s.first) >= suspectWait.get() {
			delete(k.rc.suspects, id)
			if !k.rc.ours[id] && !k.rc.alerted[id] {
				k.rc.alerted[id] = true
				due = append(due, s)
			}
		}
	}
	k.mu.Unlock()
	for _, s := range due {
		if s.why == "" && d.S.pool != nil {
			var mine bool // 本地记过这张单（例如老写法的条件单，触发后单号不变）
			_ = d.S.pool.QueryRow(ctx, `select exists (select 1 from live_orders where account=$1 and (id=$2 or client_id=$3))`, k.a.ID, "b"+strconv.FormatInt(s.order, 10), s.client).Scan(&mine)
			if mine {
				continue
			}
		}
		what := s.why
		if what == "" {
			what = "external"
		}
		d.alert(k, what, map[string]string{"inst": instOf(s.sym), "side": s.side, "qty": s.qty.String(), "px": s.px.String()})
	}
}

// checkMoney 钱包里"本金"（钱包 − 已实现 − 资金费 + 手续费）应该不变；变了说明有交易以外的进出。
// 先补一次成交和资金费（推送可能丢了），还对不上再用交易所的资金流水说明，提醒一次，然后以交易所为准重新起算。
func (d *Desks) checkMoney(ctx context.Context, k *desk, v *trading.AccountView) {
	now := time.Now()
	k.mu.Lock()
	r := &k.rc
	if !r.baseSet {
		r.base, r.baseSet, r.baseAt = v.Initial, true, now
		k.mu.Unlock()
		return
	}
	base, at := r.base, r.baseAt
	k.mu.Unlock()
	if v.Initial.Sub(base).Abs().LessThan(driftMin) {
		return
	}
	d.syncFills(ctx, k)
	s := d.sums(ctx, k.a.ID)
	v.Realized, v.Fees, v.Funding = s.realized, s.fees, s.funding
	v.Initial = v.Wallet.Sub(s.realized).Sub(s.funding).Add(s.fees)
	diff := v.Initial.Sub(base)
	if diff.Abs().LessThan(driftMin) {
		return
	}
	// 交易所的资金流水：从上次对上的时候往前多看 10 分钟
	kv := map[string]string{"amount": diff.StringFixed(2)}
	explained := zero
	if inc, err := k.c.Income(ctx, at.Add(-10*time.Minute).UnixMilli()); err == nil {
		sum := map[string]decimal.Decimal{}
		for _, x := range inc {
			if x.Asset != "" && x.Asset != "USDT" {
				continue
			}
			switch x.IncomeType {
			case "TRANSFER", "INTERNAL_TRANSFER", "CROSS_COLLATERAL_TRANSFER":
				sum["transfer"] = sum["transfer"].Add(dec(x.Income))
			case "INSURANCE_CLEAR":
				sum["insurance"] = sum["insurance"].Add(dec(x.Income))
			case "COMMISSION_REBATE", "API_REBATE", "REFERRAL_KICKBACK", "WELCOME_BONUS":
				sum["rebate"] = sum["rebate"].Add(dec(x.Income))
			}
		}
		for name, v := range sum {
			if !v.IsZero() {
				kv[name] = v.StringFixed(2)
				explained = explained.Add(v)
			}
		}
	}
	k.mu.Lock()
	r.base, r.baseAt = v.Initial, now
	k.mu.Unlock()
	// 只是返佣：不打扰
	if kv["transfer"] == "" && kv["insurance"] == "" && kv["rebate"] != "" && diff.Sub(explained).Abs().LessThan(driftMin) {
		return
	}
	if rest := diff.Sub(explained); rest.Abs().GreaterThanOrEqual(driftMin) && len(kv) > 1 {
		kv["rest"] = rest.StringFixed(2)
	}
	d.alert(k, "money", kv)
}

// checkFail 对账失败：连续 failAfter 才提醒一次；恢复时再说一声。
func (d *Desks) checkFail(k *desk, err error) {
	now := time.Now()
	k.mu.Lock()
	r := &k.rc
	if err == nil {
		sent := r.failSent
		r.failSince, r.failSent = time.Time{}, false
		k.mu.Unlock()
		if sent {
			d.alert(k, "ok", nil)
		}
		return
	}
	if r.failSince.IsZero() {
		r.failSince = now
	}
	send := !r.failSent && now.Sub(r.failSince) >= failAfter.get()
	if send {
		r.failSent = true
	}
	k.mu.Unlock()
	if send {
		d.alert(k, "fail", map[string]string{"err": errBrief(err)})
	}
}

// errBrief 给人看的错误：币安的错误码和一句话，不带地址和签名。
func errBrief(err error) string {
	var be *binance.APIError
	if errors.As(err, &be) {
		return strconv.Itoa(be.Code) + " " + be.Msg
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	s := err.Error()
	if i := strings.Index(s, "?"); i > 0 { // 别把带签名的地址发出去
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
