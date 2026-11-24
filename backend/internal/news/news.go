// Package news 新闻标题（18 第 8.6 条，D56、D100 第 8 条）。
//
// 只用公开、允许被订阅的来源：
//   - RSS / Atom：默认几家加密媒体的公开订阅源，部署的人可以用 NEWS_FEEDS 换掉或关掉（NEWS_FEEDS=off）
//   - X：只走官方接口，部署的人自己填 X_BEARER_TOKEN 和要看的账号（X_ACCOUNTS），不抓网页
//
// 只存、只显示标题、来源、时间和原文链接，不搬正文（内容在原网站）。平台不拿新闻做任何买卖判断：
// 新闻是给人看的原料，接进来的策略要用，自己去读这些源。
package news

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultFeeds 默认的公开订阅源（2026-11 查过都能订阅）。部署的人用 NEWS_FEEDS 改。
var DefaultFeeds = []string{
	"https://www.coindesk.com/arc/outboundfeeds/rss/",
	"https://cointelegraph.com/rss",
	"https://decrypt.co/feed",
}

// Item 一条新闻：只有标题、来源、时间、链接。
type Item struct {
	ID     string   `json:"id"`
	Source string   `json:"source"`
	Title  string   `json:"title"`
	URL    string   `json:"url"`
	Time   int64    `json:"time"` // 毫秒
	Coins  []string `json:"coins,omitempty"`
	Kind   string   `json:"kind"` // rss | x | notice（交易所公告）
}

// SourceState 一个来源最近一次取得怎么样（给界面看）。
type SourceState struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	OK     int64  `json:"ok"`              // 最近一次成功（毫秒），0 = 还没成功过
	Err    string `json:"error,omitempty"` // 最近一次失败的原因
	Items  int    `json:"items"`
	Polled int64  `json:"polled"`
}

const (
	maxItems  = 400
	maxBody   = 4 << 20
	rssEvery  = 5 * time.Minute
	xEvery    = 15 * time.Minute // X 的接口按次数限流、按量收费：一刻钟一次
	userAgent = "qs-news/1 (+self-hosted RSS reader)"
)

// Service 定时取各个来源，留最近 maxItems 条。
type Service struct {
	Feeds      []string     // RSS / Atom 地址
	XToken     string       // X 官方接口的 Bearer token（部署的人自己的）
	xMu        sync.RWMutex // 管理后台随时能换（2026-11-15）
	XAccounts  []string     // 要看的 X 账号（不带 @）
	XBase      string       // 测试用；默认 https://api.x.com
	Notices    bool         // 取 Bybit、OKX 的官方公告（NEWS_NOTICES=off 关掉）
	BybitBase  string       // 测试用
	OKXBase    string       // 测试用
	Coins      []string     // 认哪些币（BTC、ETH……）
	HTTP       *http.Client
	Log        *slog.Logger
	Now        func() time.Time
	mu         sync.Mutex
	items      map[string]Item
	states     map[string]*SourceState
	etags      map[string]string
	xIDs       map[string]string // 账号 → X 的用户编号
	coinRe     *regexp.Regexp
	coinByWord map[string]string
}

// FromEnv 读部署的人的设置。NEWS_FEEDS 不写用默认；写 off 一个都不取。
func FromEnv(coins []string, log *slog.Logger) *Service {
	s := &Service{Coins: coins, Log: log, XToken: strings.TrimSpace(os.Getenv("X_BEARER_TOKEN")), XBase: strings.TrimSpace(os.Getenv("X_API_BASE")),
		Notices: !strings.EqualFold(strings.TrimSpace(os.Getenv("NEWS_NOTICES")), "off")}
	if b := strings.TrimSpace(os.Getenv("NEWS_NOTICE_BASE")); b != "" { // 测试环境：两家的公告都去本机取
		s.BybitBase, s.OKXBase = b, b
	}
	switch v := strings.TrimSpace(os.Getenv("NEWS_FEEDS")); {
	case v == "":
		s.Feeds = DefaultFeeds
	case strings.EqualFold(v, "off"):
	default:
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				s.Feeds = append(s.Feeds, f)
			}
		}
	}
	for _, a := range strings.Split(os.Getenv("X_ACCOUNTS"), ",") {
		if a = strings.TrimPrefix(strings.TrimSpace(a), "@"); a != "" {
			s.XAccounts = append(s.XAccounts, a)
		}
	}
	return s
}

// 常见币的英文名，标题里写全名也认得出来
var coinNames = map[string][]string{
	"BTC": {"bitcoin"}, "ETH": {"ethereum", "ether"}, "SOL": {"solana"}, "BNB": {"binance coin"}, "XRP": {"ripple"},
	"DOGE": {"dogecoin"}, "ADA": {"cardano"}, "AVAX": {"avalanche"}, "LINK": {"chainlink"}, "TON": {"toncoin"},
	"TRX": {"tron"}, "DOT": {"polkadot"}, "LTC": {"litecoin"}, "SUI": {"sui"}, "HYPE": {"hyperliquid"},
}

func (s *Service) init() {
	if s.items != nil {
		return
	}
	s.items, s.states, s.etags, s.xIDs = map[string]Item{}, map[string]*SourceState{}, map[string]string{}, map[string]string{}
	if s.HTTP == nil {
		s.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.XBase == "" {
		s.XBase = "https://api.x.com"
	}
	s.coinByWord = map[string]string{}
	var words []string
	for _, c := range s.Coins {
		c = strings.ToUpper(c)
		s.coinByWord[strings.ToLower(c)] = c
		words = append(words, regexp.QuoteMeta(strings.ToLower(c)))
		for _, n := range coinNames[c] {
			s.coinByWord[n] = c
			words = append(words, regexp.QuoteMeta(n))
		}
	}
	if len(words) > 0 {
		sort.Slice(words, func(i, j int) bool { return len(words[i]) > len(words[j]) })
		s.coinRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9$])\$?(` + strings.Join(words, "|") + `)(?:[^a-z0-9]|$)`)
	}
}

// coinsIn 标题里提到了哪些币（只看标题，按词认，不按子串：SOL 不会认成 SOLUTION）。
func (s *Service) coinsIn(title string) []string {
	if s.coinRe == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range s.coinRe.FindAllStringSubmatch(title, -1) {
		c := s.coinByWord[strings.ToLower(m[1])]
		if c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// Run 一直取，直到 ctx 结束。没有任何来源时直接返回。
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.init()
	s.mu.Unlock()
	if len(s.Feeds) == 0 && (s.xToken() == "" || len(s.xAccounts()) == 0) && !s.Notices {
		return
	}
	s.PollOnce(ctx, true)
	rss := time.NewTicker(rssEvery)
	defer rss.Stop()
	x := time.NewTicker(xEvery)
	defer x.Stop()
	notice := time.NewTicker(noticeEvery)
	defer notice.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rss.C:
			s.pollFeeds(ctx)
		case <-x.C:
			s.pollX(ctx)
		case <-notice.C:
			s.pollNotices(ctx)
		}
	}
}

// PollOnce 把所有来源取一遍（启动时、测试用）。withX = 也取 X。
func (s *Service) PollOnce(ctx context.Context, withX bool) {
	s.mu.Lock()
	s.init()
	s.mu.Unlock()
	s.pollFeeds(ctx)
	s.pollNotices(ctx)
	if withX {
		s.pollX(ctx)
	}
}

func (s *Service) pollFeeds(ctx context.Context) {
	var wg sync.WaitGroup
	for _, f := range s.Feeds {
		wg.Add(1)
		go func(f string) {
			defer wg.Done()
			items, err := s.fetchFeed(ctx, f)
			s.store(f, "rss", items, err)
		}(f)
	}
	wg.Wait()
}

func (s *Service) store(src, kind string, items []Item, err error) {
	now := s.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[src]
	if st == nil {
		st = &SourceState{Name: src, Kind: kind}
		s.states[src] = st
	}
	st.Polled = now
	if err != nil {
		st.Err = err.Error()
		if s.Log != nil {
			s.Log.Warn("新闻来源取不到", "source", src, "err", err)
		}
		return
	}
	st.Err, st.OK = "", now
	for _, it := range items {
		if it.Time > now+5*60_000 { // 时间写在未来的：按现在算
			it.Time = now
		}
		it.Coins = s.coinsIn(it.Title)
		s.items[it.ID] = it
	}
	st.Items = len(items)
	if len(s.items) > maxItems {
		all := s.sortedLocked()
		for _, it := range all[maxItems:] {
			delete(s.items, it.ID)
		}
	}
}

func (s *Service) sortedLocked() []Item {
	out := make([]Item, 0, len(s.items))
	for _, it := range s.items {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Time != out[j].Time {
			return out[i].Time > out[j].Time
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// List 最近的新闻，新的在前。coin 不为空时只要标题里提到它的。
func (s *Service) List(coin string, limit int) ([]Item, []SourceState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	coin = strings.ToUpper(strings.TrimSpace(coin))
	var out []Item
	for _, it := range s.sortedLocked() {
		if coin != "" {
			hit := false
			for _, c := range it.Coins {
				hit = hit || c == coin
			}
			if !hit {
				continue
			}
		}
		out = append(out, it)
		if len(out) >= limit {
			break
		}
	}
	states := make([]SourceState, 0, len(s.states))
	for _, st := range s.states {
		states = append(states, *st)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Name < states[j].Name })
	return out, states
}

// Configured 有没有配任何来源。
func (s *Service) Configured() (rss int, x int) {
	if s.xToken() != "" {
		x = len(s.xAccounts())
	}
	return len(s.Feeds), x
}

// ---------- RSS / Atom ----------

type rssDoc struct {
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title   string `xml:"title"`
			Link    string `xml:"link"`
			GUID    string `xml:"guid"`
			PubDate string `xml:"pubDate"`
			Date    string `xml:"http://purl.org/dc/elements/1.1/ date"`
		} `xml:"item"`
	} `xml:"channel"`
}

type atomDoc struct {
	Title   string `xml:"title"`
	Entries []struct {
		Title string `xml:"title"`
		Links []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
		ID        string `xml:"id"`
		Updated   string `xml:"updated"`
		Published string `xml:"published"`
	} `xml:"entry"`
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// clean 标题：去掉标签、实体、多余空白，最长 300 个字。
func clean(s string) string {
	s = html.UnescapeString(tagRe.ReplaceAllString(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:299]) + "…"
	}
	return s
}

func parseTime(s string) int64 {
	s = strings.TrimSpace(s)
	for _, f := range []string{time.RFC1123Z, time.RFC1123, time.RFC3339, "Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST", "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(f, s); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

// safeLink 只收 http/https 的链接（不让 javascript: 之类的进页面）。
func safeLink(raw, base string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if !u.IsAbs() && base != "" {
		if b, err := url.Parse(base); err == nil {
			u = b.ResolveReference(u)
		}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

func idOf(link, title string) string {
	h := sha256.Sum256([]byte(link + "\n" + title))
	return hex.EncodeToString(h[:8])
}

// sourceName 来源显示的名字：订阅源自己写的标题，没有就用域名。
func sourceName(title, feedURL string) string {
	if t := clean(title); t != "" {
		if r := []rune(t); len(r) > 40 {
			return string(r[:40])
		}
		return t
	}
	if u, err := url.Parse(feedURL); err == nil {
		return strings.TrimPrefix(u.Hostname(), "www.")
	}
	return feedURL
}

func (s *Service) fetchFeed(ctx context.Context, feed string) ([]Item, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feed, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.5")
	s.mu.Lock()
	if e := s.etags[feed]; e != "" {
		req.Header.Set("If-None-Match", e)
	}
	s.mu.Unlock()
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified {
		return nil, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("对方回了 %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if e := res.Header.Get("ETag"); e != "" {
		s.mu.Lock()
		s.etags[feed] = e
		s.mu.Unlock()
	}
	return parseFeed(body, feed)
}

// parseFeed RSS 2.0 或 Atom。
func parseFeed(body []byte, feed string) ([]Item, error) {
	var r rssDoc
	if err := xml.Unmarshal(body, &r); err == nil && len(r.Channel.Items) > 0 {
		src := sourceName(r.Channel.Title, feed)
		var out []Item
		for _, it := range r.Channel.Items {
			link := safeLink(it.Link, feed)
			if link == "" {
				link = safeLink(it.GUID, feed)
			}
			title := clean(it.Title)
			if link == "" || title == "" {
				continue
			}
			t := parseTime(it.PubDate)
			if t == 0 {
				t = parseTime(it.Date)
			}
			out = append(out, Item{ID: idOf(link, title), Source: src, Title: title, URL: link, Time: t, Kind: "rss"})
		}
		return out, nil
	}
	var a atomDoc
	if err := xml.Unmarshal(body, &a); err == nil && len(a.Entries) > 0 {
		src := sourceName(a.Title, feed)
		var out []Item
		for _, e := range a.Entries {
			link := ""
			for _, l := range e.Links {
				if l.Rel == "" || l.Rel == "alternate" {
					link = safeLink(l.Href, feed)
					break
				}
			}
			title := clean(e.Title)
			if link == "" || title == "" {
				continue
			}
			t := parseTime(e.Published)
			if t == 0 {
				t = parseTime(e.Updated)
			}
			out = append(out, Item{ID: idOf(link, title), Source: src, Title: title, URL: link, Time: t, Kind: "rss"})
		}
		return out, nil
	}
	return nil, errors.New("不是 RSS 或 Atom")
}

// ---------- X（官方接口，部署的人自己的 token） ----------

func (s *Service) xGet(ctx context.Context, path string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.XBase, "/")+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.xToken())
	req.Header.Set("User-Agent", userAgent)
	res, err := s.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("连不上 X：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests {
		return errors.New("X 限流了，过一会儿再取")
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return fmt.Errorf("X 不认这个 token（%d）：检查 X_BEARER_TOKEN 和套餐", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("X 回了 %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, maxBody)).Decode(out)
}

func (s *Service) pollX(ctx context.Context) {
	if s.xToken() == "" || len(s.xAccounts()) == 0 {
		return
	}
	// 账号名换成用户编号：一次查完，以后不再查
	s.mu.Lock()
	var missing []string
	for _, a := range s.xAccounts() {
		if s.xIDs[strings.ToLower(a)] == "" {
			missing = append(missing, a)
		}
	}
	s.mu.Unlock()
	if len(missing) > 0 {
		var r struct {
			Data []struct{ ID, Username string } `json:"data"`
		}
		if err := s.xGet(ctx, "/2/users/by", url.Values{"usernames": {strings.Join(missing, ",")}}, &r); err != nil {
			s.store("X", "x", nil, err)
			return
		}
		s.mu.Lock()
		for _, u := range r.Data {
			s.xIDs[strings.ToLower(u.Username)] = u.ID
		}
		s.mu.Unlock()
	}
	for _, a := range s.xAccounts() {
		s.mu.Lock()
		id := s.xIDs[strings.ToLower(a)]
		s.mu.Unlock()
		name := "X @" + a
		if id == "" {
			s.store(name, "x", nil, errors.New("X 上找不到这个账号"))
			continue
		}
		var r struct {
			Data []struct {
				ID        string `json:"id"`
				Text      string `json:"text"`
				CreatedAt string `json:"created_at"`
			} `json:"data"`
		}
		err := s.xGet(ctx, "/2/users/"+id+"/tweets", url.Values{"max_results": {"10"}, "tweet.fields": {"created_at"}, "exclude": {"replies"}}, &r)
		var items []Item
		for _, t := range r.Data {
			link := "https://x.com/" + a + "/status/" + t.ID
			title := clean(t.Text)
			if title == "" {
				continue
			}
			items = append(items, Item{ID: idOf(link, ""), Source: name, Title: title, URL: link, Time: parseTime(t.CreatedAt), Kind: "x"})
		}
		s.store(name, "x", items, err)
	}
}

// Routes GET /api/news?coin=BTC&limit=30：不用登录（和经济日历一样是公开信息）。
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/news", func(w http.ResponseWriter, r *http.Request) {
		limit := 30
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
				limit = n
			}
		}
		items, states := s.List(r.URL.Query().Get("coin"), limit)
		if items == nil {
			items = []Item{}
		}
		rss, x := s.Configured()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "sources": states, "feeds": rss, "x": x, "notices": s.Notices})
	})
}

// SetX 换 X 的令牌和要看的账号（管理后台）。下一轮取新闻时生效。
func (s *Service) SetX(token string, accounts []string) {
	s.xMu.Lock()
	s.XToken, s.XAccounts = token, accounts
	s.xMu.Unlock()
}

func (s *Service) xToken() string {
	s.xMu.RLock()
	defer s.xMu.RUnlock()
	return s.XToken
}

func (s *Service) xAccounts() []string {
	s.xMu.RLock()
	defer s.xMu.RUnlock()
	return s.XAccounts
}
