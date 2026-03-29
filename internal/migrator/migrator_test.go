package migrator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nakedgoat/Project-NeroSplice/internal/config"
)

func TestMigrateUsersWritesStateAndPasswordReport(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_synapse/admin/v2/users":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"users": []map[string]any{
					{"name": "@alice:source.test", "displayname": "Alice", "admin": true, "deactivated": 0, "is_guest": false},
					{"name": "@sleep:source.test", "displayname": "Sleep", "admin": false, "deactivated": 1, "is_guest": false},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()

	var mu sync.Mutex
	var displayNameAuth string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /_synapse/admin/v1/register":
			_ = json.NewEncoder(w).Encode(map[string]string{"nonce": "nonce"})
		case http.MethodPost + " /_synapse/admin/v1/register":
			_ = json.NewEncoder(w).Encode(map[string]string{"user_id": "@alice:target.test"})
		case http.MethodPost + " /_matrix/client/v3/login":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "user-token"})
		case http.MethodPut + " /_matrix/client/v3/profile/@alice:target.test/displayname":
			mu.Lock()
			displayNameAuth = r.Header.Get("Authorization")
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer target.Close()

	cfg := &config.Config{
		Source: config.SynapseConfig{
			BaseURL: source.URL, ServerName: "source.test", AccessToken: "source-token",
		},
		Target: config.DendriteConfig{
			BaseURL: target.URL, ServerName: "target.test", AccessToken: "admin-token", RegistrationSharedSecret: "secret",
		},
		Migration: config.MigrationConfig{
			StatePath: filepath.Join(tempDir, "state.json"),
			PasswordReportPath: filepath.Join(tempDir, "passwords.csv"),
			Concurrency: 1,
			TempPasswordPrefix: "tmp-",
		},
	}

	m, err := New(cfg, false)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := m.MigrateUsers(context.Background()); err != nil {
		t.Fatalf("MigrateUsers returned error: %v", err)
	}
	if err := m.WritePasswordReport(); err != nil {
		t.Fatalf("WritePasswordReport returned error: %v", err)
	}

	stateBytes, err := os.ReadFile(cfg.Migration.StatePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !strings.Contains(string(stateBytes), "@alice:source.test") {
		t.Fatalf("state file missing migrated user: %s", stateBytes)
	}
	if strings.Contains(string(stateBytes), "@sleep:source.test") {
		t.Fatalf("deactivated user should not be migrated: %s", stateBytes)
	}

	reportBytes, err := os.ReadFile(cfg.Migration.PasswordReportPath)
	if err != nil {
		t.Fatalf("read password report: %v", err)
	}
	if !strings.Contains(string(reportBytes), "tmp-alice") {
		t.Fatalf("password report missing temp password: %s", reportBytes)
	}

	mu.Lock()
	gotAuth := displayNameAuth
	mu.Unlock()
	if gotAuth != "Bearer user-token" {
		t.Fatalf("display name update used wrong token: %s", gotAuth)
	}
}

func TestMigrateRoomsAggregatesFailures(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_synapse/admin/v1/rooms":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"rooms": []map[string]any{
					{"room_id": "!one:source.test", "name": "One"},
					{"room_id": "!two:source.test", "name": "Two"},
				},
			})
		case "/_synapse/admin/v1/rooms/!one:source.test/state":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"type": "m.room.name", "state_key": "", "content": map[string]any{"name": "One"}},
			})
		case "/_synapse/admin/v1/rooms/!two:source.test/state":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"type": "m.room.name", "state_key": "", "content": map[string]any{"name": "Two"}},
			})
		case "/_synapse/admin/v1/rooms/!one:source.test/members", "/_synapse/admin/v1/rooms/!two:source.test/members":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"members": []map[string]any{
					{"user_id": "@alice:source.test"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodPost + " /_matrix/client/v3/createRoom":
			if strings.Contains(r.Header.Get("Authorization"), "admin-token") {
				_ = json.NewEncoder(w).Encode(map[string]string{"room_id": "!new:target.test"})
				return
			}
			http.Error(w, "bad auth", http.StatusUnauthorized)
		case http.MethodPut + " /_matrix/client/v3/rooms/!new:target.test/state/m.room.name":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer target.Close()

	cfg := &config.Config{
		Source: config.SynapseConfig{
			BaseURL: source.URL, ServerName: "source.test", AccessToken: "source-token",
		},
		Target: config.DendriteConfig{
			BaseURL: target.URL, ServerName: "target.test", AccessToken: "admin-token", RegistrationSharedSecret: "secret",
		},
		Migration: config.MigrationConfig{
			StatePath: filepath.Join(tempDir, "state.json"),
			Concurrency: 1,
			TempPasswordPrefix: "tmp-",
		},
	}

	m, err := New(cfg, false)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	err = m.MigrateRooms(context.Background())
	if err == nil {
		t.Fatal("expected room migration error")
	}
	if !strings.Contains(err.Error(), "room migration failures:") || !strings.Contains(err.Error(), "!one:source.test") || !strings.Contains(err.Error(), "!two:source.test") {
		t.Fatalf("unexpected error aggregation: %v", err)
	}
}

func TestMigrateAllEndToEnd(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	avatarBytes := []byte("avatar-data")

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_matrix/client/versions":
			_ = json.NewEncoder(w).Encode(map[string]any{"versions": []string{"v1.10"}})
		case "/_synapse/admin/v2/users":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"users": []map[string]any{
					{
						"name": "@alice:source.test", "displayname": "Alice", "admin": true, "deactivated": 0, "is_guest": false,
						"avatar_url": "mxc://source.test/avatar1",
					},
				},
			})
		case "/_synapse/admin/v1/rooms":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"rooms": []map[string]any{
					{"room_id": "!room:source.test", "name": "General", "topic": "Talk"},
				},
			})
		case "/_synapse/admin/v1/rooms/!room:source.test/state":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"type": "m.room.name", "state_key": "", "content": map[string]any{"name": "General"}},
				{"type": "m.room.topic", "state_key": "", "content": map[string]any{"topic": "Talk"}},
				{"type": "m.room.power_levels", "state_key": "", "content": map[string]any{"users_default": 0}},
			})
		case "/_synapse/admin/v1/rooms/!room:source.test/members":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"members": []map[string]any{{"user_id": "@alice:source.test"}},
			})
		case "/_matrix/media/v3/download/source.test/avatar1":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(avatarBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()

	var mu sync.Mutex
	var createdUsers []string
	var invitedUsers []string
	var joinedRooms []string
	var profileAuth []string
	var uploadedMedia [][]byte
	var uploadedMediaTypes []string

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /_matrix/client/versions":
			_ = json.NewEncoder(w).Encode(map[string]any{"versions": []string{"v1.10"}})
		case http.MethodGet + " /_synapse/admin/v1/register":
			_ = json.NewEncoder(w).Encode(map[string]string{"nonce": "nonce"})
		case http.MethodPost + " /_synapse/admin/v1/register":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			mu.Lock()
			createdUsers = append(createdUsers, payload["username"].(string))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]string{"user_id": "@alice:target.test"})
		case http.MethodPost + " /_matrix/client/v3/login":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "alice-token"})
		case http.MethodPut + " /_matrix/client/v3/profile/@alice:target.test/displayname":
			mu.Lock()
			profileAuth = append(profileAuth, r.Header.Get("Authorization"))
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodPost + " /_matrix/client/v3/createRoom":
			_ = json.NewEncoder(w).Encode(map[string]string{"room_id": "!newroom:target.test"})
		case http.MethodPut + " /_matrix/client/v3/rooms/!newroom:target.test/state/m.room.name":
			w.WriteHeader(http.StatusOK)
		case http.MethodPut + " /_matrix/client/v3/rooms/!newroom:target.test/state/m.room.topic":
			w.WriteHeader(http.StatusOK)
		case http.MethodPut + " /_matrix/client/v3/rooms/!newroom:target.test/state/m.room.power_levels":
			w.WriteHeader(http.StatusOK)
		case http.MethodPost + " /_matrix/client/v3/rooms/!newroom:target.test/invite":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			mu.Lock()
			invitedUsers = append(invitedUsers, payload["user_id"].(string))
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodPost + " /_matrix/client/v3/join/!newroom:target.test":
			mu.Lock()
			joinedRooms = append(joinedRooms, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodPost + " /_matrix/media/v3/upload":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			uploadedMedia = append(uploadedMedia, body)
			uploadedMediaTypes = append(uploadedMediaTypes, r.Header.Get("Content-Type"))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]string{"content_uri": "mxc://target.test/avatar-new"})
		case http.MethodPut + " /_matrix/client/v3/profile/@alice:target.test/avatar_url":
			mu.Lock()
			profileAuth = append(profileAuth, r.Header.Get("Authorization"))
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer target.Close()

	cfg := &config.Config{
		Source: config.SynapseConfig{
			BaseURL: source.URL, ServerName: "source.test", AccessToken: "source-token",
		},
		Target: config.DendriteConfig{
			BaseURL: target.URL, ServerName: "target.test", AccessToken: "admin-token", RegistrationSharedSecret: "secret",
		},
		Migration: config.MigrationConfig{
			StatePath: filepath.Join(tempDir, "state.json"),
			PasswordReportPath: filepath.Join(tempDir, "passwords.csv"),
			Concurrency: 1,
			TempPasswordPrefix: "tmp-",
		},
	}

	m, err := New(cfg, false)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := m.Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight returned error: %v", err)
	}
	if err := m.MigrateAll(context.Background()); err != nil {
		t.Fatalf("MigrateAll returned error: %v", err)
	}

	stateBytes, err := os.ReadFile(cfg.Migration.StatePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !bytes.Contains(stateBytes, []byte(`"@alice:source.test"`)) || !bytes.Contains(stateBytes, []byte(`"!room:source.test"`)) {
		t.Fatalf("state missing migrated entities: %s", stateBytes)
	}

	reportBytes, err := os.ReadFile(cfg.Migration.PasswordReportPath)
	if err != nil {
		t.Fatalf("read password report: %v", err)
	}
	if !bytes.Contains(reportBytes, []byte("tmp-alice")) {
		t.Fatalf("password report missing alice: %s", reportBytes)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(createdUsers) != 1 || createdUsers[0] != "alice" {
		t.Fatalf("unexpected created users: %#v", createdUsers)
	}
	if len(invitedUsers) != 1 || invitedUsers[0] != "@alice:target.test" {
		t.Fatalf("unexpected invited users: %#v", invitedUsers)
	}
	if len(joinedRooms) != 1 {
		t.Fatalf("expected one joined room, got %#v", joinedRooms)
	}
	if len(uploadedMedia) != 1 || !bytes.Equal(uploadedMedia[0], avatarBytes) {
		t.Fatalf("unexpected uploaded media: %#v", uploadedMedia)
	}
	if len(uploadedMediaTypes) != 1 || uploadedMediaTypes[0] != "image/png" {
		t.Fatalf("unexpected media content type: %#v", uploadedMediaTypes)
	}
	if len(profileAuth) != 2 {
		t.Fatalf("expected two profile auth uses, got %#v", profileAuth)
	}
	for _, auth := range profileAuth {
		if auth != "Bearer alice-token" {
			t.Fatalf("profile update used wrong token: %#v", profileAuth)
		}
	}
}
