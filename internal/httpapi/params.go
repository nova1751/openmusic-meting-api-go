package httpapi

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/qq01-hub/openmusic-meting-api-go/internal/music"
)

func publicRequest(q url.Values) music.Request {
	return music.Request{
		Platform: valueOr(q.Get("server"), "netease"),
		Type:     valueOr(q.Get("type"), "playlist"),
		ID:       valueOr(q.Get("id"), "6907557348"),
		Quality:  strings.ToLower(q.Get("quality")),
		Format:   q.Get("format"),
		FM:       q.Get("fm") == "1",
		Redirect: q.Get("redirect") == "1" || q.Get("redirect") == "true",
	}
}

func valueOr(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

const maxSafeInteger = 1<<53 - 1

func pagination(q url.Values, req music.Request) (music.Pagination, error) {
	if req.Platform != "netease" || (req.Type != "search" && req.Type != "dj") {
		return music.Pagination{}, nil
	}
	if !q.Has("limit") && !q.Has("page") && !q.Has("offset") {
		return music.Pagination{}, nil
	}
	limit := int64(30)
	if req.Type == "dj" {
		limit = 100
	}
	limit, okLimit := decimal(q, "limit", limit)
	page, okPage := decimal(q, "page", 1)
	offset, okOffset := decimal(q, "offset", 0)
	if !okLimit || !okPage || !okOffset || limit < 1 || limit > 100 || page < 1 {
		return music.Pagination{}, errors.New("分页参数不合法：limit 为 1-100，page 从 1 开始，offset 从 0 开始")
	}
	if !q.Has("offset") {
		if page-1 > maxSafeInteger/limit {
			return music.Pagination{}, errors.New("分页偏移超出范围")
		}
		offset = (page - 1) * limit
	}
	if offset > maxSafeInteger-limit {
		return music.Pagination{}, errors.New("分页偏移超出范围")
	}
	return music.Pagination{Limit: limit, Offset: offset, Explicit: true}, nil
}

func decimal(q url.Values, key string, fallback int64) (int64, bool) {
	if !q.Has(key) {
		return fallback, true
	}
	value := q.Get(key)
	if value == "" || strings.ContainsFunc(value, func(r rune) bool { return r < '0' || r > '9' }) {
		return 0, false
	}
	number, err := strconv.ParseInt(value, 10, 64)
	return number, err == nil && number <= maxSafeInteger
}
