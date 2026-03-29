package dendrite

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nakedgoat/Project-NeroSplice/internal/config"
)

type Client struct {
	baseURL      string
	serverName   string
	adminToken   string
	sharedSecret string
	httpClient   *http.Client
}

func New(cfg config.DendriteConfig) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec

	return &Client{
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		serverName:   cfg.ServerName,
		adminToken:   cfg.AccessToken,
		sharedSecret: cfg.RegistrationSharedSecret,
		httpClient:   &http.Client{Transport: tr},
	}
}

func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/_matrix/client/versions", nil)
	if err != nil {
		return err
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("dendrite versions endpoint returned %s", res.Status)
	}
	return nil
}

func (c *Client) RegisterUser(ctx context.Context, username, password string, admin bool) (string, error) {
	var nonceResponse struct {
		Nonce string `json:"nonce"`
	}
	if err := c.getJSON(ctx, "/_synapse/admin/v1/register", "", &nonceResponse); err != nil {
		return "", fmt.Errorf("get registration nonce: %w", err)
	}

	mac := hmac.New(sha1.New, []byte(c.sharedSecret))
	_, _ = mac.Write([]byte(nonceResponse.Nonce))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(username))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(password))
	_, _ = mac.Write([]byte{0})
	if admin {
		_, _ = mac.Write([]byte("admin"))
	} else {
		_, _ = mac.Write([]byte("notadmin"))
	}

	payload := map[string]any{
		"nonce":    nonceResponse.Nonce,
		"username": username,
		"password": password,
		"admin":    admin,
		"mac":      hex.EncodeToString(mac.Sum(nil)),
	}
	var res struct {
		UserID string `json:"user_id"`
	}
	if err := c.postJSON(ctx, "/_synapse/admin/v1/register", "", payload, &res); err != nil {
		return "", fmt.Errorf("register user %s: %w", username, err)
	}
	return res.UserID, nil
}

func (c *Client) Login(ctx context.Context, localpart, password string) (string, error) {
	payload := map[string]any{
		"type": "m.login.password",
		"identifier": map[string]any{
			"type": "m.id.user",
			"user": localpart,
		},
		"password": password,
	}
	var res struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.postJSON(ctx, "/_matrix/client/v3/login", "", payload, &res); err != nil {
		return "", err
	}
	return res.AccessToken, nil
}

func (c *Client) CreateRoom(ctx context.Context, token, name, topic, alias, roomVersion string, encrypted bool) (string, error) {
	payload := map[string]any{
		"name": name,
	}
	if topic != "" {
		payload["topic"] = topic
	}
	if alias != "" {
		payload["room_alias_name"] = strings.TrimPrefix(strings.Split(alias, ":")[0], "#")
	}
	if roomVersion != "" {
		payload["room_version"] = roomVersion
	}
	if encrypted {
		payload["initial_state"] = []map[string]any{
			{
				"type":      "m.room.encryption",
				"state_key": "",
				"content": map[string]any{
					"algorithm": "m.megolm.v1.aes-sha2",
				},
			},
		}
	}
	var res struct {
		RoomID string `json:"room_id"`
	}
	if err := c.postJSON(ctx, "/_matrix/client/v3/createRoom", token, payload, &res); err != nil {
		return "", err
	}
	return res.RoomID, nil
}

func (c *Client) PutState(ctx context.Context, token, roomID, eventType, stateKey string, content map[string]any) error {
	p := "/_matrix/client/v3/rooms/" + url.PathEscape(roomID) + "/state/" + url.PathEscape(eventType)
	if stateKey != "" {
		p += "/" + url.PathEscape(stateKey)
	}
	return c.putJSON(ctx, p, token, content, nil)
}

func (c *Client) InviteUser(ctx context.Context, token, roomID, userID string) error {
	payload := map[string]any{"user_id": userID}
	p := "/_matrix/client/v3/rooms/" + url.PathEscape(roomID) + "/invite"
	return c.postJSON(ctx, p, token, payload, nil)
}

func (c *Client) JoinRoom(ctx context.Context, token, roomID string) error {
	p := "/_matrix/client/v3/join/" + url.PathEscape(roomID)
	return c.postJSON(ctx, p, token, map[string]any{}, nil)
}

func (c *Client) SetDisplayName(ctx context.Context, token, userID, displayName string) error {
	p := "/_matrix/client/v3/profile/" + url.PathEscape(userID) + "/displayname"
	return c.putJSON(ctx, p, token, map[string]string{"displayname": displayName}, nil)
}

func (c *Client) SetAvatarURL(ctx context.Context, token, userID, avatarURL string) error {
	p := "/_matrix/client/v3/profile/" + url.PathEscape(userID) + "/avatar_url"
	return c.putJSON(ctx, p, token, map[string]string{"avatar_url": avatarURL}, nil)
}

func (c *Client) UploadMedia(ctx context.Context, fileName, contentType string, data []byte) (string, error) {
	u := c.baseURL + "/_matrix/media/v3/upload"
	if fileName != "" {
		u += "?filename=" + url.QueryEscape(fileName)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.adminToken)
	req.Header.Set("Content-Type", contentType)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return "", fmt.Errorf("upload media: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	var out struct {
		ContentURI string `json:"content_uri"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ContentURI, nil
}

func (c *Client) getJSON(ctx context.Context, path, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("%s returned %s: %s", path, res.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (c *Client) postJSON(ctx context.Context, path, token string, payload any, out any) error {
	return c.doJSON(ctx, http.MethodPost, path, token, payload, out)
}

func (c *Client) putJSON(ctx context.Context, path, token string, payload any, out any) error {
	return c.doJSON(ctx, http.MethodPut, path, token, payload, out)
}

func (c *Client) doJSON(ctx context.Context, method, path, token string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("%s %s returned %s: %s", method, path, res.Status, strings.TrimSpace(string(bodyBytes)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}
