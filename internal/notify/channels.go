package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// HTTP delivery shared by every webhook-style channel.
//
// All channels are the same shape underneath: POST a small JSON document to a
// robot webhook URL and treat a non-2xx status as a failure. The differences are
// the URL, the payload, and how the vendor reports an application-level error
// (several of them answer 200 OK with an error code in the body, which is why
// each channel has its own response check).

// defaultTimeout bounds a single delivery attempt. It is deliberately short:
// a notification is worth less than the panel's responsiveness, and a channel
// that is slow is a channel that is down.
const defaultTimeout = 10 * time.Second

// maxResponseBytes caps how much of a response body is read. Vendor error
// bodies are small; an unbounded read would let a misbehaving endpoint stream
// forever into the panel's memory.
const maxResponseBytes = 64 << 10

// httpDoer is the injectable HTTP surface. Every channel accepts one so tests
// can use httptest.Server and so the panel can plug in a client with custom
// transport settings.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// baseChannel holds the fields every webhook channel needs.
type baseChannel struct {
	name    string
	baseURL string // overridable for tests
	client  httpDoer
}

// Name implements Notifier.
func (c baseChannel) Name() string { return c.name }

// postJSON marshals body, POSTs it to target and returns the response body.
//
// Content-Type is set to application/json explicitly (several vendors reject a
// missing header), and the request carries the caller's context so a cancelled
// dispatch aborts an in-flight delivery.
func (c baseChannel) postJSON(ctx context.Context, target string, body any) ([]byte, error) {
	if target == "" {
		return nil, fmt.Errorf("%s: webhook URL is not configured", c.name)
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: encode payload: %w", c.name, err)
	}

	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", c.name, err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: post: %w", c.name, err)
	}
	defer resp.Body.Close()

	// The body is read even on success because vendors report application
	// errors there.
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return data, fmt.Errorf("%s: HTTP %d: %s", c.name, resp.StatusCode, truncate(string(data), 200))
	}
	if readErr != nil {
		// The delivery itself succeeded; a truncated body only affects the
		// application-level check, which the caller performs if it can.
		return data, nil
	}
	return data, nil
}

// truncate shortens s to at most n bytes for inclusion in an error message.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// orDefaultClient returns c when it is usable, otherwise a client with the
// default timeout.
//
// The nil check is on the *concrete* pointer, not only on the interface: a
// caller who writes `Client: cfg.Client` where cfg.Client is a typed nil
// (*http.Client)(nil) produces a non-nil interface holding a nil pointer, which
// panics inside net/http instead of falling back. The panel's settings struct
// has such an optional *http.Client field, so this is a live trap rather than a
// theoretical one.
//
// A type parameter is used so the compiler rejects calling this without a
// concrete client type, and the *http.Client case is checked explicitly.
func orDefaultClient[T httpDoer](c T) httpDoer {
	if any(c) == nil {
		return defaultHTTPClient
	}
	if client, ok := any(c).(*http.Client); ok && client == nil {
		return defaultHTTPClient
	}
	return c
}

// defaultHTTPClient is the shared fallback client.
//
// One shared instance is enough and is better than one per channel: *http.Client
// is safe for concurrent use and pools connections, and a zero Timeout would
// mean "no timeout at all" — exactly the failure this package exists to
// prevent.
var defaultHTTPClient httpDoer = &http.Client{Timeout: defaultTimeout}

// ---------------------------------------------------------------------------
// Generic webhook
// ---------------------------------------------------------------------------

// Webhook posts a JSON representation of the Notification to an arbitrary URL,
// optionally with custom method and headers. It is the escape hatch for
// anything the vendor-specific channels do not cover (§6.7 lists "自定义").
type Webhook struct {
	baseChannel

	method  string
	headers map[string]string
}

// WebhookConfig configures a Webhook.
type WebhookConfig struct {
	// Name identifies the channel; defaults to "webhook".
	Name string
	// URL is the destination. It is required at send time, not at build time,
	// so a half-configured panel can still start.
	URL string
	// Method defaults to POST. GET/PUT/PATCH are accepted.
	Method string
	// Headers are added to every request, e.g. an Authorization token.
	Headers map[string]string
	// Client is optional.
	Client *http.Client
}

// NewWebhook builds a generic webhook channel.
func NewWebhook(cfg WebhookConfig) *Webhook {
	method := strings.ToUpper(strings.TrimSpace(cfg.Method))
	if method == "" {
		method = http.MethodPost
	}
	headers := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	return &Webhook{
		baseChannel: baseChannel{
			name:    orDefault(cfg.Name, "webhook"),
			baseURL: cfg.URL,
			client:  orDefaultClient(cfg.Client),
		},
		method:  method,
		headers: headers,
	}
}

// Send implements Notifier.
func (w *Webhook) Send(ctx context.Context, n Notification) error {
	n = n.Normalize()

	// Checked here rather than at construction time so a half-configured panel
	// still starts; the failure is then reported at delivery, where the UI can
	// show it next to the channel.
	if w.baseURL == "" {
		return fmt.Errorf("%s: webhook URL is not configured", w.name)
	}

	payload, err := json.Marshal(toWireNotification(n))
	if err != nil {
		return fmt.Errorf("%s: encode payload: %w", w.name, err)
	}

	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, w.method, w.baseURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%s: build request: %w", w.name, err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range w.headers {
		req.Header.Set(k, v)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %s: %w", w.name, w.method, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s: HTTP %d: %s", w.name, resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}

// wireNotification is the stable JSON document a generic webhook receives. It
// is defined as its own type (rather than marshalling Notification directly) so
// that adding a field to Notification does not silently change the contract
// with every configured webhook.
type wireNotification struct {
	Event     string            `json:"event"`
	Title     string            `json:"title"`
	Body      string            `json:"body"`
	Level     string            `json:"level"`
	Instance  string            `json:"instance,omitempty"`
	Timestamp string            `json:"timestamp"`
	Fields    map[string]string `json:"fields,omitempty"`
	// Text is a convenience rendering for consumers (for example a generic
	// "post this string" bot) that do not want to assemble the parts.
	Text string `json:"text"`
}

func toWireNotification(n Notification) wireNotification {
	return wireNotification{
		Event:     n.Event,
		Title:     n.Title,
		Body:      n.Body,
		Level:     n.Level,
		Instance:  n.Instance,
		Timestamp: n.Timestamp.Format(time.RFC3339),
		Fields:    n.Fields,
		Text:      PlainText(n),
	}
}

// ---------------------------------------------------------------------------
// DingTalk (钉钉)
// ---------------------------------------------------------------------------

// DingTalk posts to a DingTalk custom robot webhook.
//
// Request shape:
//
//	POST https://oapi.dingtalk.com/robot/send?access_token=...
//	{"msgtype":"markdown","markdown":{"title":"...","text":"..."}}
//
// The access token is passed as the `access_token` query parameter, which is
// how DingTalk's robot URLs work. If the token is supplied as a raw token (not
// a URL) the channel appends it to the webhook base URL.
//
// Response shape: DingTalk answers 200 OK with {"errcode":0,"errmsg":"ok"} and
// reports failures (bad token, rate limit, keyword mismatch) as a non-zero
// errcode in a 200 response, so the body must be checked.
type DingTalk struct {
	baseChannel
	atMobiles []string
	atAll     bool
}

// DingTalkConfig configures a DingTalk channel.
type DingTalkConfig struct {
	// Name defaults to "dingtalk".
	Name string
	// URL is the full robot webhook, e.g.
	// https://oapi.dingtalk.com/robot/send?access_token=xxx
	URL string
	// AccessToken is appended as the access_token query parameter when URL has
	// none. Convenient because the settings UI asks for the token separately.
	AccessToken string
	// AtMobiles lists phone numbers to @-mention.
	AtMobiles []string
	// AtAll @-mentions everyone. Off by default: an @all on every crash notice
	// is how a notification channel gets muted.
	AtAll bool
	// Client is optional.
	Client *http.Client
}

// NewDingTalk builds a DingTalk channel.
func NewDingTalk(cfg DingTalkConfig) *DingTalk {
	return &DingTalk{
		baseChannel: baseChannel{
			name:    orDefault(cfg.Name, "dingtalk"),
			baseURL: withAccessToken(cfg.URL, cfg.AccessToken),
			client:  orDefaultClient(cfg.Client),
		},
		atMobiles: append([]string(nil), cfg.AtMobiles...),
		atAll:     cfg.AtAll,
	}
}

// Send implements Notifier.
func (d *DingTalk) Send(ctx context.Context, n Notification) error {
	body, err := d.postJSON(ctx, d.baseURL, dingtalkPayload(n, d.atMobiles, d.atAll))
	if err != nil {
		return err
	}
	return checkRobotResponse(d.name, body, "errcode", "errmsg")
}

// dingtalkPayload builds DingTalk's markdown message.
//
// NOTE: shape written from DingTalk's robot documentation, NOT verified against
// a live robot endpoint. Confirm before production use.
func dingtalkPayload(n Notification, atMobiles []string, atAll bool) map[string]any {
	n = n.Normalize()
	at := map[string]any{
		"isAtAll": atAll,
	}
	if len(atMobiles) > 0 {
		at["atMobiles"] = atMobiles
	}
	return map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]any{
			"title": n.Title,
			"text":  Markdown(n),
		},
		"at": at,
	}
}

// ---------------------------------------------------------------------------
// WeCom (企业微信)
// ---------------------------------------------------------------------------

// WeCom posts to a WeCom (WeChat Work) group robot webhook.
//
// Request shape:
//
//	POST https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...
//	{"msgtype":"markdown","markdown":{"content":"..."}}
//
// Like DingTalk it answers 200 OK with {"errcode":0,"errmsg":"ok"}.
//
// NOTE: shape written from WeCom's group-robot documentation, NOT verified
// against a live robot endpoint. Confirm before production use.
type WeCom struct {
	baseChannel
	mentionedList []string
	mentionedAll  bool
}

// WeComConfig configures a WeCom channel.
type WeComConfig struct {
	// Name defaults to "wecom".
	Name string
	// URL is the full robot webhook, e.g.
	// https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx
	URL string
	// Key is appended as the `key` query parameter when URL has none.
	Key string
	// MentionedList lists userids to @-mention.
	MentionedList []string
	// MentionedAll @-mentions everyone.
	MentionedAll bool
	// Client is optional.
	Client *http.Client
}

// NewWeCom builds a WeCom channel.
func NewWeCom(cfg WeComConfig) *WeCom {
	return &WeCom{
		baseChannel: baseChannel{
			name:    orDefault(cfg.Name, "wecom"),
			baseURL: withQueryParam(cfg.URL, "key", cfg.Key),
			client:  orDefaultClient(cfg.Client),
		},
		mentionedList: append([]string(nil), cfg.MentionedList...),
		mentionedAll:  cfg.MentionedAll,
	}
}

// Send implements Notifier.
func (w *WeCom) Send(ctx context.Context, n Notification) error {
	body, err := w.postJSON(ctx, w.baseURL, wecomPayload(n, w.mentionedList, w.mentionedAll))
	if err != nil {
		return err
	}
	return checkRobotResponse(w.name, body, "errcode", "errmsg")
}

// wecomPayload builds WeCom's markdown message.
//
// NOTE: shape written from WeCom's documentation, NOT verified against a live
// robot endpoint. Confirm before production use.
func wecomPayload(n Notification, mentionedList []string, mentionedAll bool) map[string]any {
	n = n.Normalize()
	// WeCom puts the markdown body under "content" (DingTalk uses "text").
	return map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]any{
			"content": Markdown(n),
		},
		"mentioned_list":        mentionedList,
		"mentioned_mobile_list": []string{},
		"mentioned_all":         mentionedAll,
	}
}

// ---------------------------------------------------------------------------
// Discord
// ---------------------------------------------------------------------------

// Discord posts to a Discord channel webhook.
//
// Request shape:
//
//	POST https://discord.com/api/webhooks/<id>/<token>
//	{"content":"...","username":"scnetm","embeds":[...]}
//
// Discord answers 204 No Content on success and a JSON {"message": "..."} with
// a 4xx status on failure, so the status code alone is the success signal.
//
// NOTE: shape written from Discord's webhook documentation, NOT verified
// against a live webhook. Confirm before production use.
type Discord struct {
	baseChannel
	username  string
	avatarURL string
}

// DiscordConfig configures a Discord channel.
type DiscordConfig struct {
	// Name defaults to "discord".
	Name string
	// URL is the channel webhook URL.
	URL string
	// Username overrides the webhook's display name.
	Username string
	// AvatarURL overrides the webhook's avatar.
	AvatarURL string
	// Client is optional.
	Client *http.Client
}

// NewDiscord builds a Discord channel.
func NewDiscord(cfg DiscordConfig) *Discord {
	return &Discord{
		baseChannel: baseChannel{
			name:    orDefault(cfg.Name, "discord"),
			baseURL: cfg.URL,
			client:  orDefaultClient(cfg.Client),
		},
		username:  cfg.Username,
		avatarURL: cfg.AvatarURL,
	}
}

// Send implements Notifier.
//
// Discord's own success status is 204, which the shared postJSON already treats
// as success (2xx), and its error body has no errcode field — so no additional
// application-level check is needed.
func (d *Discord) Send(ctx context.Context, n Notification) error {
	_, err := d.postJSON(ctx, d.baseURL, discordPayload(n, d.username, d.avatarURL))
	return err
}

// discordPayload builds Discord's webhook message.
//
// Discord caps `content` at 2000 characters; the body is trimmed so a long
// stack-trace-style message does not get rejected outright.
//
// NOTE: shape written from Discord's documentation, NOT verified against a live
// webhook. Confirm before production use.
func discordPayload(n Notification, username, avatarURL string) map[string]any {
	n = n.Normalize()

	payload := map[string]any{
		"content": truncate(PlainText(n), 1900),
	}
	if username != "" {
		payload["username"] = username
	}
	if avatarURL != "" {
		payload["avatar_url"] = avatarURL
	}
	return payload
}

// ---------------------------------------------------------------------------
// QQ Bot
// ---------------------------------------------------------------------------

// QQBot posts to a QQ group bot webhook (the "群机器人" webhook form of the QQ
// bot API).
//
// Request shape:
//
//	POST https://api.sgroup.qq.com/... (or a self-hosted bridge)
//	{"msg_type":0,"content":"..."}
//
// msg_type 0 is plain text in the QQ bot protocol. The panel posts a rendered
// plain-text message rather than attempting a markdown payload, because QQ's
// text channels do not render markdown.
//
// NOTE: QQ bot APIs differ between the official bot platform and the various
// community webhook bridges, and this shape was NOT verified against either.
// Confirm against the documentation of whichever endpoint is being deployed
// before production use; the URL is configurable precisely so a bridge can be
// substituted.
type QQBot struct {
	baseChannel
	msgType int
}

// QQBotConfig configures a QQ Bot channel.
type QQBotConfig struct {
	// Name defaults to "qqbot".
	Name string
	// URL is the bot/bridge endpoint.
	URL string
	// MsgType overrides the protocol message type; defaults to 0 (text).
	MsgType *int
	// Client is optional.
	Client *http.Client
}

// NewQQBot builds a QQ Bot channel.
func NewQQBot(cfg QQBotConfig) *QQBot {
	msgType := 0
	if cfg.MsgType != nil {
		msgType = *cfg.MsgType
	}
	return &QQBot{
		baseChannel: baseChannel{
			name:    orDefault(cfg.Name, "qqbot"),
			baseURL: cfg.URL,
			client:  orDefaultClient(cfg.Client),
		},
		msgType: msgType,
	}
}

// Send implements Notifier.
func (q *QQBot) Send(ctx context.Context, n Notification) error {
	_, err := q.postJSON(ctx, q.baseURL, qqbotPayload(n, q.msgType))
	return err
}

// qqbotPayload builds the QQ bot text message.
//
// NOTE: shape NOT verified against a live QQ bot endpoint. Confirm before
// production use.
func qqbotPayload(n Notification, msgType int) map[string]any {
	n = n.Normalize()
	return map[string]any{
		"msg_type":  msgType,
		"content":   truncate(PlainText(n), 1900),
		"msg_id":    "",
		"timestamp": n.Timestamp.Unix(),
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// withAccessToken returns rawURL with token appended as the access_token query
// parameter, unless rawURL already carries one or token is empty.
//
// A malformed URL is returned unchanged: the delivery will then fail with a
// clear URL error rather than the panel panicking at construction time.
func withAccessToken(rawURL, token string) string {
	return withQueryParam(rawURL, "access_token", token)
}

func withQueryParam(rawURL, key, value string) string {
	if rawURL == "" || strings.TrimSpace(value) == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	if q.Get(key) != "" {
		return rawURL // caller already embedded it
	}
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String()
}

// checkRobotResponse inspects a vendor JSON body for an application-level
// failure reported alongside HTTP 200.
//
// Several Chinese robot webhooks use {"errcode":0,"errmsg":"ok"} on success and
// a non-zero errcode with HTTP 200 on failure, so a status-code check alone
// would report a dropped notification as delivered. An unparsable body is
// treated as success: the transport-level check already passed, and failing a
// notification because a vendor changed its response shape would be worse than
// reporting it delivered.
func checkRobotResponse(name string, body []byte, codeField, msgField string) error {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(trimmed), &resp); err != nil {
		return nil
	}

	code, ok := numericField(resp, codeField)
	if !ok {
		return nil
	}
	if code == 0 {
		return nil
	}

	msg, _ := resp[msgField].(string)
	if msg == "" {
		msg = truncate(trimmed, 200)
	}
	return fmt.Errorf("%s: %s=%d: %s", name, codeField, code, msg)
}

// numericField reads a JSON number that may have been decoded as float64 or
// json.Number.
func numericField(m map[string]any, key string) (int64, bool) {
	switch v := m[key].(type) {
	case float64:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	case int64:
		return v, true
	case int:
		return int64(v), true
	default:
		return 0, false
	}
}

// PlainText renders a Notification as a compact multi-line string. It is the
// shared body renderer for the plain-text channels (Discord content, QQ Bot)
// and for the generic webhook's "text" convenience field.
func PlainText(n Notification) string {
	n = n.Normalize()

	var b strings.Builder
	b.WriteString(icon(n.Level))
	b.WriteByte(' ')
	b.WriteString(n.Title)

	if n.Instance != "" {
		b.WriteString(" [")
		b.WriteString(n.Instance)
		b.WriteByte(']')
	}
	if n.Body != "" {
		b.WriteString("\n")
		b.WriteString(n.Body)
	}
	writeFields(&b, n.Fields)
	b.WriteString("\n")
	b.WriteString(n.Timestamp.Format(time.RFC3339))

	return b.String()
}

// Markdown renders a Notification as markdown, for DingTalk and WeCom.
//
// The two vendors' markdown dialects differ slightly (WeCom supports a subset
// and rejects some HTML), so this deliberately uses only headings, bold and
// bullet lists, which both accept.
func Markdown(n Notification) string {
	n = n.Normalize()

	var b strings.Builder
	b.WriteString("### ")
	b.WriteString(icon(n.Level))
	b.WriteByte(' ')
	b.WriteString(n.Title)

	if n.Instance != "" {
		b.WriteString(" [")
		b.WriteString(n.Instance)
		b.WriteByte(']')
	}
	if n.Body != "" {
		b.WriteString("\n\n")
		b.WriteString(n.Body)
	}
	writeFields(&b, n.Fields)

	b.WriteString("\n\n> ")
	b.WriteString(n.Event)
	b.WriteString(" · ")
	b.WriteString(n.Timestamp.Format(time.RFC3339))

	return b.String()
}

// writeFields appends the Fields map as a deterministic markdown bullet list.
//
// Sorted keys matter: a Go map iterates randomly, and a message whose lines
// shuffle between deliveries is needlessly hard to read (and would make payload
// assertions in tests flaky).
func writeFields(b *strings.Builder, fields map[string]string) {
	if len(fields) == 0 {
		return
	}

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	b.WriteString("\n")
	for _, k := range keys {
		b.WriteString("\n- **")
		b.WriteString(k)
		b.WriteString("**: ")
		b.WriteString(fields[k])
	}
}

// icon returns a short textual severity marker.
//
// Deliberately text, not emoji: the plan's UI language is Chinese and the
// destinations include terminals and log files where emoji render poorly or
// break alignment.
func icon(level string) string {
	switch level {
	case LevelError:
		return "[ERROR]"
	case LevelWarn:
		return "[WARN]"
	default:
		return "[INFO]"
	}
}
