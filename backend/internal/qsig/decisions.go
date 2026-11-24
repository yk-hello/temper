package qsig

import (
	"encoding/json"
	"sort"

	"github.com/shopspring/decimal"

	"qs/internal/portfolio"
	"qs/internal/trading"
)

// DemoStrategyID 平台内置的示例策略：信号是随机的，界面上要写明"示例"。
const DemoStrategyID = "demo-random"

// 给界面看的决策、复盘、信心校准（3.23–3.25）。这些都只给开启服务的人（D75 第 18 条），
// 权限在接口层判断。

// Decision 一条决策：信号原文（含理由、证据、信心）加执行结果。心跳不算决策（第 18 条）。
type Decision struct {
	ID         string                `json:"id"`
	StrategyID string                `json:"strategy_id"`
	Type       string                `json:"type"`
	Status     string                `json:"status"`
	Code       string                `json:"code,omitempty"`
	ReceivedAt int64                 `json:"received_at"`
	Message    json.RawMessage       `json:"message"`
	Executions []portfolio.Execution `json:"executions"`
}

// Decisions 某个策略最近 n 条决策，新的在前。搬引擎之前的旧信号不算（战绩从零开始）。
func (s *Service) Decisions(strategy string, n int) []Decision {
	pf := s.Portfolio(strategy)
	since := pf.Since()
	s.mu.Lock()
	var recs []Record
	for i := len(s.order) - 1; i >= 0 && len(recs) < n; i-- {
		r := s.recs[s.order[i]]
		if r == nil {
			continue
		}
		// 被拒的消息是发信号那边的格式或规则问题，用户看不懂也用不上（平台方在日志和 /v1 里看）
		if r.StrategyID != strategy || r.Type == "heartbeat" || r.Status != "accepted" || r.ReceivedAt < since {
			continue
		}
		recs = append(recs, *r)
	}
	s.mu.Unlock()
	out := make([]Decision, 0, len(recs))
	for _, r := range recs {
		ex := pf.Executions(r.ID)
		if ex == nil {
			ex = []portfolio.Execution{}
		}
		out = append(out, Decision{ID: r.ID, StrategyID: r.StrategyID, Type: r.Type, Status: r.Status, Code: r.Code, ReceivedAt: r.ReceivedAt, Message: r.Raw, Executions: ex})
	}
	return out
}

func (s *Service) messageOf(id string) *Message {
	s.mu.Lock()
	r := s.recs[id]
	s.mu.Unlock()
	if r == nil {
		return nil
	}
	var m Message
	if json.Unmarshal(r.Raw, &m) != nil {
		return nil
	}
	return &m
}

func targetOf(m *Message, inst string) *TargetMsg {
	if m == nil {
		return nil
	}
	for i := range m.Targets {
		if m.Targets[i].Instrument == inst {
			return &m.Targets[i]
		}
	}
	return nil
}

// Leg 一笔成交，带上当时的决策价和滑点。
type Leg struct {
	Time     int64           `json:"time"`
	Side     string          `json:"side"`
	Qty      decimal.Decimal `json:"qty"`
	Px       decimal.Decimal `json:"px"`
	Fee      decimal.Decimal `json:"fee"`
	Maker    bool            `json:"maker"`
	Realized decimal.Decimal `json:"realized"`
	Signal   string          `json:"signal,omitempty"`
	RefPx    decimal.Decimal `json:"ref_px"`              // 决策价（ref_price）；风控平仓没有
	SlipBps  decimal.Decimal `json:"slip_bps"`            // 成交价相对决策价，对我们不利为正
	ClosedBy string          `json:"closed_by,omitempty"` // 风控平仓的原因
}

// TradeDetail 一笔交易摊开看（3.24）：证据、决策价、成交价、滑点、手续费、资金费，一路算到净收益。
type TradeDetail struct {
	portfolio.Trade
	Legs      []Leg                `json:"legs"`
	Fundings  []trading.FundingRec `json:"fundings"`
	Decisions []TradeDecision      `json:"decisions"`
	OpenConf  *float64             `json:"confidence"`
}

// TradeDecision 这笔交易用到的决策。
type TradeDecision struct {
	ID         string          `json:"id"`
	Weight     string          `json:"weight"`
	Confidence float64         `json:"confidence"`
	Rationale  json.RawMessage `json:"rationale"`
}

// Trades 某个策略所有交易的明细，最新的在前；还没平完的也在里面（close_time 为 0）。
func (s *Service) Trades(strategy string) []TradeDetail {
	closed, open := s.Portfolio(strategy).AllTrades()
	all := append(closed, open...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].OpenTime > all[j].OpenTime })
	out := make([]TradeDetail, 0, len(all))
	for _, t := range all {
		out = append(out, s.detail(t))
	}
	return out
}

func (s *Service) detail(t portfolio.Trade) TradeDetail {
	d := TradeDetail{Trade: t, Legs: []Leg{}, Fundings: t.FundingRecs, Decisions: []TradeDecision{}}
	if d.Fundings == nil {
		d.Fundings = []trading.FundingRec{}
	}
	for _, id := range t.Signals {
		if tg := targetOf(s.messageOf(id), t.Instrument); tg != nil {
			d.Decisions = append(d.Decisions, TradeDecision{ID: id, Weight: tg.Weight, Confidence: tg.Confidence, Rationale: tg.Rationale})
			if d.OpenConf == nil {
				c := tg.Confidence
				d.OpenConf = &c
			}
		}
	}
	for _, f := range t.Legs {
		l := Leg{Time: f.Time, Side: f.Side, Qty: f.Qty, Px: f.Px, Fee: f.Fee, Maker: f.Maker, Realized: f.Realized}
		if f.Source == "risk" {
			l.ClosedBy = f.Tag
		} else {
			l.Signal = f.Tag
			if tg := targetOf(s.messageOf(f.Tag), t.Instrument); tg != nil {
				if ref, err := decimal.NewFromString(tg.RefPrice); err == nil && ref.Sign() > 0 {
					l.RefPx = ref
					sl := f.Px.Sub(ref).Div(ref).Mul(decimal.NewFromInt(10_000))
					if f.Side == "sell" {
						sl = sl.Neg()
					}
					l.SlipBps = sl.Round(2)
				}
			}
		}
		d.Legs = append(d.Legs, l)
	}
	return d
}

// CalibrationMin 每档不到这么多笔，就只写"样本不够"，不给胜率（D75 第 5 条）。
const CalibrationMin = 30

// Bucket 信心校准的一档：策略说的信心，和实际胜率对不对得上。
type Bucket struct {
	Lo      float64  `json:"lo"`
	Hi      float64  `json:"hi"`
	Trades  int      `json:"trades"`
	Wins    int      `json:"wins"`
	Enough  bool     `json:"enough"`
	WinRate *float64 `json:"win_rate"` // 样本不够时为空
	AvgNet  *float64 `json:"avg_net"`  // 每笔平均净收益，样本不够时为空
}

var calibrationEdges = []float64{0, 0.5, 0.6, 0.7, 0.8, 1}

// Calibration 已平仓的交易按开仓时的信心分档。
func (s *Service) Calibration(strategy string) []Bucket {
	closed, _ := s.Portfolio(strategy).AllTrades()
	conf := make([]*float64, len(closed))
	for i, t := range closed {
		conf[i] = s.detail(t).OpenConf
	}
	return CalibrationOf(closed, conf)
}

// CalibrationOf 算法单独拿出来，手算样例在测试里。conf 和 closed 一一对应，空的（比如找不到原始信号）不计入。
func CalibrationOf(closed []portfolio.Trade, conf []*float64) []Bucket {
	out := make([]Bucket, len(calibrationEdges)-1)
	sum := make([]float64, len(out))
	for i := range out {
		out[i].Lo, out[i].Hi = calibrationEdges[i], calibrationEdges[i+1]
	}
	for i, t := range closed {
		if conf[i] == nil {
			continue
		}
		for j := range out {
			if *conf[i] < out[j].Hi || j == len(out)-1 {
				out[j].Trades++
				if t.Net.Sign() > 0 {
					out[j].Wins++
				}
				f, _ := t.Net.Float64()
				sum[j] += f
				break
			}
		}
	}
	for j := range out {
		if out[j].Trades >= CalibrationMin {
			r := float64(out[j].Wins) / float64(out[j].Trades)
			a := sum[j] / float64(out[j].Trades)
			out[j].Enough, out[j].WinRate, out[j].AvgNet = true, &r, &a
		}
	}
	return out
}
