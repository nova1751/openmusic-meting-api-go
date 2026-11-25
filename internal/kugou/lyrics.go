package kugou

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"github.com/nova1751/openmusic-meting-api-go/internal/music"
	"github.com/nova1751/openmusic-meting-api-go/internal/providerhttp"
)

const maxLyrics = 1 << 20

func (c *Client) lyric(ctx context.Context, req music.Request, hash string) (music.Result, error) {
	res, err := c.call(ctx, request{address: lyricsHost + "/search", cookie: req.Cookie, params: map[string]string{
		"ver": "1", "man": "yes", "client": "pc", "hash": hash}})
	if err != nil {
		return music.Result{}, err
	}
	candidates := res.List("candidates")
	result := music.Result{Lyrics: music.Lyrics{Source: "lrc"}}
	if len(candidates) == 0 {
		return result, nil
	}
	entry := providerhttp.AsObject(candidates[0])
	id, key := entry.Text("id"), entry.Text("accesskey")
	if id == "" || key == "" {
		return result, nil
	}
	download := func(format string) ([]byte, error) {
		res, err := c.call(ctx, request{address: lyricsHost + "/download", cookie: req.Cookie, params: map[string]string{
			"ver": "1", "client": "pc", "id": id, "accesskey": key, "fmt": format, "charset": "utf8"}})
		if err != nil {
			return nil, err
		}
		content := strings.TrimRight(res.Text("content"), "=")
		if base64.RawStdEncoding.DecodedLen(len(content)) > maxLyrics {
			return nil, errors.New("Kugou lyrics exceed size limit")
		}
		return base64.RawStdEncoding.DecodeString(content)
	}
	lines, err := download("lrc")
	result.Lyrics.Lyric = string(lines)
	if req.Format != "json" {
		return result, err
	}
	if data, err := download("krc"); err == nil {
		if words, err := decodeKRC(data); err == nil && words != "" {
			result.Lyrics.WordLyric, result.Lyrics.WordFormat, result.Lyrics.Source = words, "krc", "platform"
		}
	}
	return result, nil
}

func decodeKRC(data []byte) (string, error) {
	if len(data) < 4 || string(data[:4]) != "krc1" {
		return "", errors.New("invalid KRC header")
	}
	key := [16]byte{64, 71, 97, 119, 94, 50, 116, 71, 81, 54, 49, 45, 206, 210, 110, 105}
	compressed := append([]byte(nil), data[4:]...)
	for i := range compressed {
		compressed[i] ^= key[i%len(key)]
	}
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", err
	}
	defer reader.Close()
	result, err := io.ReadAll(io.LimitReader(reader, maxLyrics+1))
	if err != nil {
		return "", err
	}
	if len(result) > maxLyrics {
		return "", errors.New("KRC expands past lyric size limit")
	}
	return string(result), nil
}
