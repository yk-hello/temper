package qsig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/book"
	"qs/internal/portfolio"
	"qs/internal/trading"
)

const venue = "binance-usdm"

type env struct {
	t     *testing.T
	srv   *httptest.Server
	svc   *Service
	books *book.Store
	eng   *trading.Engine
	pfs   map[string]*portfolio.Portfolio
	acct  map[string]int64
	now   time.Time
	evs   []trading.Event
}

func (e *env) pf(id string) *portfolio.Portfolio { return e.svc.Portfolio(id) }

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func newEnv(t *testing.T) *env {
	e := &env{t: t, books: book.NewStore(), pfs: map[string]*portfolio.Portfolio{}, acct: map[string]int64{}, now: time.Now()}
	e.eng = trading.New(e.books, venue, trading.NewSpecs(trading.DefaultSpecs()), quiet())
	e.eng.SetPersist(func(b []trading.Event) error {
		e.evs = append(e.evs, b...)
		return nil
	})
	setBook(e.books, "BTC-PERP", "77000")
	setBook(e.books, "ETH-PERP", "2900")
	setBook(e.books, "SOL-PERP", "180")
	pf := func(id string) *portfolio.Portfolio {
		if p, ok := e.pfs[id]; ok {
			return p
		}
		e.acct[id] = int64(-1 - len(e.acct))
		p := portfolio.New(id, e.acct[id], venue, venue, e.books, e.eng)
		e.pfs[id] = p
		return p
	}
	var ins []Instrument
	for _, s := range []string{"BTC-PERP", "ETH-PERP", "SOL-PERP"} {
		ins = append(ins, Instrument{Instrument: s, Tradable: true})
	}
	svc, err := NewService([]Key{{ID: "k1", Secret: "s3cret", StrategyID: "news-alpha"}, {ID: "k2", Secret: "other", StrategyID: "other"}},
		ins, e.books, venue, pf, quiet())
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	e.srv = httptest.NewServer(svc.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

// setBook：以 px 为中间价，上下各 10 档，每档 1 个单位，档距 0.01%
func setBook(s *book.Store, inst, mid string) {
	m := d(mid)
	step := m.Mul(d("0.0001"))
	var bids, asks []book.Level
	for i := 1; i <= 10; i++ {
		k := decimal.NewFromInt(int64(i))
		bids = append(bids, book.Level{Px: m.Sub(step.Mul(k)), Qty: d("1")})
		asks = append(asks, book.Level{Px: m.Add(step.Mul(k)), Qty: d("1")})
	}
	s.SetBook(book.Book{Venue: venue, Instrument: inst, Bids: bids, Asks: asks, Time: time.Now().UnixMilli()})
	s.UpdateTicker(venue, inst, func(t *book.Ticker) { t.Last, t.Mark = m, m })
}

func ts(t time.Time) string { return t.UTC().Format(TimeLayout) }

var idn = 0

func newID() string {
	idn++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", idn)
}

func signal(id string, issued time.Time, targets ...map[string]any) map[string]any {
	return map[string]any{
		"protocol": "qsig/1", "type": "signal", "id": id, "strategy_id": "news-alpha", "strategy_version": "t1",
		"issued_at": ts(issued), "data_as_of": ts(issued.Add(-30 * time.Second)), "valid_until": ts(issued.Add(60 * time.Second)),
		"mode": "partial", "targets": targets,
	}
}

func target(inst, w, ref string) map[string]any {
	return map[string]any{"instrument": inst, "weight": w, "confidence": 0.7, "horizon_min": 60, "ref_price": ref,
		"ref_price_source": "binance_usdm_last", "execution": map[string]any{"urgency": "immediate", "max_slippage_bps": 20},
		"rationale": map[string]any{"summary": map[string]any{"zh": "测试"}}}
}

func (e *env) do(method, path, key, secret string, body []byte, tsOff time.Duration) (int, map[string]any) {
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(body))
	t := time.Now().Add(tsOff).UnixMilli()
	req.Header.Set("X-QS-Key", key)
	req.Header.Set("X-QS-Timestamp", fmt.Sprint(t))
	req.Header.Set("X-QS-Signature", Sign(secret, t, method, path, body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (e *env) send(msg map[string]any) (int, map[string]any) {
	b, _ := json.Marshal(msg)
	return e.do("POST", "/v1/messages", "k1", "s3cret", b, 0)
}

func waitExec(t *testing.T, e *env, id string) []any {
	for i := 0; i < 100; i++ {
		_, out := e.do("GET", "/v1/messages/"+id, "k1", "s3cret", nil, 0)
		if ex, ok := out["executions"].([]any); ok && len(ex) > 0 {
			done := true
			for _, x := range ex {
				if x.(map[string]any)["state"] == "pending" {
					done = false
				}
			}
			if done {
				return ex
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("执行回报没有完成：%s", id)
	return nil
}

func TestAcceptExecuteAndLedger(t *testing.T) {
	e := newEnv(t)
	id := newID()
	code, out := e.send(signal(id, time.Now(), target("BTC-PERP", "0.5", "77000")))
	if code != 200 || out["status"] != "accepted" {
		t.Fatalf("应接受：%d %v", code, out)
	}
	ex := waitExec(t, e, id)[0].(map[string]any)
	if ex["state"] != "filled" {
		t.Fatalf("应全部成交：%v", ex)
	}
	// 0.5 × 10000 / 77000 ≈ 0.0649 BTC，盘口第一档 1 BTC 足够，成交价 77007.7（上方一档）
	if ex["avg_price"] != "77007.7" {
		t.Fatalf("成交价不对：%v", ex["avg_price"])
	}
	if err := e.eng.Check(); err != nil {
		t.Fatal(err)
	}
	pf := e.pfs["news-alpha"]
	nav := pf.NAV()
	if !nav.LessThan(portfolio.InitialNAV) || nav.LessThan(d("9990")) {
		t.Fatalf("净值应只少了手续费和半个价差：%s", nav)
	}
	// 同 id 同内容 → duplicate；同 id 不同内容 → 409
	b, _ := json.Marshal(signal(id, time.Now(), target("BTC-PERP", "0.5", "77000")))
	var orig map[string]any
	_ = json.Unmarshal(b, &orig)
	code, out = e.send(signal(id, time.Now().Add(time.Second), target("BTC-PERP", "0.4", "77000")))
	if code != 409 || out["code"] != "ID_CONFLICT" {
		t.Fatalf("应 ID_CONFLICT：%d %v", code, out)
	}
}

func TestDuplicate(t *testing.T) {
	e := newEnv(t)
	msg := signal(newID(), time.Now(), target("ETH-PERP", "-0.3", "2900"))
	if code, _ := e.send(msg); code != 200 {
		t.Fatal(code)
	}
	code, out := e.send(msg)
	if code != 200 || out["status"] != "duplicate" {
		t.Fatalf("应 duplicate：%d %v", code, out)
	}
}

func TestRejections(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	cases := []struct {
		name   string
		msg    map[string]any
		mutate func(map[string]any)
		http   int
		code   string
	}{
		{"权重超出范围", signal(newID(), now, target("BTC-PERP", "1.5", "77000")), nil, 400, "SCHEMA_VIOLATION"},
		{"空列表", signal(newID(), now), func(m map[string]any) { m["targets"] = []any{} }, 400, "SCHEMA_VIOLATION"},
		{"协议版本", signal(newID(), now, target("BTC-PERP", "0.1", "77000")), func(m map[string]any) { m["protocol"] = "qsig/2" }, 400, "UNSUPPORTED_PROTOCOL"},
		{"策略不符", signal(newID(), now, target("BTC-PERP", "0.1", "77000")), func(m map[string]any) { m["strategy_id"] = "other" }, 403, "STRATEGY_MISMATCH"},
		{"已过期", signal(newID(), now.Add(-2*time.Minute), target("BTC-PERP", "0.1", "77000")), nil, 422, "EXPIRED"},
		{"不认识的品种", signal(newID(), now, target("ABC-PERP", "0.1", "1")), nil, 422, "UNKNOWN_INSTRUMENT"},
		{"总杠杆超 2", signal(newID(), now, target("BTC-PERP", "1", "77000"), target("ETH-PERP", "-1", "2900"), target("SOL-PERP", "0.1", "180")), nil, 422, "WEIGHT_LIMIT_EXCEEDED"},
		{"有效期太长", signal(newID(), now, target("BTC-PERP", "0.1", "77000")), func(m map[string]any) { m["valid_until"] = ts(now.Add(10 * time.Minute)) }, 400, "SCHEMA_VIOLATION"},
	}
	for _, c := range cases {
		if c.mutate != nil {
			c.mutate(c.msg)
		}
		code, out := e.send(c.msg)
		if code != c.http || out["code"] != c.code {
			t.Errorf("%s：期望 %d %s，得到 %d %v", c.name, c.http, c.code, code, out)
		}
	}
	// 签名错误、时钟偏差
	b, _ := json.Marshal(signal(newID(), now, target("BTC-PERP", "0.1", "77000")))
	if code, out := e.do("POST", "/v1/messages", "k1", "wrong", b, 0); code != 401 || out["code"] != "AUTH_FAILED" {
		t.Errorf("签名错误应 401：%d %v", code, out)
	}
	if code, out := e.do("POST", "/v1/messages", "k1", "s3cret", b, -time.Minute); code != 401 || out["code"] != "TIMESTAMP_SKEW" {
		t.Errorf("时钟偏差应 401：%d %v", code, out)
	}
	// 出错位置
	_, out := e.send(signal(newID(), now, target("BTC-PERP", "1.5", "77000")))
	if det, _ := out["details"].(map[string]any); det["path"] != "/targets/0/weight" {
		t.Errorf("出错位置应是 /targets/0/weight：%v", out)
	}
}

func TestStaleAndPriceMoved(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	id := newID()
	if code, out := e.send(signal(id, now, target("BTC-PERP", "0.2", "77000"))); code != 200 {
		t.Fatal(code, out)
	}
	waitExec(t, e, id)
	if code, out := e.send(signal(newID(), now.Add(-5*time.Second), target("BTC-PERP", "0.3", "77000"))); code != 422 || out["code"] != "STALE" {
		t.Fatalf("更早的信号应 STALE：%d %v", code, out)
	}
	// ref_price 偏离当前价太多 → 不执行（并给出警告）
	id2 := newID()
	code, out := e.send(signal(id2, now.Add(time.Second), target("BTC-PERP", "0.5", "76000")))
	if code != 200 || !strings.Contains(fmt.Sprint(out["warnings"]), "REF_PRICE_DEVIATION") {
		t.Fatalf("应接受并警告：%d %v", code, out)
	}
	if st := waitExec(t, e, id2)[0].(map[string]any)["state"]; st != "skipped_price_moved" {
		t.Fatalf("应 skipped_price_moved：%v", st)
	}
	// 和当前目标相同 → 维持现状
	id3 := newID()
	e.send(signal(id3, now.Add(2*time.Second), target("BTC-PERP", "0.2", "77000")))
	if st := waitExec(t, e, id3)[0].(map[string]any)["state"]; st != "skipped_no_change" {
		t.Fatalf("应 skipped_no_change：%v", st)
	}
}

func TestHaltPauseOnlyAllowsReduce(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	id := newID()
	e.send(signal(id, now, target("BTC-PERP", "0.4", "77000")))
	waitExec(t, e, id)
	halt := map[string]any{"protocol": "qsig/1", "type": "halt", "id": newID(), "strategy_id": "news-alpha", "strategy_version": "t1",
		"issued_at": ts(now), "action": "pause", "reason": map[string]any{"zh": "测试暂停"}}
	if code, out := e.send(halt); code != 200 {
		t.Fatal(code, out)
	}
	time.Sleep(20 * time.Millisecond)
	if code, out := e.send(signal(newID(), now.Add(time.Second), target("BTC-PERP", "0.8", "77000"))); code != 422 || out["code"] != "STRATEGY_PAUSED" {
		t.Fatalf("暂停时加仓应拒绝：%d %v", code, out)
	}
	if code, out := e.send(signal(newID(), now.Add(2*time.Second), target("BTC-PERP", "0.1", "77000"))); code != 200 {
		t.Fatalf("暂停时减仓应接受：%d %v", code, out)
	}
}

func TestStopLoss(t *testing.T) {
	e := newEnv(t)
	msg := signal(newID(), time.Now(), target("BTC-PERP", "0.5", "77000"))
	msg["targets"].([]map[string]any)[0]["exits"] = map[string]any{"stop_loss_pct": "0.02"}
	e.send(msg)
	waitExec(t, e, msg["id"].(string))
	setBook(e.books, "BTC-PERP", "75000") // 下跌约 2.6%
	pf := e.pfs["news-alpha"]
	pf.Watch()
	time.Sleep(20 * time.Millisecond)
	st := pf.State()
	if len(st.Positions) != 1 || st.Positions[0].Qty != "0" || st.Positions[0].ClosedBy != "stop_loss" {
		t.Fatalf("应止损平仓：%+v", st.Positions)
	}
	if err := e.eng.Check(); err != nil {
		t.Fatal(err)
	}
	if !pf.NAV().LessThan(d("9900")) {
		t.Fatalf("止损后净值应亏损约 1.3%%：%s", pf.NAV())
	}
}

// 重启恢复：用引擎的事件重放出的持仓、余额，必须和重启前一模一样
func TestRestoreFromEvents(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	for i, w := range []string{"0.5", "-0.3", "0.2"} {
		id := newID()
		e.send(signal(id, now.Add(time.Duration(i)*time.Second), target("BTC-PERP", w, "77000")))
		waitExec(t, e, id)
	}
	pf := e.pfs["news-alpha"]
	before := pf.State()
	e.eng.Flush()

	eng2 := trading.New(e.books, venue, trading.NewSpecs(trading.DefaultSpecs()), quiet())
	if err := eng2.Restore(e.evs); err != nil {
		t.Fatal(err)
	}
	if err := eng2.Check(); err != nil {
		t.Fatal(err)
	}
	p2 := portfolio.New("news-alpha", e.acct["news-alpha"], venue, venue, e.books, eng2)
	p2.Restore(nil, nil, nil)
	after := p2.State()
	if before.NAV != after.NAV || before.Cash != after.Cash || len(after.Positions) != 1 ||
		before.Positions[0].Qty != after.Positions[0].Qty || before.Positions[0].EntryPrice != after.Positions[0].EntryPrice {
		t.Fatalf("恢复后不一致：\n前 %+v\n后 %+v", before, after)
	}
}

// 3.2：超过 180 秒没心跳，持仓保持、止损照常，状态写离线
func TestOfflineKeepsPositionsAndStops(t *testing.T) {
	e := newEnv(t)
	hb := map[string]any{"protocol": "qsig/1", "type": "heartbeat", "id": newID(), "strategy_id": "news-alpha", "strategy_version": "t1",
		"issued_at": ts(time.Now()), "status": "ok"}
	if code, out := e.send(hb); code != 200 {
		t.Fatal(code, out)
	}
	msg := signal(newID(), time.Now(), target("BTC-PERP", "0.5", "77000"))
	msg["targets"].([]map[string]any)[0]["exits"] = map[string]any{"stop_loss_pct": "0.02"}
	e.send(msg)
	waitExec(t, e, msg["id"].(string))
	online := func() bool {
		for _, s := range e.svc.Status() {
			if s.StrategyID == "news-alpha" {
				return s.Online
			}
		}
		t.Fatal("没有这个策略")
		return false
	}
	if !online() {
		t.Fatal("刚发过心跳应在线")
	}
	real := e.svc.Now
	e.svc.Now = func() time.Time { return real().Add(181 * time.Second) }
	if online() {
		t.Fatal("超过 180 秒没心跳应离线")
	}
	pf := e.pfs["news-alpha"]
	pf.Watch()
	st := pf.State()
	if len(st.Positions) != 1 || st.Positions[0].Qty == "0" {
		t.Fatalf("离线时持仓应保持：%+v", st.Positions)
	}
	setBook(e.books, "BTC-PERP", "75000")
	pf.Watch()
	if st := pf.State(); st.Positions[0].Qty != "0" || st.Positions[0].ClosedBy != "stop_loss" {
		t.Fatalf("离线时止损照常：%+v", st.Positions)
	}
}

// 第 18 条：心跳不占决策的名额
func TestHeartbeatsDontCrowdDecisions(t *testing.T) {
	e := newEnv(t)
	pf := e.pf("news-alpha")
	pf.Watch()
	now := time.Now()
	id := newID()
	e.send(signal(id, now, target("BTC-PERP", "0.5", "77000")))
	waitExec(t, e, id)
	for i := 0; i < 5; i++ {
		e.send(map[string]any{"protocol": "qsig/1", "type": "heartbeat", "id": newID(), "strategy_id": "news-alpha", "strategy_version": "t1", "issued_at": ts(now), "status": "ok"})
	}
	if got := e.svc.Recent(1); len(got) != 1 || got[0].ID != id {
		t.Fatalf("Recent：%+v", got)
	}
	ds := e.svc.Decisions("news-alpha", 10)
	if len(ds) != 1 || ds[0].ID != id || len(ds[0].Executions) != 1 || ds[0].Executions[0].State != "filled" {
		t.Fatalf("决策：%+v", ds)
	}
	if !strings.Contains(string(ds[0].Message), "测试") {
		t.Fatalf("决策里应有理由：%s", ds[0].Message)
	}
	if got := e.svc.Decisions("other", 10); len(got) != 0 {
		t.Fatalf("别的策略不该混进来：%+v", got)
	}
}

// 3.24：每笔交易摊开，一路算到净收益，和账本对得上
func TestTradeDetailMatchesLedger(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	id1, id2 := newID(), newID()
	e.send(signal(id1, now, target("BTC-PERP", "0.5", "77000")))
	waitExec(t, e, id1)
	setBook(e.books, "BTC-PERP", "78000")
	e.send(signal(id2, now.Add(time.Second), target("BTC-PERP", "0", "78000")))
	waitExec(t, e, id2)
	list := e.svc.Trades("news-alpha")
	if len(list) != 1 || list[0].CloseTime == 0 {
		t.Fatalf("交易：%+v", list)
	}
	tr := list[0]
	if len(tr.Legs) != 2 || len(tr.Decisions) != 2 || tr.OpenConf == nil || *tr.OpenConf != 0.7 {
		t.Fatalf("明细：%+v", tr)
	}
	// 决策价 77000，买在 77007.7：滑点 +1 bp；决策价 78000，卖在 77992.2：滑点 +1 bp
	if !tr.Legs[0].RefPx.Equal(d("77000")) || !tr.Legs[0].SlipBps.Equal(d("1")) || !tr.Legs[1].SlipBps.Equal(d("1")) {
		t.Fatalf("腿：%+v", tr.Legs)
	}
	sum := decimal.Zero
	for _, l := range tr.Legs {
		sum = sum.Add(l.Realized).Sub(l.Fee)
	}
	if !sum.Equal(tr.Net) {
		t.Fatalf("逐笔加起来 %s ≠ 净收益 %s", sum, tr.Net)
	}
	v, _ := e.eng.View(e.acct["news-alpha"])
	if !tr.Net.Equal(v.Wallet.Sub(v.Initial)) {
		t.Fatalf("净收益 %s ≠ 钱包变化 %s", tr.Net, v.Wallet.Sub(v.Initial))
	}
}

// 3.25 手算样例：信心 0.75 的 30 笔，18 笔赚 2、12 笔亏 1 → 胜率 60%，平均 (36−12)/30 = 0.8；
// 信心 0.55 的只有 29 笔 → 样本不够，不给胜率。边界：0.5 算 0.5–0.6 这一档，1.0 算最高档，找不到信心的不计入。
func TestCalibrationByHand(t *testing.T) {
	var trades []portfolio.Trade
	var conf []*float64
	add := func(c float64, net string, n int) {
		for i := 0; i < n; i++ {
			x := c
			trades = append(trades, portfolio.Trade{Net: d(net)})
			conf = append(conf, &x)
		}
	}
	add(0.75, "2", 18)
	add(0.75, "-1", 12)
	add(0.55, "1", 29)
	add(0.5, "1", 1)
	add(1.0, "1", 2)
	trades = append(trades, portfolio.Trade{Net: d("9")})
	conf = append(conf, nil)
	b := CalibrationOf(trades, conf)
	if len(b) != 5 {
		t.Fatalf("%+v", b)
	}
	if x := b[3]; x.Trades != 30 || x.Wins != 18 || !x.Enough || *x.WinRate != 0.6 || *x.AvgNet != 0.8 {
		t.Fatalf("0.7–0.8：%+v", x)
	}
	if x := b[1]; x.Trades != 30 || !x.Enough {
		t.Fatalf("0.5–0.6 应含 29+1 笔：%+v", x)
	}
	if x := b[4]; x.Trades != 2 || x.Enough || x.WinRate != nil || x.AvgNet != nil {
		t.Fatalf("样本不够时不给胜率：%+v", x)
	}
	if x := b[0]; x.Trades != 0 || x.Enough {
		t.Fatalf("%+v", x)
	}
}

// 第 7 条：被拒的消息重发，状态码和第一次一样
func TestRejectedResendSameStatus(t *testing.T) {
	e := newEnv(t)
	msg := signal(newID(), time.Now().Add(-time.Hour), target("BTC-PERP", "0.1", "100"))
	c1, b1 := e.send(msg)
	c2, b2 := e.send(msg)
	if c1 < 400 || c1 != c2 || b1["code"] != b2["code"] {
		t.Fatalf("第一次 %d %v，重发 %d %v", c1, b1["code"], c2, b2["code"])
	}
}

// 第 8 条：写库失败后，列表接口不能一直报错，同一个 id 还能重发
func TestPersistFailureLeavesNothing(t *testing.T) {
	e := newEnv(t)
	fail := true
	old := e.svc.Persist
	e.svc.Persist = func(st Stored) error {
		if fail {
			return errors.New("写不进去")
		}
		if old != nil {
			return old(st)
		}
		return nil
	}
	msg := signal(newID(), time.Now(), target("BTC-PERP", "0.1", "100"))
	if c, _ := e.send(msg); c != 500 {
		t.Fatalf("写库失败应返回 500：%d", c)
	}
	e.svc.mu.Lock()
	left := len(e.svc.order)
	e.svc.mu.Unlock()
	if left != 0 {
		t.Fatalf("不该留下半条：%d", left)
	}
	fail = false
	if c, b := e.send(msg); c != 200 {
		t.Fatalf("恢复后同一个 id 要能发：%d %v", c, b)
	}
}

// 第 11 条：信号时间写到未来要拒绝，不能把后面正常的信号都挡住
func TestFutureIssuedRejected(t *testing.T) {
	e := newEnv(t)
	future := time.Now().Add(2 * time.Minute)
	m := signal(newID(), future, target("BTC-PERP", "0.1", "100"))
	if c, b := e.send(m); c != 400 || b["code"] != "SCHEMA_VIOLATION" {
		t.Fatalf("未来的时间要拒绝：%d %v", c, b)
	}
	if c, b := e.send(signal(newID(), time.Now(), target("BTC-PERP", "0.1", "100"))); c != 200 {
		t.Fatalf("后面正常的信号不受影响：%d %v", c, b)
	}
}

// 前向记录（D96）：每条消息带上一环的指纹；写库失败的不进链；改了中间一条后面全对不上
func TestForwardChain(t *testing.T) {
	e := newEnv(t)
	var got []Stored
	fail := false
	e.svc.Persist = func(st Stored) error {
		if fail {
			return errors.New("写不进去")
		}
		got = append(got, st)
		return nil
	}
	m1 := signal(newID(), time.Now(), target("BTC-PERP", "0.1", "100"))
	if c, b := e.send(m1); c != 200 {
		t.Fatalf("第一条：%d %v", c, b)
	}
	fail = true
	if c, _ := e.send(signal(newID(), time.Now(), target("BTC-PERP", "0.2", "100"))); c != 500 {
		t.Fatal("写库失败应返回 500")
	}
	fail = false
	m3 := signal(newID(), time.Now(), target("BTC-PERP", "0.15", "100"))
	if c, b := e.send(m3); c != 200 {
		t.Fatalf("第三条：%d %v", c, b)
	}
	if len(got) != 2 {
		t.Fatalf("写进去 %d 条", len(got))
	}
	if got[0].Prev != "" || got[1].Prev != got[0].Chain {
		t.Fatalf("链没接上：%q → %q / %q", got[0].Chain, got[1].Prev, got[1].Chain)
	}
	for i, s := range got {
		if ChainHash(s.Prev, s.ID, s.ReceivedAt.UnixMilli(), s.Body) != s.Chain {
			t.Fatalf("第 %d 环算不回来", i)
		}
	}
	// 改了第一条的内容：重算的第一环变了，第二条记的"上一环"就对不上
	tampered := append([]byte(nil), got[0].Body...)
	tampered[len(tampered)-2] ^= 1
	if ChainHash(got[0].Prev, got[0].ID, got[0].ReceivedAt.UnixMilli(), tampered) == got[1].Prev {
		t.Fatal("改了内容链还对得上")
	}
	// 重启接上最后一环
	e.svc.SetChains(map[string]string{"s1": "abc"})
	e.svc.cmu.Lock()
	ok := e.svc.chains["s1"] == "abc"
	e.svc.cmu.Unlock()
	if !ok {
		t.Fatal("SetChains 没接上")
	}
}

// 刚装好、一个策略都没有：列表要是 []，不是 null（null 时前端的策略页和跟随向导一直显示"加载中"，9.7 从零走一遍时发现）
func TestStatusEmptyIsList(t *testing.T) {
	svc, err := NewService(nil, nil, book.NewStore(), "binance-usdm", func(string) *portfolio.Portfolio { return nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(svc.Status())
	if string(b) != "[]" {
		t.Fatalf("没有策略时应该是 []：%s", b)
	}
}

// 11-16 走查：只发信号、不发心跳的程序（示例脚本就是这样），收到信号那一刻也算活着，180 秒内不写"策略离线"
func TestSignalCountsAsHeartbeat(t *testing.T) {
	e := newEnv(t)
	msg := signal(newID(), time.Now(), target("BTC-PERP", "0.2", "77000"))
	if code, out := e.send(msg); code != 200 {
		t.Fatal(code, out)
	}
	on := func() (bool, string) {
		for _, s := range e.svc.Status() {
			if s.StrategyID == "news-alpha" {
				return s.Online, s.Heartbeat
			}
		}
		t.Fatal("没有这个策略")
		return false, ""
	}
	if ok, hb := on(); !ok || hb != "ok" {
		t.Fatalf("刚收到信号应在线：%v %q", ok, hb)
	}
	real := e.svc.Now
	e.svc.Now = func() time.Time { return real().Add(181 * time.Second) }
	if ok, _ := on(); ok {
		t.Fatal("之后 180 秒既没信号也没心跳，照样算离线")
	}
}

// 重启后最后心跳从记录里接上：不再显示"从未 / 离线"（11-17 走查）
func TestHeartbeatSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	hb, _ := json.Marshal(map[string]string{"status": "degraded", "note": "data lag"})
	e.svc.Load([]Stored{
		{ID: "a", StrategyID: "news-alpha", Type: "heartbeat", Status: "accepted", ReceivedAt: now.Add(-90 * time.Second), Body: hb},
		{ID: "b", StrategyID: "news-alpha", Type: "signal", Status: "rejected", ReceivedAt: now.Add(-10 * time.Second), Body: []byte(`{}`)},
		{ID: "c", StrategyID: "news-alpha", Type: "heartbeat", Status: "accepted", ReceivedAt: now.Add(-3 * time.Hour), Body: []byte(`{"status":"ok"}`)},
	})
	for _, s := range e.svc.Status() {
		if s.StrategyID != "news-alpha" {
			continue
		}
		if !s.Online || s.Heartbeat != "degraded" || s.LastHeartbeat != now.Add(-90*time.Second).UnixMilli() {
			t.Fatalf("重启后的心跳不对：在线 %v 状态 %q 时间 %d", s.Online, s.Heartbeat, s.LastHeartbeat)
		}
		return
	}
	t.Fatal("没有这个策略")
}
