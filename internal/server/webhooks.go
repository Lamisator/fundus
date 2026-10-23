package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Lamisator/fundus/internal/store"
)

// dispatcher delivers webhook events asynchronously. Each request carries
//
//	X-Fundus-Event:     the event name
//	X-Fundus-Signature: sha256=<hex HMAC of the body with the hook secret>
type dispatcher struct {
	st     *store.Store
	client *http.Client
	mu     sync.Mutex
	hooks  []store.Webhook
	loaded bool
	queue  chan job
}

type job struct {
	hook  store.Webhook
	event string
	data  any
}

func newDispatcher(st *store.Store) *dispatcher {
	d := &dispatcher{st: st, client: &http.Client{Timeout: 10 * time.Second}, queue: make(chan job, 256)}
	for i := 0; i < 2; i++ {
		go func() {
			for j := range d.queue {
				d.deliver(context.Background(), j.hook, j.event, j.data)
			}
		}()
	}
	return d
}

func (d *dispatcher) reload() {
	d.mu.Lock()
	d.loaded = false
	d.mu.Unlock()
}

func (d *dispatcher) active() []store.Webhook {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.loaded {
		list, err := d.st.ListWebhooks(context.Background())
		if err != nil {
			return nil
		}
		d.hooks, d.loaded = list, true
	}
	return d.hooks
}

func (d *dispatcher) emit(event string, data any) {
	for _, h := range d.active() {
		if !h.Active || !wants(h.Events, event) {
			continue
		}
		select {
		case d.queue <- job{h, event, data}:
		default:
			slog.Warn("webhook queue full, dropping event", "event", event, "url", h.URL)
		}
	}
}

func wants(events []string, ev string) bool {
	for _, e := range events {
		if e == "*" || e == ev {
			return true
		}
	}
	return false
}

// deliver POSTs one event, retrying twice on failure, and records the outcome.
func (d *dispatcher) deliver(ctx context.Context, h store.Webhook, event string, data any) string {
	body, _ := json.Marshal(map[string]any{"event": event, "at": time.Now().UTC(), "data": data})
	mac := hmac.New(sha256.New, []byte(h.Secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	status := ""
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * 2 * time.Second)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
		if err != nil {
			status = "error: " + err.Error()
			break
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Fundus-Webhook/"+Version)
		req.Header.Set("X-Fundus-Event", event)
		req.Header.Set("X-Fundus-Signature", sig)
		resp, err := d.client.Do(req)
		if err != nil {
			status = "error: " + err.Error()
			continue
		}
		resp.Body.Close()
		status = strconv.Itoa(resp.StatusCode)
		if resp.StatusCode < 500 {
			break
		}
	}
	if len(status) > 200 {
		status = status[:200]
	}
	d.st.WebhookResult(context.Background(), h.ID, fmt.Sprintf("%s %s", event, status))
	return status
}
