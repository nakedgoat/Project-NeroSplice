package synapse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nakedgoat/Project-NeroSplice/internal/config"
)

func TestListUsersFiltersGuestsAndRemoteUsers(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_synapse/admin/v2/users" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"users": []map[string]any{
				{"name": "@alice:source.test", "displayname": "Alice", "admin": true, "deactivated": 0, "is_guest": false},
				{"name": "@bob:remote.test", "displayname": "Bob", "admin": false, "deactivated": 0, "is_guest": false},
				{"name": "@guest:source.test", "displayname": "Guest", "admin": false, "deactivated": 0, "is_guest": true},
			},
		})
	}))
	defer server.Close()

	client := New(config.SynapseConfig{
		BaseURL:    server.URL,
		ServerName: "source.test",
		AccessToken: "token",
	})

	users, err := client.ListUsers(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListUsers returned error: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("unexpected user count: %d", len(users))
	}
	if users[0].UserID != "@alice:source.test" {
		t.Fatalf("unexpected user id: %s", users[0].UserID)
	}
}

func TestGetRoomStateFallsBackToClientEndpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_synapse/admin/v1/rooms/!room:source.test/state":
			http.Error(w, "nope", http.StatusNotFound)
		case "/_matrix/client/v3/rooms/!room:source.test/state":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"type": "m.room.name", "state_key": "", "content": map[string]any{"name": "Room"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(config.SynapseConfig{
		BaseURL:    server.URL,
		ServerName: "source.test",
		AccessToken: "token",
	})

	state, err := client.GetRoomState(context.Background(), "!room:source.test")
	if err != nil {
		t.Fatalf("GetRoomState returned error: %v", err)
	}
	if len(state) != 1 || state[0].Type != "m.room.name" {
		t.Fatalf("unexpected state: %#v", state)
	}
}
