package news

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const rssBody = `<?xml version="1.0"?>
<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/"><channel><title>Coin Daily</title>
<item><title><![CDATA[Bitcoin tops <b>$100k</b> as ETFs see inflows]]></title><link>https://example.com/a</link><pubDate>Tue, 10 Nov 2026 08:00:00 +0000</pubDate></item>
<item><title>SOL &amp; ETH lead gains; new solution for wallets</title><link>/b</link><dc:date>2026-11-10T09:00:00Z</dc:date></item>
<item><title>Bad link</title><link>javascript:alert(1)</link></item>
<item><title></title><link>https://example.com/empty</link></item>
</channel></rss>`

const atomBody = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>Exchange Notices</title>
<entry><title>Will list XYZ perpetual</title><link rel="alternate" href="https://example.org/n/1"/><id>1</id><updated>2026-11-10T10:00:00Z</updated></entry>
</feed>`

func TestParseFeeds(t *testing.T) {
	items, err := parseFeed([]byte(rssBody), "https://example.com/rss")
	if err != nil || len(items) != 2 {
		t.Fatalf("RSS：%v %+v", err, items)
	}
	if items[0].Title != "Bitcoin tops $100k as ETFs see inflows" || items[0].Source != "Coin Daily" || items[0].Time != time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("第一条：%+v", items[0])
	}
	if items[1].URL != "https://example.com/b" || items[1].Title != "SOL & ETH lead gains; new solution for wallets" || items[1].Time == 0 {
		t.Fatalf("相对链接、实体、dc:date：%+v", items[1])
	}
	a, err := parseFeed([]byte(atomBody), "https://example.org/atom")
	if err != nil || len(a) != 1 || a[0].URL != "https://example.org/n/1" || a[0].Source != "Exchange Notices" {
		t.Fatalf("Atom：%v %+v", err, a)
	}
	if _, err := parseFeed([]byte("<html>nope</html>"), "x"); err == nil {
		t.Fatal("不是订阅源应该报错")
	}
}

func TestCoinsInTitle(t *testing.T) {
	s := &Service{Coins: []string{"BTC", "ETH", "SOL", "SUI"}}
	s.init()
	cases := map[string]string{
		"Bitcoin tops $100k":                 "BTC",
		"SOL & ETH lead gains; new solution": "ETH,SOL",
		"$BTC dips while Ether holds":        "BTC,ETH",
		"Suite of tools for builders":        "",
		"Ethereum's upgrade":                 "ETH",
		"BTCUSD breaks out":                  "",
		"Sui network outage (SUI)":           "SUI",
	}
	for title, want := range cases {
		if got := strings.Join(s.coinsIn(title), ","); got != want {
			t.Errorf("%q：认出 %q，应该是 %q", title, got, want)
		}
	}
}

func TestServicePollAndAPI(t *testing.T) {
	var hits, xAuthBad atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch {
		case r.URL.Path == "/rss":
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write([]byte(rssBody))
		case r.URL.Path == "/atom":
			_, _ = w.Write([]byte(atomBody))
		case r.URL.Path == "/down":
			w.WriteHeader(http.StatusBadGateway)
		case strings.HasPrefix(r.URL.Path, "/2/"):
			if r.Header.Get("Authorization") != "Bearer tok" {
				xAuthBad.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Path == "/2/users/by" {
				_, _ = w.Write([]byte(`{"data":[{"id":"42","username":"SomeExchange"}]}`))
				return
			}
			if r.URL.Path == "/2/users/42/tweets" {
				_, _ = w.Write([]byte(`{"data":[{"id":"9001","text":"Maintenance for ETH deposits at 08:00 UTC https://t.co/x","created_at":"2026-11-10T11:00:00.000Z"}]}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s := &Service{Feeds: []string{srv.URL + "/rss", srv.URL + "/atom", srv.URL + "/down"}, XToken: "tok", XAccounts: []string{"someexchange"}, XBase: srv.URL,
		Coins: []string{"BTC", "ETH", "SOL"}, Now: func() time.Time { return now }}
	s.PollOnce(context.Background(), true)
	items, states := s.List("", 50)
	if len(items) != 4 {
		t.Fatalf("应该有 4 条（RSS 2、Atom 1、X 1）：%+v", items)
	}
	if items[0].Kind != "x" || items[0].URL != "https://x.com/someexchange/status/9001" || items[0].Source != "X @someexchange" {
		t.Fatalf("最新的是 X 那条：%+v", items[0])
	}
	var bad *SourceState
	for i := range states {
		if strings.HasSuffix(states[i].Name, "/down") {
			bad = &states[i]
		}
	}
	if bad == nil || bad.Err == "" || bad.OK != 0 || len(states) != 4 {
		t.Fatalf("取不到的来源要记下原因：%+v", states)
	}
	if eth, _ := s.List("eth", 50); len(eth) != 2 {
		t.Fatalf("按币筛：%+v", eth)
	}
	// 第二次：RSS 没变（304）也不丢旧的
	s.PollOnce(context.Background(), false)
	if again, _ := s.List("", 50); len(again) != 4 {
		t.Fatalf("304 以后：%d 条", len(again))
	}
	if xAuthBad.Load() != 0 {
		t.Fatal("X 的 token 没带对")
	}

	mux := http.NewServeMux()
	s.Routes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/news?coin=BTC&limit=5", nil))
	var out struct {
		Items   []Item        `json:"items"`
		Sources []SourceState `json:"sources"`
		Feeds   int           `json:"feeds"`
		X       int           `json:"x"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Items) != 1 || out.Feeds != 3 || out.X != 1 {
		t.Fatalf("接口：%v %s", err, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tok") {
		t.Fatal("接口不能带出 X 的 token")
	}
}

func TestNoSourcesReturnsEmpty(t *testing.T) {
	t.Setenv("NEWS_FEEDS", "off")
	t.Setenv("NEWS_NOTICES", "off")
	t.Setenv("X_BEARER_TOKEN", "")
	s := FromEnv([]string{"BTC"}, nil)
	if len(s.Feeds) != 0 {
		t.Fatalf("off 应该一个都不取：%v", s.Feeds)
	}
	done := make(chan struct{})
	go func() { s.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("没有来源时 Run 应该直接返回")
	}
	mux := http.NewServeMux()
	s.Routes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/news", nil))
	if !strings.Contains(rec.Body.String(), `"items":[]`) || !strings.Contains(rec.Body.String(), `"feeds":0`) {
		t.Fatalf("没来源：%s", rec.Body.String())
	}
	t.Setenv("NEWS_FEEDS", "")
	if d := FromEnv(nil, nil); len(d.Feeds) != len(DefaultFeeds) {
		t.Fatal("不写 NEWS_FEEDS 用默认的")
	}
	t.Setenv("NEWS_FEEDS", " https://a.example/rss , ,https://b.example/feed ")
	t.Setenv("X_ACCOUNTS", "@one, two")
	if d := FromEnv(nil, nil); len(d.Feeds) != 2 || len(d.XAccounts) != 2 || d.XAccounts[0] != "one" {
		t.Fatalf("自己写的：%v %v", d.Feeds, d.XAccounts)
	}
}

// 交易所公告（18 第 10.2 条）：Bybit 只要上新、下架、维护三类；OKX 只要上新、下架、交易规则调整，活动推广不要
func TestNotices(t *testing.T) {
	var bybitTypesAsked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v5/announcements/index":
			typ := r.URL.Query().Get("type")
			bybitTypesAsked = append(bybitTypesAsked, typ)
			if typ == "maintenance_updates" {
				w.WriteHeader(http.StatusBadGateway) // 一类取不到不影响别的
				return
			}
			_, _ = w.Write([]byte(`{"retCode":0,"result":{"list":[{"title":"Delisting of ` + strings.ToUpper(typ) + ` ETH Perpetual Contract","url":"https://announcements.bybit.com/a/` + typ + `","dateTimestamp":1794500000000,"publishTime":1794500001000}]}}`))
		case "/api/v5/support/announcements":
			_, _ = w.Write([]byte(`{"code":"0","data":[{"details":[
				{"annType":"announcements-new-listings","title":"OKX will launch SOL margin","url":"https://www.okx.com/help/1","pTime":"1794400000000"},
				{"annType":"latest-events","title":"Win 1,000,000 USDT in our campaign","url":"https://www.okx.com/help/2","pTime":"1794400000001"},
				{"annType":"announcements-delistings","title":"Delist XYZ","url":"javascript:alert(1)","pTime":"1794400000002"}]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	s := &Service{Notices: true, BybitBase: srv.URL, OKXBase: srv.URL, Coins: []string{"ETH", "SOL"}, Now: func() time.Time { return time.UnixMilli(1794600000000) }}
	s.PollOnce(context.Background(), false)
	items, states := s.List("", 50)
	if len(bybitTypesAsked) != 3 || strings.Join(bybitTypesAsked, ",") != "new_crypto,delistings,maintenance_updates" {
		t.Fatalf("Bybit 问的分类：%v", bybitTypesAsked)
	}
	var bybit, okx int
	for _, it := range items {
		if it.Kind != "notice" {
			t.Fatalf("公告的种类：%+v", it)
		}
		if strings.Contains(it.Title, "campaign") || strings.Contains(it.URL, "javascript") {
			t.Fatalf("活动和坏链接不要：%+v", it)
		}
		switch it.Source {
		case "Bybit":
			bybit++
		case "OKX":
			okx++
		}
	}
	if bybit != 2 || okx != 1 {
		t.Fatalf("Bybit 2 条、OKX 1 条：%+v", items)
	}
	for _, st := range states {
		if st.Err != "" {
			t.Fatalf("一类取不到不算这个来源坏了：%+v", st)
		}
	}
	if eth, _ := s.List("ETH", 50); len(eth) != 2 {
		t.Fatalf("公告也按币筛：%d", len(eth))
	}
}
