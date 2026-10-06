package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureServer is an httptest server that records every request it receives.
type captureServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests []capturedRequest
}

type capturedRequest struct {
	Method  string
	Path    string
	Query   string
	Headers http.Header
	Body    []byte
}

func (c *captureServer) add(r capturedRequest) {
	c.mu.Lock()
	c.requests = append(c.requests, r)
	c.mu.Unlock()
}

// last returns the most recent request, failing the test when none arrived.
func (c *captureServer) last(t *testing.T) capturedRequest {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		t.Fatal("no request was received")
	}
	return c.requests[len(c.requests)-1]
}

// count returns how many requests arrived.
func (c *captureServer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// newCaptureServer starts a recording server that answers with status and body.
func newCaptureServer(t *testing.T, status int, body string) *captureServer {
	t.Helper()

	cs := &captureServer{}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		cs.add(capturedRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Query:   r.URL.RawQuery,
			Headers: r.Header.Clone(),
			Body:    data,
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(cs.Close)
	return cs
}

// decodeBody unmarshals the last request body into a generic map.
func decodeBody(t *testing.T, req capturedRequest) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(req.Body, &m); err != nil {
		t.Fatalf("request body is not valid JSON from a map: %v\n%s", err, req.Body)
	}
	return m
}

func sampleNotification() Notification {
	return Notification{
		Event:     EventInstanceCrashed,
		Title:     "Instance crashed",
		Body:      "process exited with code 139",
		Level:     LevelError,
		Instance:  "survival-1",
		Timestamp: time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC),
		Fields:    map[string]string{"exitCode": "139", "restarts": "2"},
	}
}

// ---------------------------------------------------------------------------
// Channel payloads
// ---------------------------------------------------------------------------

// TestWebhookPayload asserts the exact generic-webhook document, including that
// every field survives and that the "text" convenience rendering is present.
func TestWebhookPayload(t *testing.T) {
	t.Parallel()

	srv := newCaptureServer(t, http.StatusOK, `{"ok":true}`)
	hook := NewWebhook(WebhookConfig{
		URL:     srv.URL + "/hook",
		Headers: map[string]string{"Authorization": "Bearer tok", "X-Panel": "scnetm"},
	})

	if err := hook.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	req := srv.last(t)
	if req.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if req.Path != "/hook" {
		t.Errorf("path = %q, want /hook", req.Path)
	}
	if got := req.Headers.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := req.Headers.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization header = %q, want the configured value", got)
	}
	if got := req.Headers.Get("X-Panel"); got != "scnetm" {
		t.Errorf("X-Panel header = %q", got)
	}

	body := decodeBody(t, req)
	want := map[string]any{
		"event":     EventInstanceCrashed,
		"title":     "Instance crashed",
		"body":      "process exited with code 139",
		"level":     LevelError,
		"instance":  "survival-1",
		"timestamp": "2026-04-05T06:07:08Z",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%q] = %v, want %v", k, body[k], v)
		}
	}
	fields, ok := body["fields"].(map[string]any)
	if !ok {
		t.Fatalf("fields = %#v, want an object", body["fields"])
	}
	if fields["exitCode"] != "139" {
		t.Errorf("fields.exitCode = %v, want 139", fields["exitCode"])
	}
	text, _ := body["text"].(string)
	if !strings.Contains(text, "Instance crashed") || !strings.Contains(text, "survival-1") {
		t.Errorf("text = %q, want it to mention the title and instance", text)
	}
	if !strings.Contains(text, "exitCode") {
		t.Errorf("text = %q, want it to include the fields", text)
	}
}

func TestWebhookCustomMethod(t *testing.T) {
	t.Parallel()

	srv := newCaptureServer(t, http.StatusOK, `{}`)
	hook := NewWebhook(WebhookConfig{URL: srv.URL, Method: "put"})

	if err := hook.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := srv.last(t).Method; got != http.MethodPut {
		t.Errorf("method = %q, want PUT", got)
	}
}

// TestDingTalkPayload asserts DingTalk's exact markdown document shape.
func TestDingTalkPayload(t *testing.T) {
	t.Parallel()

	srv := newCaptureServer(t, http.StatusOK, `{"errcode":0,"errmsg":"ok"}`)
	dt := NewDingTalk(DingTalkConfig{
		URL:       srv.URL + "/robot/send",
		AtMobiles: []string{"13800000000"},
	})

	if err := dt.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	req := srv.last(t)
	body := decodeBody(t, req)

	if body["msgtype"] != "markdown" {
		t.Errorf("msgtype = %v, want markdown", body["msgtype"])
	}

	md, ok := body["markdown"].(map[string]any)
	if !ok {
		t.Fatalf("markdown = %#v, want an object", body["markdown"])
	}
	if md["title"] != "Instance crashed" {
		t.Errorf("markdown.title = %v, want the notification title", md["title"])
	}
	text, _ := md["text"].(string)
	if !strings.Contains(text, "Instance crashed") {
		t.Errorf("markdown.text = %q, want it to contain the title", text)
	}
	if !strings.Contains(text, "###") {
		t.Errorf("markdown.text = %q, want markdown heading syntax", text)
	}

	at, ok := body["at"].(map[string]any)
	if !ok {
		t.Fatalf("at = %#v, want an object", body["at"])
	}
	if at["isAtAll"] != false {
		t.Errorf("at.isAtAll = %v, want false by default", at["isAtAll"])
	}
	mobiles, _ := at["atMobiles"].([]any)
	if len(mobiles) != 1 || mobiles[0] != "13800000000" {
		t.Errorf("at.atMobiles = %#v, want the configured number", at["atMobiles"])
	}
}

// TestDingTalkAccessTokenInQuery pins the token plumbing: the settings UI
// collects the token separately from the URL.
func TestDingTalkAccessTokenInQuery(t *testing.T) {
	t.Parallel()

	srv := newCaptureServer(t, http.StatusOK, `{"errcode":0,"errmsg":"ok"}`)
	dt := NewDingTalk(DingTalkConfig{URL: srv.URL + "/robot/send", AccessToken: "tok123"})

	if err := dt.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := srv.last(t).Query; got != "access_token=tok123" {
		t.Errorf("query = %q, want access_token=tok123", got)
	}
}

func TestDingTalkKeepsTokenAlreadyInURL(t *testing.T) {
	t.Parallel()

	srv := newCaptureServer(t, http.StatusOK, `{"errcode":0,"errmsg":"ok"}`)
	// The URL already carries a token; the separately configured one must not
	// overwrite it.
	dt := NewDingTalk(DingTalkConfig{
		URL:         srv.URL + "/robot/send?access_token=fromurl",
		AccessToken: "fromfield",
	})

	if err := dt.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := srv.last(t).Query; got != "access_token=fromurl" {
		t.Errorf("query = %q, want the URL's own token to win", got)
	}
}

// TestDingTalkApplicationError covers the vendor habit of answering HTTP 200
// with an error code: that must be reported as a failure.
func TestDingTalkApplicationError(t *testing.T) {
	t.Parallel()

	srv := newCaptureServer(t, http.StatusOK, `{"errcode":310000,"errmsg":"keywords not in content"}`)
	dt := NewDingTalk(DingTalkConfig{URL: srv.URL})

	err := dt.Send(context.Background(), sampleNotification())
	if err == nil {
		t.Fatal("a non-zero errcode in a 200 response must be an error")
	}
	for _, want := range []string{"310000", "keywords not in content"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

// TestWeComPayload asserts WeCom's shape, which differs from DingTalk in
// putting the body under "content".
func TestWeComPayload(t *testing.T) {
	t.Parallel()

	srv := newCaptureServer(t, http.StatusOK, `{"errcode":0,"errmsg":"ok"}`)
	wc := NewWeCom(WeComConfig{
		URL:           srv.URL + "/cgi-bin/webhook/send",
		Key:           "key-abc",
		MentionedList: []string{"@all"},
	})

	if err := wc.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	req := srv.last(t)
	if !strings.Contains(req.Query, "key=key-abc") {
		t.Errorf("query = %q, want it to carry key=key-abc", req.Query)
	}

	body := decodeBody(t, req)
	if body["msgtype"] != "markdown" {
		t.Errorf("msgtype = %v, want markdown", body["msgtype"])
	}
	md, ok := body["markdown"].(map[string]any)
	if !ok {
		t.Fatalf("markdown = %#v, want an object", body["markdown"])
	}
	content, ok := md["content"].(string)
	if !ok {
		t.Fatalf("markdown.content = %#v, want a string (WeCom uses content, not text)", md["content"])
	}
	if !strings.Contains(content, "Instance crashed") {
		t.Errorf("markdown.content = %q, want the title", content)
	}
	// WeCom must not receive DingTalk's "title" key.
	if _, present := md["title"]; present {
		t.Error("WeCom markdown must not carry a title field (DingTalk only)")
	}
	mentioned, _ := body["mentioned_list"].([]any)
	if len(mentioned) != 1 || mentioned[0] != "@all" {
		t.Errorf("mentioned_list = %#v", body["mentioned_list"])
	}
}

func TestWeComApplicationError(t *testing.T) {
	t.Parallel()

	srv := newCaptureServer(t, http.StatusOK, `{"errcode":93000,"errmsg":"invalid webhook key"}`)
	wc := NewWeCom(WeComConfig{URL: srv.URL})

	err := wc.Send(context.Background(), sampleNotification())
	if err == nil {
		t.Fatal("a non-zero errcode must be reported")
	}
	if !strings.Contains(err.Error(), "invalid webhook key") {
		t.Errorf("error = %q, want the vendor message", err)
	}
}

// TestDiscordPayload asserts Discord's shape: a "content" string, plus the
// optional username/avatar overrides.
func TestDiscordPayload(t *testing.T) {
	t.Parallel()

	srv := newCaptureServer(t, http.StatusNoContent, "")
	dc := NewDiscord(DiscordConfig{
		URL:       srv.URL + "/api/webhooks/1/tok",
		Username:  "scnetm",
		AvatarURL: "https://example.invalid/a.png",
	})

	if err := dc.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Send (204 must be success): %v", err)
	}

	req := srv.last(t)
	if req.Path != "/api/webhooks/1/tok" {
		t.Errorf("path = %q", req.Path)
	}

	body := decodeBody(t, req)
	content, ok := body["content"].(string)
	if !ok {
		t.Fatalf("content = %#v, want a string", body["content"])
	}
	if !strings.Contains(content, "Instance crashed") || !strings.Contains(content, "survival-1") {
		t.Errorf("content = %q, want title and instance", content)
	}
	if body["username"] != "scnetm" {
		t.Errorf("username = %v, want scnetm", body["username"])
	}
	if body["avatar_url"] != "https://example.invalid/a.png" {
		t.Errorf("avatar_url = %v", body["avatar_url"])
	}
	// Discord does not use the DingTalk/WeCom envelope.
	if _, present := body["msgtype"]; present {
		t.Error("Discord payload must not carry a msgtype field")
	}
}

// TestDiscordTruncatesLongContent guards Discord's 2000-character content cap.
func TestDiscordTruncatesLongContent(t *testing.T) {
	t.Parallel()

	srv := newCaptureServer(t, http.StatusNoContent, "")
	dc := NewDiscord(DiscordConfig{URL: srv.URL})

	n := sampleNotification()
	n.Body = strings.Repeat("x", 5000)

	if err := dc.Send(context.Background(), n); err != nil {
		t.Fatalf("Send: %v", err)
	}
	body := decodeBody(t, srv.last(t))
	content, _ := body["content"].(string)
	if len(content) > 2000 {
		t.Errorf("content length = %d, want <= 2000 (Discord's cap)", len(content))
	}
}

func TestQQBotPayload(t *testing.T) {
	t.Parallel()

	srv := newCaptureServer(t, http.StatusOK, `{"code":0}`)
	qq := NewQQBot(QQBotConfig{URL: srv.URL + "/v2/groups/1/messages"})

	if err := qq.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	body := decodeBody(t, srv.last(t))
	if body["msg_type"] != float64(0) {
		t.Errorf("msg_type = %v, want 0 (text)", body["msg_type"])
	}
	content, _ := body["content"].(string)
	if !strings.Contains(content, "Instance crashed") {
		t.Errorf("content = %q, want the title", content)
	}
	if body["timestamp"] != float64(1775369228) { // 2026-04-05T06:07:08Z
		t.Errorf("timestamp = %v, want the unix seconds of the notification", body["timestamp"])
	}
}

// TestChannelHTTPErrorIsFailure checks the shared transport check: any non-2xx
// is an error naming the status and a snippet of the body.
func TestChannelHTTPErrorIsFailure(t *testing.T) {
	t.Parallel()

	srv := newCaptureServer(t, http.StatusInternalServerError, "upstream exploded")

	channels := map[string]Notifier{
		"webhook":  NewWebhook(WebhookConfig{URL: srv.URL}),
		"dingtalk": NewDingTalk(DingTalkConfig{URL: srv.URL}),
		"wecom":    NewWeCom(WeComConfig{URL: srv.URL}),
		"discord":  NewDiscord(DiscordConfig{URL: srv.URL}),
		"qqbot":    NewQQBot(QQBotConfig{URL: srv.URL}),
	}

	for name, ch := range channels {
		t.Run(name, func(t *testing.T) {
			err := ch.Send(context.Background(), sampleNotification())
			if err == nil {
				t.Fatal("a 500 response must be an error")
			}
			if !strings.Contains(err.Error(), "500") {
				t.Errorf("error = %q, want it to include the status code", err)
			}
			if !strings.Contains(err.Error(), "upstream exploded") {
				t.Errorf("error = %q, want it to include the response snippet", err)
			}
		})
	}
}

func TestChannelUnconfiguredURL(t *testing.T) {
	t.Parallel()

	channels := map[string]Notifier{
		"webhook":  NewWebhook(WebhookConfig{}),
		"dingtalk": NewDingTalk(DingTalkConfig{}),
		"wecom":    NewWeCom(WeComConfig{}),
		"discord":  NewDiscord(DiscordConfig{}),
		"qqbot":    NewQQBot(QQBotConfig{}),
	}

	for name, ch := range channels {
		t.Run(name, func(t *testing.T) {
			err := ch.Send(context.Background(), sampleNotification())
			if err == nil {
				t.Fatal("an unconfigured URL must be a send-time error, not a panic")
			}
			if !strings.Contains(err.Error(), "not configured") {
				t.Errorf("error = %q, want it to say the URL is not configured", err)
			}
		})
	}
}

func TestChannelNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		got  string
		want string
	}{
		{NewWebhook(WebhookConfig{}).Name(), "webhook"},
		{NewDingTalk(DingTalkConfig{}).Name(), "dingtalk"},
		{NewWeCom(WeComConfig{}).Name(), "wecom"},
		{NewDiscord(DiscordConfig{}).Name(), "discord"},
		{NewQQBot(QQBotConfig{}).Name(), "qqbot"},
		{NewWebhook(WebhookConfig{Name: "ops-hook"}).Name(), "ops-hook"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("Name() = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestChannelContextCancellation(t *testing.T) {
	t.Parallel()

	srv := newCaptureServer(t, http.StatusOK, `{}`)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := NewWebhook(WebhookConfig{URL: srv.URL}).Send(ctx, sampleNotification()); err == nil {
		t.Fatal("Send with a cancelled context must fail")
	}
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func TestRenderers(t *testing.T) {
	t.Parallel()

	n := sampleNotification()

	plain := PlainText(n)
	for _, want := range []string{"[ERROR]", "Instance crashed", "survival-1",
		"exitCode", "139", "restarts", "2026-04-05T06:07:08Z"} {
		if !strings.Contains(plain, want) {
			t.Errorf("PlainText is missing %q:\n%s", want, plain)
		}
	}

	md := Markdown(n)
	for _, want := range []string{"### ", "**exitCode**", "instance.crashed", "> "} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown is missing %q:\n%s", want, md)
		}
	}
}

// TestRenderersAreDeterministic pins the sorted field order: a Go map iterates
// randomly, so unsorted rendering would shuffle message lines between
// deliveries.
func TestRenderersAreDeterministic(t *testing.T) {
	t.Parallel()

	n := Notification{
		Event: EventDiskLow, Title: "Disk low", Level: LevelWarn,
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Fields: map[string]string{
			"zeta": "1", "alpha": "2", "mid": "3", "beta": "4", "gamma": "5",
		},
	}

	first := Markdown(n)
	for i := 0; i < 50; i++ {
		if got := Markdown(n); got != first {
			t.Fatalf("Markdown is not deterministic:\n%s\n---\n%s", first, got)
		}
	}

	idxAlpha := strings.Index(first, "**alpha**")
	idxZeta := strings.Index(first, "**zeta**")
	if idxAlpha < 0 || idxZeta < 0 || idxAlpha > idxZeta {
		t.Errorf("fields should be sorted alphabetically:\n%s", first)
	}
}

func TestNormalizeFillsDefaults(t *testing.T) {
	t.Parallel()

	n := Notification{Event: EventBackupFailed}.Normalize()
	if n.Level != LevelInfo {
		t.Errorf("Level = %q, want the info default", n.Level)
	}
	if n.Title != EventBackupFailed {
		t.Errorf("Title = %q, want the event name as a fallback", n.Title)
	}
	if n.Timestamp.IsZero() {
		t.Error("Timestamp must default to now")
	}
	if n.Timestamp.Location() != time.UTC {
		t.Errorf("Timestamp zone = %v, want UTC", n.Timestamp.Location())
	}
}

func TestEventsCatalog(t *testing.T) {
	t.Parallel()

	want := []string{
		"instance.started", "instance.stopped", "instance.crashed",
		"instance.restart.failed", "backup.completed", "backup.failed",
		"disk.low", "server.ready",
	}
	got := AllEvents()
	if len(got) != len(want) {
		t.Fatalf("AllEvents() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AllEvents()[%d] = %q, want %q", i, got[i], want[i])
		}
		if !ValidEvent(got[i]) {
			t.Errorf("ValidEvent(%q) = false", got[i])
		}
	}
	if ValidEvent("bogus.event") {
		t.Error("ValidEvent must reject an unknown event")
	}
	if ValidEvent("") {
		t.Error("ValidEvent must reject the empty event")
	}
}

func TestNotificationJSONShape(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(sampleNotification())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{`"event"`, `"title"`, `"body"`, `"level"`, `"instance"`, `"timestamp"`, `"fields"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("JSON %s is missing %s", data, key)
		}
	}
}
