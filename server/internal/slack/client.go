package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// webAPIBase is overridden in tests via WithBaseURL. The Slack Web API
// terminates only at https://slack.com/api — we hardcode it rather than
// per-deploy config because there is no staging endpoint.
const webAPIBase = "https://slack.com/api"

// Client wraps a single Slack workspace's bot token for outbound Web API
// calls. One Client per (agent, workspace) since bot tokens are per-app.
type Client struct {
	httpClient *http.Client
	baseURL    string
	botToken   string
}

// NewClient returns a Client with sane HTTP timeouts. botToken may be
// empty for endpoints that don't need it (auth.test on a freshly
// exchanged token uses NewClient + WithBotToken in the OAuth path).
func NewClient(botToken string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		baseURL:    webAPIBase,
		botToken:   botToken,
	}
}

// WithBaseURL overrides the API base URL — test-only.
func (c *Client) WithBaseURL(u string) *Client {
	c.baseURL = strings.TrimRight(u, "/")
	return c
}

// PostMessageRequest is the subset of chat.postMessage we use.
type PostMessageRequest struct {
	Channel  string `json:"channel"`
	Text     string `json:"text"`
	ThreadTS string `json:"thread_ts,omitempty"`
}

type PostMessageResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Channel string `json:"channel,omitempty"`
	TS      string `json:"ts,omitempty"`
}

// PostMessage posts to a channel/thread. Returns the parsed response so
// the caller can keep the message TS if it ever needs to update it.
func (c *Client) PostMessage(ctx context.Context, req PostMessageRequest) (*PostMessageResponse, error) {
	var out PostMessageResponse
	if err := c.doJSON(ctx, "chat.postMessage", req, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return &out, fmt.Errorf("slack: %s", out.Error)
	}
	return &out, nil
}

// UserInfoResponse is the trimmed users.info response.
type UserInfoResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	User  struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Profile struct {
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
			RealName    string `json:"real_name"`
			Image192    string `json:"image_192"`
		} `json:"profile"`
	} `json:"user"`
}

// UsersInfo fetches profile data for a Slack user. Email is best-effort
// (Slack only returns it if the app has the users:read.email scope).
func (c *Client) UsersInfo(ctx context.Context, userID string) (*UserInfoResponse, error) {
	v := url.Values{}
	v.Set("user", userID)
	var out UserInfoResponse
	if err := c.doForm(ctx, "users.info", v, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return &out, fmt.Errorf("slack: %s", out.Error)
	}
	return &out, nil
}

// AuthTestResponse is the trimmed auth.test response — used after OAuth
// exchange to confirm the token is live and learn the bot user id.
type AuthTestResponse struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	URL    string `json:"url,omitempty"`
	Team   string `json:"team,omitempty"`
	TeamID string `json:"team_id,omitempty"`
	User   string `json:"user,omitempty"`
	UserID string `json:"user_id,omitempty"`
	BotID  string `json:"bot_id,omitempty"`
}

func (c *Client) AuthTest(ctx context.Context) (*AuthTestResponse, error) {
	var out AuthTestResponse
	if err := c.doForm(ctx, "auth.test", url.Values{}, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return &out, fmt.Errorf("slack: %s", out.Error)
	}
	return &out, nil
}

// OAuthV2AccessResponse is the trimmed oauth.v2.access response.
type OAuthV2AccessResponse struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	BotUserID   string `json:"bot_user_id"`
	AppID       string `json:"app_id"`
	Team        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"team"`
}

// OAuthV2Access exchanges an OAuth code for a bot token. Note: this is
// the only endpoint that does NOT use the bot token (it produces one).
// clientID / clientSecret are the per-app credentials minted by Slack at
// app creation; we pull them from the slack_agent_app row's stored
// metadata where the manifest client cached them.
func OAuthV2Access(ctx context.Context, httpClient *http.Client, baseURL, code, clientID, clientSecret, redirectURI string) (*OAuthV2AccessResponse, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	if baseURL == "" {
		baseURL = webAPIBase
	}
	v := url.Values{}
	v.Set("code", code)
	v.Set("client_id", clientID)
	v.Set("client_secret", clientSecret)
	if redirectURI != "" {
		v.Set("redirect_uri", redirectURI)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/oauth.v2.access", strings.NewReader(v.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out OAuthV2AccessResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode oauth.v2.access: %w", err)
	}
	if !out.OK {
		return &out, fmt.Errorf("slack: %s", out.Error)
	}
	return &out, nil
}

// ── internals ──────────────────────────────────────────────────────────

func (c *Client) doJSON(ctx context.Context, method string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if c.botToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.botToken)
	}
	return c.do(req, out)
}

func (c *Client) doForm(ctx context.Context, method string, v url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+method, strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.botToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.botToken)
	}
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

// ErrNoBotToken is returned when an endpoint that requires the bot token
// is called on a Client that was not given one.
var ErrNoBotToken = errors.New("slack: bot token not set")
