package synapse

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/nakedgoat/Project-NeroSplice/internal/config"
	"github.com/nakedgoat/Project-NeroSplice/internal/models"
)

type Client struct {
	baseURL    string
	serverName string
	httpClient *http.Client
	token      string
}

func New(cfg config.SynapseConfig) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec

	return &Client{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		serverName: cfg.ServerName,
		httpClient: &http.Client{Transport: tr},
		token:      cfg.AccessToken,
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
		return fmt.Errorf("synapse versions endpoint returned %s", res.Status)
	}
	return nil
}

func (c *Client) ListUsers(ctx context.Context, limit int) ([]models.User, error) {
	type userEntry struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayname"`
		AvatarURL   string `json:"avatar_url"`
		Admin       bool   `json:"admin"`
		Deactivated int    `json:"deactivated"`
		IsGuest     bool   `json:"is_guest"`
	}
	type response struct {
		Users []userEntry `json:"users"`
		Next  string      `json:"next_token"`
	}

	var (
		users []models.User
		from  = "0"
	)

	for {
		if limit > 0 && len(users) >= limit {
			break
		}

		res := response{}
		uri := fmt.Sprintf("%s/_synapse/admin/v2/users?from=%s&limit=100&guests=false", c.baseURL, url.QueryEscape(from))
		if err := c.getJSON(ctx, uri, &res); err != nil {
			return nil, err
		}

		for _, item := range res.Users {
			if item.IsGuest || !c.isLocalUser(item.Name) {
				continue
			}
			users = append(users, models.User{
				UserID:      item.Name,
				Localpart:   localpart(item.Name),
				DisplayName: item.DisplayName,
				AvatarURL:   item.AvatarURL,
				Admin:       item.Admin,
				Deactivated: item.Deactivated != 0,
			})
			if limit > 0 && len(users) >= limit {
				break
			}
		}

		if res.Next == "" || len(res.Users) == 0 {
			break
		}
		from = res.Next
	}

	return users, nil
}

func (c *Client) ListRooms(ctx context.Context, limit int) ([]models.Room, error) {
	type roomEntry struct {
		RoomID      string `json:"room_id"`
		Name        string `json:"name"`
		Canonical   string `json:"canonical_alias"`
		Topic       string `json:"topic"`
		JoinRule    string `json:"join_rules"`
		RoomVersion string `json:"version"`
	}
	type response struct {
		Rooms []roomEntry `json:"rooms"`
		Next  string      `json:"next_batch"`
	}

	var (
		rooms []models.Room
		from  = "0"
	)

	for {
		if limit > 0 && len(rooms) >= limit {
			break
		}

		res := response{}
		uri := fmt.Sprintf("%s/_synapse/admin/v1/rooms?from=%s&limit=100", c.baseURL, url.QueryEscape(from))
		if err := c.getJSON(ctx, uri, &res); err != nil {
			return nil, err
		}

		for _, item := range res.Rooms {
			state, err := c.GetRoomState(ctx, item.RoomID)
			if err != nil {
				return nil, err
			}
			members, err := c.GetRoomMembers(ctx, item.RoomID)
			if err != nil {
				return nil, err
			}
			room := models.Room{
				RoomID:      item.RoomID,
				Name:        item.Name,
				Topic:       item.Topic,
				Canonical:   item.Canonical,
				JoinRule:    item.JoinRule,
				RoomVersion: item.RoomVersion,
				Members:     members,
				State:       state,
			}
			for _, ev := range state {
				switch ev.Type {
				case "m.room.name":
					if v, ok := ev.Content["name"].(string); ok && room.Name == "" {
						room.Name = v
					}
				case "m.room.topic":
					if v, ok := ev.Content["topic"].(string); ok && room.Topic == "" {
						room.Topic = v
					}
				case "m.room.canonical_alias":
					if v, ok := ev.Content["alias"].(string); ok && room.Canonical == "" {
						room.Canonical = v
					}
				case "m.room.join_rules":
					if v, ok := ev.Content["join_rule"].(string); ok && room.JoinRule == "" {
						room.JoinRule = v
					}
				case "m.room.create":
					if v, ok := ev.Content["room_version"].(string); ok && room.RoomVersion == "" {
						room.RoomVersion = v
					}
				case "m.room.encryption":
					room.Encrypted = true
				case "m.room.avatar":
					if v, ok := ev.Content["url"].(string); ok {
						room.AvatarURL = v
					}
				case "m.room.history_visibility":
					if v, ok := ev.Content["history_visibility"].(string); ok {
						room.HistoryVis = v
					}
				case "m.room.guest_access":
					if v, ok := ev.Content["guest_access"].(string); ok {
						room.GuestAccess = v
					}
				}
			}

			rooms = append(rooms, room)
			if limit > 0 && len(rooms) >= limit {
				break
			}
		}

		if res.Next == "" || len(res.Rooms) == 0 {
			break
		}
		from = res.Next
	}

	return rooms, nil
}

func (c *Client) GetRoomState(ctx context.Context, roomID string) ([]models.StateEvent, error) {
	var state []models.StateEvent
	uri := fmt.Sprintf("%s/_synapse/admin/v1/rooms/%s/state", c.baseURL, url.PathEscape(roomID))
	if err := c.getJSON(ctx, uri, &state); err == nil {
		return state, nil
	}

	uri = fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/state", c.baseURL, url.PathEscape(roomID))
	if err := c.getJSON(ctx, uri, &state); err != nil {
		return nil, fmt.Errorf("get room state for %s: %w", roomID, err)
	}
	return state, nil
}

func (c *Client) GetRoomMembers(ctx context.Context, roomID string) ([]string, error) {
	type adminMembersResponse struct {
		Members []struct {
			UserID string `json:"user_id"`
		} `json:"members"`
	}
	type joinedResponse struct {
		Joined map[string]struct{} `json:"joined"`
	}

	adminRes := adminMembersResponse{}
	uri := fmt.Sprintf("%s/_synapse/admin/v1/rooms/%s/members", c.baseURL, url.PathEscape(roomID))
	if err := c.getJSON(ctx, uri, &adminRes); err == nil && len(adminRes.Members) > 0 {
		members := make([]string, 0, len(adminRes.Members))
		for _, item := range adminRes.Members {
			if c.isLocalUser(item.UserID) {
				members = append(members, item.UserID)
			}
		}
		return members, nil
	}

	joinedRes := joinedResponse{}
	uri = fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/joined_members", c.baseURL, url.PathEscape(roomID))
	if err := c.getJSON(ctx, uri, &joinedRes); err != nil {
		return nil, fmt.Errorf("get room members for %s: %w", roomID, err)
	}
	members := make([]string, 0, len(joinedRes.Joined))
	for userID := range joinedRes.Joined {
		if c.isLocalUser(userID) {
			members = append(members, userID)
		}
	}
	return members, nil
}

func (c *Client) DownloadMXC(ctx context.Context, mxcURI string) ([]byte, string, error) {
	server, mediaID, err := splitMXC(mxcURI)
	if err != nil {
		return nil, "", err
	}
	downloadURL := c.baseURL + path.Join("/_matrix/media/v3/download", server, mediaID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, "", fmt.Errorf("download media %s: %s: %s", mxcURI, res.Status, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", err
	}
	return data, res.Header.Get("Content-Type"), nil
}

func (c *Client) getJSON(ctx context.Context, uri string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("%s returned %s: %s", uri, res.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (c *Client) isLocalUser(userID string) bool {
	return strings.HasSuffix(userID, ":"+c.serverName)
}

func localpart(userID string) string {
	trimmed := strings.TrimPrefix(userID, "@")
	if i := strings.IndexByte(trimmed, ':'); i >= 0 {
		return trimmed[:i]
	}
	return trimmed
}

func splitMXC(mxcURI string) (string, string, error) {
	if !strings.HasPrefix(mxcURI, "mxc://") {
		return "", "", fmt.Errorf("invalid mxc uri: %s", mxcURI)
	}
	parts := strings.Split(strings.TrimPrefix(mxcURI, "mxc://"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid mxc uri: %s", mxcURI)
	}
	return parts[0], parts[1], nil
}
