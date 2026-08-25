// Package ari implements a minimal client for the Asterisk REST Interface.
//
// ARI has two halves:
//   - a REST API used to query and manipulate channels, bridges, endpoints,
//     device state, etc. (request/response over HTTP); and
//   - a WebSocket that streams asynchronous Stasis events (channel created,
//     dialed, hung up, ...).
//
// For Phase 1 we implement the REST calls we need for the live dashboard and
// subscribe to the event WebSocket, forwarding events to the rest of the app.
package ari

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Client talks to ARI over REST and (optionally) the event WebSocket.
type Client struct {
	baseURL  string
	username string
	password string
	appName  string
	http     *http.Client
}

// Event is a decoded Stasis event. We keep the raw payload plus the always
// present "type" discriminator so callers can decide how much to decode.
type Event struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
}

// New constructs an ARI client. It does not perform any I/O.
func New(baseURL, username, password, appName string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		appName:  appName,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// do performs an authenticated ARI request and, if v is non-nil and the
// response has a body, decodes JSON into v. query may be nil.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, v any) error {
	u := c.baseURL + "/ari/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.username, c.password)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ari %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("ari %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if v == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// get is a convenience wrapper for GET requests.
func (c *Client) get(ctx context.Context, path string, v any) error {
	return c.do(ctx, http.MethodGet, path, nil, v)
}

// Endpoint is a slimmed-down view of an ARI endpoint resource.
type Endpoint struct {
	Technology string   `json:"technology"`
	Resource   string   `json:"resource"`
	State      string   `json:"state"`
	ChannelIDs []string `json:"channel_ids"`
}

// Endpoints lists all endpoints known to Asterisk (registered or not).
func (c *Client) Endpoints(ctx context.Context) ([]Endpoint, error) {
	var out []Endpoint
	if err := c.get(ctx, "endpoints", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Channel is a slimmed-down view of an active ARI channel.
type Channel struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Caller struct {
		Name   string `json:"name"`
		Number string `json:"number"`
	} `json:"caller"`
	Connected struct {
		Name   string `json:"name"`
		Number string `json:"number"`
	} `json:"connected"`
	Dialplan struct {
		Context  string `json:"context"`
		Exten    string `json:"exten"`
		Priority int    `json:"priority"`
		AppName  string `json:"app_name"`
		AppData  string `json:"app_data"`
	} `json:"dialplan"`
	CreationTime string `json:"creationtime"`
}

// Channels lists all active channels (i.e. live calls/legs).
func (c *Client) Channels(ctx context.Context) ([]Channel, error) {
	var out []Channel
	if err := c.get(ctx, "channels", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// OriginateParams describes a new outbound call. Endpoint is required (e.g.
// "PJSIP/1001"); the call is placed into the dialplan at Context,Extension.
type OriginateParams struct {
	Endpoint  string
	Extension string
	Context   string
	Priority  string
	CallerID  string
	Timeout   string // seconds; defaults to "30"
}

// Originate places a new call and connects it into the dialplan. It returns the
// created channel.
func (c *Client) Originate(ctx context.Context, p OriginateParams) (Channel, error) {
	var ch Channel
	if p.Endpoint == "" {
		return ch, fmt.Errorf("endpoint is required")
	}
	q := url.Values{}
	q.Set("endpoint", p.Endpoint)
	if p.Extension != "" {
		q.Set("extension", p.Extension)
	}
	if p.Context != "" {
		q.Set("context", p.Context)
	}
	q.Set("priority", orDefault(p.Priority, "1"))
	if p.CallerID != "" {
		q.Set("callerId", p.CallerID)
	}
	q.Set("timeout", orDefault(p.Timeout, "30"))
	err := c.do(ctx, http.MethodPost, "channels", q, &ch)
	return ch, err
}

// Hangup terminates an active channel by id.
func (c *Client) Hangup(ctx context.Context, channelID, reason string) error {
	q := url.Values{}
	if reason != "" {
		q.Set("reason", reason)
	}
	return c.do(ctx, http.MethodDelete, "channels/"+channelID, q, nil)
}

// RTPStats is a per-channel audio RTP counter snapshot. Rx is packets Asterisk
// received FROM the peer (the peer is sending audio); Tx is packets Asterisk
// sent TO the peer (the peer is receiving audio). Raw is the underlying QoS
// string, exposed for diagnostics.
type RTPStats struct {
	Rx    int64  `json:"rx"`
	Tx    int64  `json:"tx"`
	Known bool   `json:"known"` // false when Asterisk reported no RTP data at all
	Raw   string `json:"raw,omitempty"`
}

// channelVar evaluates a channel variable/function via ARI, returning "" on error.
func (c *Client) channelVar(ctx context.Context, id, name string) string {
	var out struct {
		Value string `json:"value"`
	}
	q := url.Values{}
	q.Set("variable", name)
	if err := c.do(ctx, http.MethodGet, "channels/"+id+"/variable", q, &out); err != nil {
		return ""
	}
	return out.Value
}

// ChannelRTP reads the audio RTP packet counters for a channel. It prefers the
// full QoS blob (CHANNEL(rtpqos,audio,all) -> "...;rxcount=N;txcount=M;...")
// and falls back to the individual keys. Missing values are zero (e.g. before
// media flows).
func (c *Client) ChannelRTP(ctx context.Context, id string) RTPStats {
	var st RTPStats
	st.Raw = c.channelVar(ctx, id, "CHANNEL(rtpqos,audio,all)")
	for _, kv := range strings.Split(st.Raw, ";") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "rxcount":
			st.Rx = parseInt(v)
		case "txcount":
			st.Tx = parseInt(v)
		}
	}
	if st.Rx == 0 && st.Tx == 0 {
		st.Rx = parseInt(c.channelVar(ctx, id, "CHANNEL(rtpqos,audio,rxcount)"))
		st.Tx = parseInt(c.channelVar(ctx, id, "CHANNEL(rtpqos,audio,txcount)"))
	}
	// Known is true only if Asterisk actually reported RTP data. When the QoS
	// blob is empty and both counters are zero we simply couldn't read it (e.g.
	// media not up yet, or the read path failed) and must not claim "no audio".
	st.Known = strings.TrimSpace(st.Raw) != "" || st.Rx > 0 || st.Tx > 0
	return st
}

func parseInt(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// ReloadModule asks Asterisk to reload a module (e.g. "res_pjsip.so"). This is
// how the GUI applies configuration changes without a full restart.
func (c *Client) ReloadModule(ctx context.Context, module string) error {
	if module == "" {
		return fmt.Errorf("module is required")
	}
	return c.do(ctx, http.MethodPut, "asterisk/modules/"+module, nil, nil)
}

// Info is a slim view of ARI's /asterisk/info: version and lifecycle times.
type Info struct {
	System struct {
		Version  string `json:"version"`
		EntityID string `json:"entity_id"`
	} `json:"system"`
	Status struct {
		StartupTime    string `json:"startup_time"`
		LastReloadTime string `json:"last_reload_time"`
	} `json:"status"`
}

// AsteriskInfo returns version and status information about the running PBX.
func (c *Client) AsteriskInfo(ctx context.Context) (Info, error) {
	var info Info
	err := c.get(ctx, "asterisk/info", &info)
	return info, err
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// StreamEvents connects to the ARI events WebSocket and delivers Stasis events
// to onEvent until ctx is cancelled or the connection fails. The caller is
// expected to run this in a goroutine and reconnect on error.
func (c *Client) StreamEvents(ctx context.Context, onEvent func(Event)) error {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = "/ari/events"
	q := u.Query()
	q.Set("app", c.appName)
	q.Set("api_key", c.username+":"+c.password)
	u.RawQuery = q.Encode()

	conn, _, err := websocket.Dial(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("ari events dial: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(1 << 20)

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var ev Event
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		ev.Raw = json.RawMessage(data)
		onEvent(ev)
	}
}

// --- Bridges and call control (used by the outbound dialer) -----------------
//
// The dialer connects a customer to an agent by moving the customer's channel
// into a bridge the agent is already sitting in, rather than dialing the agent
// and waiting for them to answer. That is what makes a predictive connect feel
// instant: the agent hears the customer with no ring, because their leg was
// already up.

// Bridge is an ARI bridge resource.
type Bridge struct {
	ID         string   `json:"id"`
	Technology string   `json:"technology"`
	BridgeType string   `json:"bridge_type"`
	Channels   []string `json:"channels"`
}

// CreateBridge creates (or returns, if the id already exists) a mixing bridge.
// ARI treats POST bridges with an explicit id as create-or-get, which is what
// makes an agent's bridge safe to re-assert after a reconnect.
func (c *Client) CreateBridge(ctx context.Context, id string) (Bridge, error) {
	var b Bridge
	q := url.Values{}
	q.Set("type", "mixing")
	q.Set("bridgeId", id)
	err := c.do(ctx, http.MethodPost, "bridges", q, &b)
	return b, err
}

// GetBridge returns one bridge by id.
func (c *Client) GetBridge(ctx context.Context, id string) (Bridge, error) {
	var b Bridge
	err := c.get(ctx, "bridges/"+id, &b)
	return b, err
}

// AddToBridge places a channel into a bridge.
func (c *Client) AddToBridge(ctx context.Context, bridgeID string, channelIDs ...string) error {
	if len(channelIDs) == 0 {
		return nil
	}
	q := url.Values{}
	q.Set("channel", strings.Join(channelIDs, ","))
	return c.do(ctx, http.MethodPost, "bridges/"+bridgeID+"/addChannel", q, nil)
}

// RemoveFromBridge takes a channel out of a bridge without hanging it up, so an
// agent's own leg survives the customer leaving.
func (c *Client) RemoveFromBridge(ctx context.Context, bridgeID string, channelIDs ...string) error {
	if len(channelIDs) == 0 {
		return nil
	}
	q := url.Values{}
	q.Set("channel", strings.Join(channelIDs, ","))
	return c.do(ctx, http.MethodPost, "bridges/"+bridgeID+"/removeChannel", q, nil)
}

// DestroyBridge tears a bridge down.
func (c *Client) DestroyBridge(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "bridges/"+id, nil, nil)
}

// OriginateToApp places a call that lands in this Stasis application instead of
// the dialplan, so the engine owns the channel from the moment it answers. That
// ownership is the whole point: the dialer has to decide, per call, whether a
// human answered and whether an agent is free — decisions the dialplan cannot
// make for it.
//
// vars are set on the channel before dialing, which is how the engine
// correlates the answer back to the lead that caused it.
func (c *Client) OriginateToApp(ctx context.Context, endpoint, callerID, timeout string, vars map[string]string) (Channel, error) {
	var ch Channel
	if endpoint == "" {
		return ch, fmt.Errorf("endpoint is required")
	}
	q := url.Values{}
	q.Set("endpoint", endpoint)
	q.Set("app", c.appName)
	if callerID != "" {
		q.Set("callerId", callerID)
	}
	q.Set("timeout", orDefault(timeout, "30"))
	for k, v := range vars {
		q.Set("variables["+k+"]", v)
	}
	err := c.do(ctx, http.MethodPost, "channels", q, &ch)
	return ch, err
}

// Answer answers a channel that reached Stasis.
func (c *Client) Answer(ctx context.Context, channelID string) error {
	return c.do(ctx, http.MethodPost, "channels/"+channelID+"/answer", nil, nil)
}

// Play starts media on a channel and returns the playback id. media is an ARI
// media URI, e.g. "sound:/var/lib/asterisk/sounds/en/tpbx/safe-harbour".
func (c *Client) Play(ctx context.Context, channelID, media string) (string, error) {
	var pb struct {
		ID string `json:"id"`
	}
	q := url.Values{}
	q.Set("media", media)
	err := c.do(ctx, http.MethodPost, "channels/"+channelID+"/play", q, &pb)
	return pb.ID, err
}

// ChannelVar reads one channel variable, returning "" when unset or on error.
// Exported for the dialer, which uses channel variables to carry the lead id
// through the originate and back out on the Stasis event.
func (c *Client) ChannelVar(ctx context.Context, channelID, name string) string {
	return c.channelVar(ctx, channelID, name)
}
