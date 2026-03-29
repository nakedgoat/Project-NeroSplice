package dendrite

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nakedgoat/Project-NeroSplice/internal/config"
)

func TestRegisterUserUsesSynapseSharedSecretMAC(t *testing.T) {
	t.Parallel()

	const nonce = "abc123"
	const secret = "shared-secret"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /_synapse/admin/v1/register":
			_ = json.NewEncoder(w).Encode(map[string]string{"nonce": nonce})
		case http.MethodPost + " /_synapse/admin/v1/register":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}

			mac := hmac.New(sha1.New, []byte(secret))
			_, _ = mac.Write([]byte(nonce))
			_, _ = mac.Write([]byte{0})
			_, _ = mac.Write([]byte("alice"))
			_, _ = mac.Write([]byte{0})
			_, _ = mac.Write([]byte("pw"))
			_, _ = mac.Write([]byte{0})
			_, _ = mac.Write([]byte("admin"))
			wantMAC := hex.EncodeToString(mac.Sum(nil))

			if got := payload["mac"]; got != wantMAC {
				t.Fatalf("unexpected mac: got %v want %s", got, wantMAC)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"user_id": "@alice:target.test"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(config.DendriteConfig{
		BaseURL:                  server.URL,
		ServerName:               "target.test",
		RegistrationSharedSecret: secret,
	})

	userID, err := client.RegisterUser(context.Background(), "alice", "pw", true)
	if err != nil {
		t.Fatalf("RegisterUser returned error: %v", err)
	}
	if userID != "@alice:target.test" {
		t.Fatalf("unexpected user id: %s", userID)
	}
}
