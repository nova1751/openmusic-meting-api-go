package tencent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"
	"github.com/gorilla/websocket"
)

type mqttSocket struct {
	*websocket.Conn
	reader io.Reader
}

func (s *mqttSocket) Read(dst []byte) (int, error) {
	for {
		if s.reader == nil {
			kind, reader, err := s.NextReader()
			if err != nil {
				return 0, err
			}
			if kind != websocket.BinaryMessage {
				return 0, errors.New("invalid QQ Music MQTT message")
			}
			s.reader = reader
		}
		n, err := s.reader.Read(dst)
		if err != io.EOF {
			return n, err
		}
		s.reader = nil
		if n > 0 {
			return n, nil
		}
	}
}

func (s *mqttSocket) Write(data []byte) (int, error) {
	if err := s.WriteMessage(websocket.BinaryMessage, data); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (s *mqttSocket) SetDeadline(t time.Time) error {
	if err := s.SetReadDeadline(t); err != nil {
		return err
	}
	return s.SetWriteDeadline(t)
}

type qrEvent struct {
	kind string
	body []byte
}

var mqttReference = regexp.MustCompile(`^[A-Za-z0-9:._-]{1,256}$`)

func (c *Client) listenQR(ctx context.Context, session *QRSession) {
	defer session.cancel()
	path := "/ws/handshake"
	for attempt := 0; attempt < 4; attempt++ {
		status, credential, reference := c.waitQR(ctx, session, path)
		if reference == "" || !mqttReference.MatchString(reference) {
			session.set(status, credential)
			return
		}
		path = "/ws/handshake/" + reference
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			session.set("expired", "")
			return
		}
	}
	session.set("error", "")
}

func (c *Client) waitQR(ctx context.Context, session *QRSession, path string) (status, credential, reference string) {
	connectCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	target := (&url.URL{Scheme: "wss", Host: "mu.y.qq.com", Path: path}).String()
	headers := http.Header{"Origin": {"https://y.qq.com"}, "Referer": {"https://y.qq.com/"},
		"User-Agent": {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/123.0.0.0 Safari/537.36"}}
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{"mqtt"}
	raw, response, err := dialer.DialContext(connectCtx, target, headers)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return "error", "", ""
	}
	defer raw.Close()
	raw.SetReadLimit(1 << 20)
	topic := "management.qrcode_login/" + session.id
	events := make(chan qrEvent, 8)
	client := paho.NewClient(paho.ClientConfig{
		Conn:          packets.NewThreadSafeConn(&mqttSocket{Conn: raw}),
		ClientID:      strconv.FormatInt(time.Now().UnixMilli(), 10) + strconv.FormatInt(1000+randomInt(9000), 10),
		PacketTimeout: 10 * time.Second,
		OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(in paho.PublishReceived) (bool, error) {
			p := in.Packet
			if p.Topic != topic || p.Properties == nil || len(p.Payload) > 1<<16 {
				return false, nil
			}
			event := qrEvent{kind: p.Properties.User.Get("type"), body: p.Payload}
			select {
			case events <- event:
			case <-ctx.Done():
			}
			return true, nil
		}},
	})
	maxPacket := uint32(1 << 20)
	ack, err := client.Connect(connectCtx, &paho.Connect{CleanStart: true, KeepAlive: 45, Properties: &paho.ConnectProperties{
		AuthMethod: "pass", MaximumPacketSize: &maxPacket, User: paho.UserProperties{
			{Key: "tmeAppID", Value: "qqmusic"}, {Key: "business", Value: "management"}, {Key: "hashTag", Value: session.id},
			{Key: "clientTag", Value: "management.user"}, {Key: "userID", Value: session.id},
		},
	}})
	if err != nil {
		if ack != nil && ack.Properties != nil && (ack.ReasonCode == 0x9C || ack.ReasonCode == 0x9D) {
			return "error", "", strings.Trim(ack.Properties.ServerReference, "/")
		}
		return "error", "", ""
	}
	sub, err := client.Subscribe(connectCtx, &paho.Subscribe{
		Subscriptions: []paho.SubscribeOptions{{Topic: topic}},
		Properties:    &paho.SubscribeProperties{User: paho.UserProperties{{Key: "authorization", Value: "tmelogin"}, {Key: "pubsub", Value: "unicast"}}},
	})
	if err != nil || len(sub.Reasons) != 1 || sub.Reasons[0] >= 128 {
		return "error", "", ""
	}
	for {
		select {
		case <-ctx.Done():
			return "expired", "", ""
		case <-client.Done():
			return "error", "", ""
		case event := <-events:
			switch event.kind {
			case "scanned":
				session.set("scanned", "")
			case "canceled", "loginFailed":
				return "error", "", ""
			case "timeout":
				return "expired", "", ""
			case "cookies":
				confirmCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
				cookie, err := c.confirmQR(confirmCtx, session.id, event.body)
				cancel()
				if err != nil {
					return "error", "", ""
				}
				return "confirmed", cookie, ""
			}
		}
	}
}
