package calendar

// 日程从哪里来（18 第 10.1 条，D102）：
//   - FRED（圣路易斯联储的官方接口）：部署的人填一个免费的 FRED_API_KEY，CPI、非农、PPI、PCE、GDP、议息的发布日期全自动更新。
//     FRED 只给日期，时间按惯例补：议息美东 14:00，别的 08:30；同一天有更准的来源（经济分析局的日历文件、内置日程）时用更准的。
//   - 经济分析局（BEA）的日历文件：不用密钥，在线取（calendar.go 的 fetch）。
//   - 劳工统计局（BLS）：拒绝脚本访问，不绕过（D56）。没配 FRED 时用内置的：bls.ics（到 2026 年底）加上下面的 blsMore。
//     内置日程快用完（不到 60 天）时，接口带上 until，界面提醒配 FRED 或者升级。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// blsMore 2027 年的日程，2026-11-13 从 bls.gov 的发布日程页（schedule/news_release/cpi.htm、empsit.htm、ppi.htm）抄的，都是美东 08:30。
var blsMore = map[string][]string{
	"Consumer Price Index": {"2027-01-13", "2027-02-11", "2027-03-10", "2027-04-13", "2027-05-12", "2027-06-10", "2027-07-14", "2027-08-11", "2027-09-14", "2027-10-14", "2027-11-10", "2027-12-10"},
	"Employment Situation": {"2027-01-08", "2027-02-05", "2027-03-05", "2027-04-02", "2027-05-07", "2027-06-04", "2027-07-02", "2027-08-06", "2027-09-03", "2027-10-08", "2027-11-05", "2027-12-03"},
	"Producer Price Index": {"2027-01-14", "2027-02-12", "2027-03-12", "2027-04-14", "2027-05-13", "2027-06-11", "2027-07-15", "2027-08-10", "2027-09-10", "2027-10-15", "2027-11-12", "2027-12-15"},
}

var eastern = func() *time.Location {
	if l, err := time.LoadLocation("America/New_York"); err == nil {
		return l
	}
	return time.FixedZone("EST", -5*3600)
}()

// at 美东某天某时。
func at(day string, hh, mm int) (int64, bool) {
	d, err := time.ParseInLocation("2006-01-02", day, eastern)
	if err != nil {
		return 0, false
	}
	return time.Date(d.Year(), d.Month(), d.Day(), hh, mm, 0, 0, eastern).UnixMilli(), true
}

// builtinBLS 内置的劳工统计局日程：bls.ics 加 blsMore。
func builtinBLS() []Event {
	out := ParseICS(bytes.NewReader(blsICS))
	for title, days := range blsMore {
		for _, d := range days {
			if t, ok := at(d, 8, 30); ok {
				out = append(out, Event{T: t, Key: KeyOf(title), Title: title})
			}
		}
	}
	return out
}

// BuiltinUntil 内置的劳工统计局日程最后一条是什么时候（毫秒）。
func BuiltinUntil() int64 {
	var m int64
	for _, e := range builtinBLS() {
		if e.T > m {
			m = e.T
		}
	}
	return m
}

// dayKey 同一类事件同一天（美东）算一条。
func dayKey(e Event) string {
	return e.Key + "|" + time.UnixMilli(e.T).In(eastern).Format("2006-01-02")
}

// merge 把几个来源合在一起：同一类同一天只留一条，排在前面的来源优先（更准的放前面）。
func merge(sources ...[]Event) []Event {
	seen := map[string]bool{}
	var out []Event
	for _, src := range sources {
		for _, e := range src {
			if e.Key == "" {
				continue
			}
			k := dayKey(e)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

// fredReleases FRED 的发布名 → 我们的分类（只要这几种）。
func fredKey(name string) string {
	if k := KeyOf(name); k != "" {
		return k
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "fomc press release") {
		return "fomc"
	}
	return ""
}

// fetchFRED 从 FRED 取今天往后（含前 7 天）的发布日期，按名字挑出要的几种。最多翻 10 页（每页 1000 条）。
func (s *Service) fetchFRED(ctx context.Context) ([]Event, error) {
	key := s.fredKey()
	if key == "" {
		return nil, nil
	}
	base := strings.TrimRight(s.FREDBase, "/")
	if base == "" {
		base = "https://api.stlouisfed.org"
	}
	hc := s.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	start := time.Now().In(eastern).AddDate(0, 0, -7).Format("2006-01-02")
	var out []Event
	for page := 0; page < 10; page++ {
		q := url.Values{"api_key": {key}, "file_type": {"json"}, "include_release_dates_with_no_data": {"true"}, "realtime_start": {start},
			"order_by": {"release_date"}, "sort_order": {"asc"}, "limit": {"1000"}, "offset": {strconv.Itoa(page * 1000)}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/fred/releases/dates?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "qs-calendar/1 (+self-hosted)")
		resp, err := hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("连不上 FRED：%w", stripKey(err, key))
		}
		var r struct {
			ReleaseDates []struct {
				Name string `json:"release_name"`
				Date string `json:"date"`
			} `json:"release_dates"`
			Error string `json:"error_message"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r)
		resp.Body.Close()
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return nil, errors.New("FRED 不认这个密钥：检查 FRED_API_KEY")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("FRED 回了 %d", resp.StatusCode)
		}
		if err != nil {
			return nil, err
		}
		for _, d := range r.ReleaseDates {
			k := fredKey(d.Name)
			if k == "" {
				continue
			}
			hh, mm := 8, 30
			if k == "fomc" {
				hh, mm = 14, 0
			}
			if t, ok := at(d.Date, hh, mm); ok {
				out = append(out, Event{T: t, Key: k, Title: d.Name})
			}
		}
		if len(r.ReleaseDates) < 1000 {
			break
		}
	}
	return out, nil
}

// stripKey 错误信息里可能带着请求地址（含密钥）：去掉。
func stripKey(err error, key string) error {
	if key == "" || err == nil {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "***"))
}
