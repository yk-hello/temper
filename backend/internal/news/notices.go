package news

// 交易所公告（18 第 10.2 条，D102）：Bybit、OKX 的官方公开接口，只要上新、下架、维护和交易规则调整这几类；
// 活动、返佣、理财推广一律不要（D90）。币安没有公开的公告接口，不抓网页（D56），没接。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const noticeEvery = 10 * time.Minute

// bybitTypes Bybit 的公告分类：新币上线、下架、维护和调整。
var bybitTypes = []string{"new_crypto", "delistings", "maintenance_updates"}

// okxTypes OKX 的公告分类：新币上线、下架、交易规则调整。
var okxTypes = map[string]bool{"announcements-new-listings": true, "announcements-delistings": true, "announcements-trading-updates": true}

func (s *Service) noticeGet(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	res, err := s.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("连不上：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("对方回了 %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, maxBody)).Decode(v)
}

func (s *Service) pollNotices(ctx context.Context) {
	if !s.Notices {
		return
	}
	bybit := strings.TrimRight(s.BybitBase, "/")
	if bybit == "" {
		bybit = "https://api.bybit.com"
	}
	okx := strings.TrimRight(s.OKXBase, "/")
	if okx == "" {
		okx = "https://www.okx.com"
	}
	var items []Item
	var err error
	okCount := 0
	for _, t := range bybitTypes {
		var r struct {
			RetCode int `json:"retCode"`
			Result  struct {
				List []struct {
					Title   string `json:"title"`
					URL     string `json:"url"`
					Date    int64  `json:"dateTimestamp"`
					Publish int64  `json:"publishTime"`
				} `json:"list"`
			} `json:"result"`
		}
		if e := s.noticeGet(ctx, bybit+"/v5/announcements/index?"+url.Values{"locale": {"en-US"}, "type": {t}, "limit": {"10"}}.Encode(), &r); e != nil {
			err = e
			continue
		}
		okCount++
		for _, a := range r.Result.List {
			link, title := safeLink(a.URL, bybit), clean(a.Title)
			if link == "" || title == "" {
				continue
			}
			ts := a.Publish
			if ts == 0 {
				ts = a.Date
			}
			items = append(items, Item{ID: idOf(link, title), Source: "Bybit", Title: title, URL: link, Time: ts, Kind: "notice"})
		}
	}
	if okCount > 0 { // 三类里取到一类也算取到了
		err = nil
	}
	s.store("Bybit", "notice", items, err)

	items, err = nil, nil
	var r struct {
		Code string `json:"code"`
		Data []struct {
			Details []struct {
				Type  string `json:"annType"`
				Title string `json:"title"`
				URL   string `json:"url"`
				PTime string `json:"pTime"`
			} `json:"details"`
		} `json:"data"`
	}
	if err = s.noticeGet(ctx, okx+"/api/v5/support/announcements", &r); err == nil {
		for _, d := range r.Data {
			for _, a := range d.Details {
				if !okxTypes[a.Type] {
					continue
				}
				link, title := safeLink(a.URL, okx), clean(a.Title)
				if link == "" || title == "" {
					continue
				}
				ts, _ := strconv.ParseInt(a.PTime, 10, 64)
				items = append(items, Item{ID: idOf(link, title), Source: "OKX", Title: title, URL: link, Time: ts, Kind: "notice"})
			}
		}
	}
	s.store("OKX", "notice", items, err)
}
