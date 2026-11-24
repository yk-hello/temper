package aireview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// ListModels 问这家有哪些模型（系统页"获取模型列表"，D112，11-24 用户："还要下载模型……选择一个"）。
// OpenAI 兼容：GET {地址}/models；Claude：官方 SDK 的模型列表（自动翻页）。最多返回 500 个，按名字排好。
// 错误里不带密钥（密钥只在请求头里）；对方返回的内容不进错误。
func ListModels(ctx context.Context, format, key, base string, hc *http.Client) ([]string, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	var ids []string
	if format == "anthropic" {
		opts := []option.RequestOption{option.WithAPIKey(key), option.WithHTTPClient(hc), option.WithMaxRetries(1)}
		if b := ClaudeBase(base); b != "" {
			opts = append(opts, option.WithBaseURL(b+"/"))
		}
		client := anthropic.NewClient(opts...)
		it := client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{Limit: anthropic.Int(1000)})
		for it.Next() && len(ids) < 500 {
			ids = append(ids, it.Current().ID)
		}
		if err := it.Err(); err != nil {
			var ae *anthropic.Error
			if errors.As(err, &ae) {
				return nil, fmt.Errorf("HTTP %d", ae.StatusCode)
			}
			var ue *url.Error
			if errors.As(err, &ue) {
				return nil, ue.Err
			}
			return nil, errors.New("request failed")
		}
	} else {
		req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+"/models", nil)
		if err != nil {
			return nil, errors.New("bad request")
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := hc.Do(req)
		if err != nil {
			var ue *url.Error
			if errors.As(err, &ue) {
				return nil, ue.Err
			}
			return nil, errors.New("request failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		var r struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r); err != nil {
			return nil, errors.New("bad response")
		}
		for _, m := range r.Data {
			if m.ID != "" && len(ids) < 500 {
				ids = append(ids, m.ID)
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}
