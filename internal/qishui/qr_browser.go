package qishui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
)

const passportAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) SodaMusic/3.2.1 Chrome/136.0.7103.59 Electron/36.4.0 Safari/537.36"

type QRBrowser struct {
	assets      map[string][]byte
	parent      context.Context
	address     string
	executable  string
	localScript string
	slots       chan struct{}
}

// NewQRBrowser serves the existing security SDK assets on an ephemeral
// loopback listener. Each QR session gets its own browser and cookie storage.
func NewQRBrowser(ctx context.Context, assetsDir, executable string) (*QRBrowser, error) {
	assets := make(map[string][]byte)
	for _, name := range []string{"security_host.html", "react.js", "react-dom.js", "sdk-glue.js", "bdms.js", "security_seed.html"} {
		f, err := os.Open(filepath.Join(assetsDir, name))
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(f, 2<<20))
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		if len(data) == 0 || len(data) == 2<<20 {
			return nil, errors.New("invalid Qishui SDK asset size")
		}
		assets["/"+name] = data
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, MaxHeaderBytes: 8 << 10, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := assets[r.URL.Path]
		if r.Method != http.MethodGet || len(value) == 0 {
			http.NotFound(w, r)
			return
		}
		kind := "application/javascript; charset=utf-8"
		if strings.HasSuffix(r.URL.Path, ".html") {
			kind = "text/html; charset=utf-8"
		}
		w.Header().Set("Content-Type", kind)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(value)
	})}
	go func() { _ = server.Serve(listener) }()
	context.AfterFunc(ctx, func() { _ = server.Close() })
	return &QRBrowser{assets: assets, parent: ctx, address: "http://" + listener.Addr().String() + "/security_host.html",
		executable: executable, localScript: base64.StdEncoding.EncodeToString(assets["/bdms.js"]), slots: make(chan struct{}, 2)}, nil
}

func (b *QRBrowser) open(ctx context.Context) (context.Context, func(), error) {
	select {
	case b.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	default:
		return nil, nil, errors.New("Qishui QR service is busy")
	}
	lifetime, end := context.WithTimeout(b.parent, 3*time.Minute)
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.Flag("disable-web-security", true), chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-features", "BlockThirdPartyCookies,ThirdPartyStoragePartitioning"), chromedp.Flag("disable-dev-shm-usage", true))
	if b.executable != "" {
		options = append(options, chromedp.ExecPath(b.executable))
	}
	alloc, stopAlloc := chromedp.NewExecAllocator(lifetime, options...)
	tab, stopTab := chromedp.NewContext(alloc)
	context.AfterFunc(lifetime, func() { stopTab(); stopAlloc(); <-b.slots })
	stopInit := context.AfterFunc(ctx, end)
	defer stopInit()

	chromedp.ListenTarget(tab, func(event any) {
		if pause, ok := event.(*fetch.EventRequestPaused); ok {
			go func() {
				conn := cdp.WithExecutor(tab, chromedp.FromContext(tab).Target)
				_ = fetch.FulfillRequest(pause.RequestID, http.StatusOK).
					WithResponseHeaders([]*fetch.HeaderEntry{{Name: "Content-Type", Value: "text/javascript; charset=utf-8"}}).
					WithBody(b.localScript).Do(conn)
			}()
		}
	})
	err := chromedp.Run(tab,
		emulation.SetUserAgentOverride(passportAgent),
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "https://*/obj/rc-client-security/web/stable/1.0.0.41/bdms.js*"}}),
		chromedp.Navigate(b.address),
		chromedp.Poll(`Boolean(window.bdms && window.__qishuiRequest)`, nil, chromedp.WithPollingTimeout(15*time.Second)),
	)
	if err != nil {
		end()
		return nil, nil, err
	}
	return tab, end, nil
}

type passportRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    *string           `json:"body"`
	Timeout int               `json:"timeout"`
}

type passportResponse struct {
	Status      int    `json:"status"`
	Body        string `json:"body"`
	ResponseURL string `json:"responseURL"`
	Headers     string `json:"headers"`
}

func (q *QRSession) browserRequest(ctx context.Context, input passportRequest, requireSignature bool) (passportResponse, []*http.Cookie, error) {
	params, err := json.Marshal(input)
	if err != nil {
		return passportResponse{}, nil, err
	}
	deviceToken, _ := json.Marshal(q.msToken)
	expression := `(async () => {
		localStorage.setItem('xmst', ` + string(deviceToken) + `);
		localStorage.setItem('xmsty', ` + string(deviceToken) + `);
		const reply = await window.__qishuiRequest(` + string(params) + `);
		if (reply.body && reply.body.length > 1048576) throw new Error('response too large');
		return reply;
	})()`
	request, cancel := context.WithCancel(q.tab)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	var response passportResponse
	var cookies []*http.Cookie
	err = chromedp.Run(request,
		chromedp.Evaluate(expression, &response, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
		chromedp.ActionFunc(func(ctx context.Context) error {
			values, err := storage.GetCookies().Do(ctx)
			for _, value := range values {
				host := strings.TrimPrefix(strings.ToLower(value.Domain), ".")
				if host == "qishui.com" || strings.HasSuffix(host, ".qishui.com") ||
					host == "douyin.com" || strings.HasSuffix(host, ".douyin.com") ||
					host == "zijieapi.com" || strings.HasSuffix(host, ".zijieapi.com") {
					cookies = append(cookies, &http.Cookie{Name: value.Name, Value: value.Value})
				}
			}
			return err
		}),
	)
	signed, parseErr := url.Parse(response.ResponseURL)
	if err == nil && requireSignature && (response.Status < 200 || response.Status >= 400 || parseErr != nil || signed.Scheme != "https" ||
		signed.Hostname() != "api.qishui.com" || len(signed.Query().Get("a_bogus")) != 44) {
		err = errors.New("Qishui security SDK did not return a valid signed response")
	}
	return response, cookies, err
}
