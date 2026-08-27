package cloudflarecompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
)

type turnCredentials struct {
	URLs       []string
	Username   string
	Credential string
}

func measureTURNLoopback(ctx context.Context, client *http.Client, o options) packetSummary {
	creds, err := resolveTURNCredentials(ctx, client, o)
	if err != nil {
		return unavailablePacket(err.Error())
	}
	urls := make([]string, 0, len(creds.URLs))
	for _, raw := range creds.URLs {
		if strings.HasPrefix(strings.ToLower(raw), "turn:") {
			u := raw
			if !strings.Contains(strings.ToLower(u), "transport=") {
				sep := "?"
				if strings.Contains(u, "?") {
					sep = "&"
				}
				u += sep + "transport=udp"
			}
			if strings.Contains(strings.ToLower(u), "transport=udp") {
				urls = append(urls, u)
			}
		}
	}
	if len(urls) == 0 {
		return unavailablePacket("TURN credentials contain no UDP relay URL")
	}
	cfg := webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: urls, Username: creds.Username, Credential: creds.Credential, CredentialType: webrtc.ICECredentialTypePassword}}, ICETransportPolicy: webrtc.ICETransportPolicyRelay}
	sender, err := webrtc.NewPeerConnection(cfg)
	if err != nil {
		return unavailablePacket(err.Error())
	}
	defer sender.Close()
	receiver, err := webrtc.NewPeerConnection(cfg)
	if err != nil {
		return unavailablePacket(err.Error())
	}
	defer receiver.Close()
	ordered := false
	retries := uint16(0)
	recvReady := make(chan struct{})
	open := make(chan struct{})
	var openOnce sync.Once
	const count = 1000
	received := make([]bool, count+1)
	var mu sync.Mutex
	receiver.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnOpen(func() { openOnce.Do(func() { close(recvReady) }) })
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			n, e := strconv.Atoi(string(m.Data))
			if e == nil && n >= 1 && n <= count {
				mu.Lock()
				received[n] = true
				mu.Unlock()
			}
		})
	})
	dc, err := sender.CreateDataChannel("channel", &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &retries})
	if err != nil {
		return unavailablePacket(err.Error())
	}
	dc.OnOpen(func() { close(open) })
	if err = exchangeDescriptions(sender, receiver); err != nil {
		return unavailablePacket(err.Error())
	}
	select {
	case <-open:
	case <-ctx.Done():
		return unavailablePacket(ctx.Err().Error())
	case <-time.After(12 * time.Second):
		return unavailablePacket("TURN loopback sender channel timeout")
	}
	select {
	case <-recvReady:
	case <-ctx.Done():
		return unavailablePacket(ctx.Err().Error())
	case <-time.After(5 * time.Second):
		return unavailablePacket("TURN loopback receiver channel timeout")
	}
	for n := 1; n <= count; n++ {
		if err := dc.SendText(strconv.Itoa(n)); err != nil {
			return unavailablePacket(err.Error())
		}
		if n%10 == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	quiet := time.NewTimer(3 * time.Second)
	defer quiet.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return unavailablePacket(ctx.Err().Error())
		case <-quiet.C:
			goto done
		case <-ticker.C:
			mu.Lock()
			got := 0
			for i := 1; i <= count; i++ {
				if received[i] {
					got++
				}
			}
			mu.Unlock()
			if got == count {
				goto done
			}
		}
	}
done:
	mu.Lock()
	got := 0
	for i := 1; i <= count; i++ {
		if received[i] {
			got++
		}
	}
	mu.Unlock()
	lost := count - got
	loss := float64(lost) * 100 / float64(count)
	return packetSummary{Available: true, Transport: "webrtc-datachannel-turn-udp", Topology: "turn-loopback", Protocol: "cloudflare-loopback-v1", Sent: count, Received: got, Lost: lost, TransactionLossPercent: &loss}
}

func exchangeDescriptions(sender, receiver *webrtc.PeerConnection) error {
	offer, err := sender.CreateOffer(nil)
	if err != nil {
		return err
	}
	g := webrtc.GatheringCompletePromise(sender)
	if err = sender.SetLocalDescription(offer); err != nil {
		return err
	}
	<-g
	if sender.LocalDescription() == nil {
		return errors.New("sender has no local description")
	}
	if err = receiver.SetRemoteDescription(*sender.LocalDescription()); err != nil {
		return err
	}
	answer, err := receiver.CreateAnswer(nil)
	if err != nil {
		return err
	}
	g = webrtc.GatheringCompletePromise(receiver)
	if err = receiver.SetLocalDescription(answer); err != nil {
		return err
	}
	<-g
	if receiver.LocalDescription() == nil {
		return errors.New("receiver has no local description")
	}
	return sender.SetRemoteDescription(*receiver.LocalDescription())
}

func resolveTURNCredentials(ctx context.Context, client *http.Client, o options) (turnCredentials, error) {
	if o.TurnURL != "" && o.TurnUsername != "" && o.TurnCredential != "" {
		return turnCredentials{URLs: []string{o.TurnURL}, Username: o.TurnUsername, Credential: o.TurnCredential}, nil
	}
	candidates := []string{}
	if o.TurnCredentialsURL != "" {
		candidates = append(candidates, o.TurnCredentialsURL)
	} else {
		for _, p := range []string{"/api/turn/credentials", "/turn-credentials"} {
			if raw, e := endpoint(o.Server, p, nil); e == nil {
				candidates = append(candidates, raw)
			}
		}
	}
	var errs []string
	for _, raw := range candidates {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if e != nil {
			continue
		}
		if o.Token != "" {
			req.Header.Set("Authorization", "Bearer "+o.Token)
		}
		resp, e := client.Do(req)
		if e != nil {
			errs = append(errs, e.Error())
			continue
		}
		body, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if e != nil || resp.StatusCode/100 != 2 {
			errs = append(errs, fmt.Sprintf("%s returned HTTP %d", raw, resp.StatusCode))
			continue
		}
		var v any
		if e = json.Unmarshal(body, &v); e != nil {
			errs = append(errs, e.Error())
			continue
		}
		c := parseCredentialValue(v)
		if len(c.URLs) > 0 && c.Username != "" && c.Credential != "" {
			return c, nil
		}
		errs = append(errs, "credential response missing urls, username, or credential")
	}
	if len(errs) == 0 {
		return turnCredentials{}, errors.New("no TURN credentials configured; use --turn-credentials-url or --turn-url/--turn-username/--turn-credential")
	}
	return turnCredentials{}, errors.New(strings.Join(errs, "; "))
}

func parseCredentialValue(v any) turnCredentials {
	c := turnCredentials{}
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			if s, ok := t["username"].(string); ok && c.Username == "" {
				c.Username = s
			}
			if s, ok := t["credential"].(string); ok && c.Credential == "" {
				c.Credential = s
			}
			if s, ok := t["password"].(string); ok && c.Credential == "" {
				c.Credential = s
			}
			for _, k := range []string{"urls", "servers"} {
				if z, ok := t[k]; ok {
					walk(z)
				}
			}
			if s, ok := t["server"].(string); ok {
				if !strings.Contains(s, "://") && !strings.HasPrefix(s, "turn:") {
					s = "turn:" + s
				}
				c.URLs = append(c.URLs, s)
			}
			if z, ok := t["iceServers"]; ok {
				walk(z)
			}
		case []any:
			for _, z := range t {
				walk(z)
			}
		case string:
			if u, e := url.Parse(t); e == nil && (u.Scheme == "turn" || u.Scheme == "turns") {
				c.URLs = append(c.URLs, t)
			}
		}
	}
	walk(v)
	c.URLs = uniqueStrings(c.URLs)
	return c
}
func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
