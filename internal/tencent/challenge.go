package tencent

import (
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

type Challenge struct {
	SongMID string
	URL     string
	Expires time.Time
}

func (c *Client) rememberChallenge(cookie, song, raw string) bool {
	raw = strings.NewReplacer("\\:", ":", "\\/", "/", "\\_", "_").Replace(raw)
	target, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || target.Scheme != "https" || target.Hostname() != "c.y.qq.com" ||
		target.Port() != "" || target.User != nil {
		return false
	}
	if cookie == "" {
		return true
	}
	c.challengeMu.Lock()
	defer c.challengeMu.Unlock()
	now := time.Now()
	for key, entry := range c.challenges {
		if !entry.Expires.After(now) {
			delete(c.challenges, key)
		}
	}
	if len(c.challenges) >= 256 {
		var oldest [32]byte
		var stamp time.Time
		for key, entry := range c.challenges {
			if stamp.IsZero() || entry.Expires.Before(stamp) {
				oldest, stamp = key, entry.Expires
			}
		}
		delete(c.challenges, oldest)
	}
	c.challenges[sha256.Sum256([]byte(cookie))] = Challenge{SongMID: song, URL: target.String(), Expires: now.Add(5 * time.Minute)}
	return true
}

func (c *Client) Challenge(cookie string) (Challenge, bool) {
	c.challengeMu.Lock()
	defer c.challengeMu.Unlock()
	entry, ok := c.challenges[sha256.Sum256([]byte(cookie))]
	return entry, ok && time.Now().Before(entry.Expires)
}

func (c *Client) clearChallenge(cookie string) {
	if cookie == "" {
		return
	}
	c.challengeMu.Lock()
	delete(c.challenges, sha256.Sum256([]byte(cookie)))
	c.challengeMu.Unlock()
}

const (
	verificationWidth  = 400
	verificationHeight = 720
)

type VerificationBrowser struct {
	parent     context.Context
	executable string
	slots      chan struct{}
}

func NewVerificationBrowser(ctx context.Context, executable string) *VerificationBrowser {
	return &VerificationBrowser{parent: ctx, executable: executable, slots: make(chan struct{}, 2)}
}

type VerificationWindow struct {
	tab     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	pressed bool
}

func (b *VerificationBrowser) Open(ctx context.Context, cookie string, challenge Challenge) (*VerificationWindow, error) {
	if !time.Now().Before(challenge.Expires) {
		return nil, errors.New("the official QQ Music challenge expired")
	}
	select {
	case b.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, errors.New("QQ Music verification is busy")
	}
	life, finish := context.WithDeadline(b.parent, challenge.Expires)
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	if b.executable != "" {
		options = append(options, chromedp.ExecPath(b.executable))
	}
	alloc, stopAlloc := chromedp.NewExecAllocator(life, options...)
	tab, stopTab := chromedp.NewContext(alloc)
	context.AfterFunc(life, func() { stopTab(); stopAlloc(); <-b.slots })
	stopInit := context.AfterFunc(ctx, finish)
	defer stopInit()

	var credentials []*network.CookieParam
	for _, pair := range strings.Split(cookie, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || name == "" || value == "" || (&http.Cookie{Name: name, Value: value}).Valid() != nil {
			continue
		}
		credentials = append(credentials, &network.CookieParam{Name: name, Value: value,
			Domain: "c.y.qq.com", Path: "/", Secure: true, HTTPOnly: true})
	}
	if len(credentials) == 0 {
		finish()
		return nil, errors.New("no usable QQ Music credentials")
	}
	if err := chromedp.Run(tab,
		chromedp.EmulateViewport(verificationWidth, verificationHeight),
		network.SetCookies(credentials),
		chromedp.Navigate(challenge.URL),
	); err != nil {
		finish()
		return nil, err
	}
	return &VerificationWindow{tab: tab, cancel: finish}, nil
}

func (v *VerificationWindow) Close()       { v.cancel() }
func (v *VerificationWindow) Active() bool { return v.tab.Err() == nil }

func (v *VerificationWindow) Frame(ctx context.Context) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	request, cancel := context.WithCancel(v.tab)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	var image []byte
	err := chromedp.Run(request, chromedp.CaptureScreenshot(&image))
	return image, err
}

func (v *VerificationWindow) Pointer(ctx context.Context, kind string, x, y float64) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
		return errors.New("invalid pointer coordinates")
	}
	request, cancel := context.WithCancel(v.tab)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	x, y = min(max(x, 0), verificationWidth), min(max(y, 0), verificationHeight)
	switch kind {
	case "down":
		v.pressed = true
		return chromedp.Run(request, input.DispatchMouseEvent(input.MousePressed, x, y).WithButton(input.Left).WithButtons(1).WithClickCount(1))
	case "move":
		buttons := int64(0)
		if v.pressed {
			buttons = 1
		}
		return chromedp.Run(request, input.DispatchMouseEvent(input.MouseMoved, x, y).WithButtons(buttons))
	case "up":
		v.pressed = false
		return chromedp.Run(request, input.DispatchMouseEvent(input.MouseReleased, x, y).WithButton(input.Left).WithClickCount(1))
	default:
		return errors.New("invalid pointer event")
	}
}
