package onchain

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 链上页（12 的 V.15–V.20）：存盘、覆盖率、钱包盈亏分布、最大地址提高刷新频率。

// PrioDefault 刷新频率提高的地址数：持仓金额最大的这么多个。
const PrioDefault = 100

// maxEventGap 两次扫描隔得太久（后端停过），这之间的变化不知道是什么时候发生的，不报动作。
const maxEventGap = 15 * 60 * 1000

// ---------- 调度 ----------

// queues 分两队：持仓金额最大的一批（每 3 次查询占 1 次），其余的。
func (s *Scanner) queues() (prio, rest []string) {
	n := s.PrioN
	if n == 0 {
		n = PrioDefault
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	type av struct {
		a string
		v float64
	}
	all := make([]av, 0, len(s.accounts))
	for a := range s.accounts {
		v := 0.0
		for _, p := range s.pos[a] {
			v += p.Value
		}
		all = append(all, av{a, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].a < all[j].a
	})
	for i, x := range all {
		if i < n && x.v > 0 {
			prio = append(prio, x.a)
		} else {
			rest = append(rest, x.a)
		}
	}
	return prio, rest
}

// loadOI 取 Hyperliquid 每个币的总持仓量，算覆盖率用。
func (s *Scanner) loadOI(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{"type": "metaAndAssetCtxs"})
	req, err := http.NewRequestWithContext(ctx, "POST", s.InfoURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	var raw []json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return err
	}
	if len(raw) < 2 {
		return fmt.Errorf("返回格式不对")
	}
	var meta struct {
		Universe []struct {
			Name string `json:"name"`
		} `json:"universe"`
	}
	var ctxs []struct {
		OI string `json:"openInterest"`
	}
	if err := json.Unmarshal(raw[0], &meta); err != nil {
		return err
	}
	if err := json.Unmarshal(raw[1], &ctxs); err != nil {
		return err
	}
	out := make(map[string]float64, len(ctxs))
	for i, c := range ctxs {
		if i < len(meta.Universe) {
			out[meta.Universe[i].Name] = f64(c.OI)
		}
	}
	s.mu.Lock()
	s.oi = out
	s.mu.Unlock()
	return nil
}

// coverageLocked 覆盖率：扫描到的持仓占全部持仓量的比例。
// 持仓量是单边的（多头总量 = 空头总量），所以扫描到的多空合计要除以 2。调用时持锁。
func (s *Scanner) coverageLocked(coin string) (cov, oi float64) {
	oi = s.oi[coin]
	if oi <= 0 {
		return 0, 0
	}
	sum := 0.0
	for _, ps := range s.pos {
		for _, p := range ps {
			if p.Coin == coin {
				sum += math.Abs(p.Size)
			}
		}
	}
	return math.Min(1, sum/2/oi), oi
}

// ---------- 钱包盈亏分布（V.18）----------

// PnLBucket 一组：浮动盈亏占开仓金额的百分比落在 [Lo, Hi) 的持仓。
type PnLBucket struct {
	Lo         float64 `json:"lo"`
	Hi         float64 `json:"hi"`
	LongValue  float64 `json:"long_value"`
	ShortValue float64 `json:"short_value"`
	LongN      int     `json:"long_n"`
	ShortN     int     `json:"short_n"`
}

// PnLDist 多空两边按盈亏分组。
type PnLDist struct {
	Coin    string      `json:"coin"`
	Buckets []PnLBucket `json:"buckets"`
	LongUp  float64     `json:"long_up"`  // 多单里浮盈的金额
	LongDn  float64     `json:"long_dn"`  // 多单里浮亏的金额
	ShortUp float64     `json:"short_up"` // 空单里浮盈的金额
	ShortDn float64     `json:"short_dn"` // 空单里浮亏的金额
}

var pnlEdges = []float64{math.Inf(-1), -20, -10, -5, 0, 5, 10, 20, math.Inf(1)}

// PnLDistOf 算一个币的分布。
func (s *Scanner) PnLDistOf(coin string) PnLDist {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d := PnLDist{Coin: coin}
	for i := 0; i+1 < len(pnlEdges); i++ {
		d.Buckets = append(d.Buckets, PnLBucket{Lo: pnlEdges[i], Hi: pnlEdges[i+1]})
	}
	for _, ps := range s.pos {
		for _, p := range ps {
			if p.Coin != coin || p.Size == 0 {
				continue
			}
			cost := math.Abs(p.Size) * p.Entry
			if cost <= 0 {
				continue
			}
			pct := p.UPnL / cost * 100
			for i := range d.Buckets {
				b := &d.Buckets[i]
				if pct >= b.Lo && pct < b.Hi {
					if p.Size > 0 {
						b.LongValue += p.Value
						b.LongN++
					} else {
						b.ShortValue += p.Value
						b.ShortN++
					}
					break
				}
			}
			switch {
			case p.Size > 0 && p.UPnL >= 0:
				d.LongUp += p.Value
			case p.Size > 0:
				d.LongDn += p.Value
			case p.UPnL >= 0:
				d.ShortUp += p.Value
			default:
				d.ShortDn += p.Value
			}
		}
	}
	// JSON 不能写无穷大：两头用极大值代替，界面写"以上""以下"
	d.Buckets[0].Lo = -1e9
	d.Buckets[len(d.Buckets)-1].Hi = 1e9
	return d
}

// ---------- 存盘（V.20）----------

type snapshot struct {
	Accounts map[string]Account `json:"accounts"`
	Pos      map[string][]Pos   `json:"pos"`
	Scanned  map[string]int64   `json:"scanned"`
	Events   []Event            `json:"events"`
	OI       map[string]float64 `json:"oi"`
	Cycle    int                `json:"cycle"`
	CycleEnd int64              `json:"cycle_end"`
}

// Snapshot 把现在的扫描结果压缩成一块。
func (s *Scanner) Snapshot() ([]byte, error) {
	s.mu.RLock()
	v := snapshot{Accounts: s.accounts, Pos: s.pos, Scanned: s.scanned, Events: s.events, OI: s.oi, Cycle: s.cycle, CycleEnd: s.cycleEnd}
	raw, err := json.Marshal(v)
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(raw)
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Restore 从存盘恢复：只补还没扫描到的地址，已经有更新数据的不覆盖。
func (s *Scanner) Restore(b []byte) error {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	var v snapshot
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accounts == nil {
		s.accounts, s.pos, s.scanned = map[string]Account{}, map[string][]Pos{}, map[string]int64{}
	}
	for a, x := range v.Accounts {
		if _, ok := s.accounts[a]; !ok {
			s.accounts[a] = x
		}
	}
	for a, t := range v.Scanned {
		if cur, ok := s.scanned[a]; !ok || t > cur {
			s.scanned[a] = t
			s.pos[a] = v.Pos[a]
		}
	}
	s.events = append(append([]Event(nil), v.Events...), s.events...)
	if s.oi == nil {
		s.oi = v.OI
	}
	s.cycle = max(s.cycle, v.Cycle)
	s.cycleEnd = max(s.cycleEnd, v.CycleEnd)
	return nil
}

const storeSchema = `
create table if not exists onchain_snapshot (
  id   int primary key,
  t    bigint not null,
  body bytea  not null
);
`

// Persist 数据库连上后调用：先恢复上次的结果，之后每 5 分钟存一次。
func (s *Scanner) Persist(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, storeSchema); err != nil {
		return err
	}
	var body []byte
	var t int64
	if err := pool.QueryRow(ctx, `select t, body from onchain_snapshot where id=1`).Scan(&t, &body); err == nil {
		if err := s.Restore(body); err != nil && s.Log != nil {
			s.Log.Warn("恢复链上扫描结果失败", "err", err)
		} else if s.Log != nil {
			s.Log.Info("已恢复链上扫描结果", "saved_at", time.UnixMilli(t).Format(time.RFC3339))
		}
	}
	s.pool = pool
	go func() {
		tick := time.NewTicker(5 * time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				c, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := s.Save(c); err != nil && s.Log != nil {
					s.Log.Warn("存链上扫描结果失败", "err", err)
				}
				cancel()
			}
		}
	}()
	return nil
}

// Save 存一次。关机前也调一次。
func (s *Scanner) Save(ctx context.Context) error {
	if s.pool == nil {
		return nil
	}
	b, err := s.Snapshot()
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `insert into onchain_snapshot (id,t,body) values (1,$1,$2) on conflict (id) do update set t=excluded.t, body=excluded.body`, time.Now().UnixMilli(), b)
	return err
}

// ---------- 模拟（只有测试环境用）----------

// RunSim 不连 Hyperliquid：造一批地址和持仓，走和真实一样的 update，所以动作流、盈亏分布都有数据。
func (s *Scanner) RunSim(ctx context.Context, coins []string, price func(coin string) float64) {
	s.defaults()
	rnd := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 11))
	addr := func(i int) string { return fmt.Sprintf("0x%040x", i+1) }
	const n = 240
	s.mu.Lock()
	for i := 0; i < n; i++ {
		s.accounts[addr(i)] = Account{Address: addr(i), Value: 1e6 * rnd.ExpFloat64(), AllTimePnL: 1e6 * (rnd.Float64() - 0.4) * 8, MonthPnL: 1e5 * (rnd.Float64() - 0.5) * 8, RankByValue: i + 1}
	}
	s.mu.Unlock()
	mk := func(coin string, mark float64) Pos {
		long := rnd.IntN(2) == 0
		value := 30_000 + rnd.ExpFloat64()*900_000
		entry := mark * (1 + (rnd.Float64()-0.5)*0.3)
		size := value / mark
		upnl := (mark - entry) * size
		liq := entry * (1 - 0.04 - rnd.Float64()*0.22)
		if !long {
			size, upnl = -size, -upnl
			liq = entry * (1 + 0.04 + rnd.Float64()*0.22)
		}
		return Pos{Coin: coin, Size: size, Entry: entry, Value: value, UPnL: upnl, LiqPx: liq, Lev: 5 + rnd.IntN(20), Cross: true}
	}
	state := map[string][]Pos{}
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	first, i := true, 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		a := addr(i)
		ps := state[a]
		next := append([]Pos(nil), ps...)
		c := coins[rnd.IntN(len(coins))]
		if mark := price(c); mark > 0 {
			found := -1
			for j, p := range next {
				if p.Coin == c {
					found = j
				}
			}
			switch {
			case found < 0:
				next = append(next, mk(c, mark))
			case rnd.IntN(3) == 0:
				next = append(next[:found], next[found+1:]...)
			default:
				p := next[found]
				p.Size *= 0.6 + rnd.Float64()*0.8
				p.Value = math.Abs(p.Size) * mark
				p.UPnL = (mark - p.Entry) * p.Size
				next[found] = p
			}
		}
		state[a] = next
		s.update(a, next)
		i++
		if i >= n {
			i = 0
			s.mu.Lock()
			s.cycle++
			s.cycleEnd = time.Now().UnixMilli()
			if first {
				first = false
				s.oi = map[string]float64{}
				for _, ps := range s.pos {
					for _, p := range ps {
						s.oi[p.Coin] += math.Abs(p.Size) * 0.8 // 让覆盖率大约是 60%
					}
				}
			}
			s.mu.Unlock()
		}
	}
}
