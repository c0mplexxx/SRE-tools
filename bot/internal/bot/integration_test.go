package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestHTTPClientsLargeReplySmoke(t *testing.T) {
	t.Parallel()

	var activeQuery string
	alertmanager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		activeQuery = r.URL.RawQuery
		var alerts []Alert
		for i := 0; i < 10; i++ {
			alerts = append(alerts, Alert{
				Labels: map[string]string{
					"tenant":    "1",
					"severity":  "warning",
					"alertname": "disk_space_" + strings.Repeat("x", i),
					"instance":  "node-01",
				},
				Annotations: map[string]string{"line": "low disk " + strings.Repeat("a", 36)},
			})
		}
		alerts = append(alerts, Alert{Labels: map[string]string{"tenant": "0", "alertname": "ignore"}})
		_ = json.NewEncoder(w).Encode(alerts)
	}))
	defer alertmanager.Close()

	var (
		mu       sync.Mutex
		messages []string
	)
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Fatalf("unexpected Telegram path %s", r.URL.Path)
		}
		var payload struct {
			ChatID    int64  `json:"chat_id"`
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode Telegram payload: %v", err)
		}
		if payload.ChatID != 42 || payload.ParseMode != "HTML" {
			t.Fatalf("unexpected Telegram payload: %#v", payload)
		}
		mu.Lock()
		messages = append(messages, payload.Text)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
	}))
	defer telegram.Close()

	service := &Service{
		Alerts: &AlertmanagerClient{
			BaseURL: alertmanager.URL,
			Client:  alertmanager.Client(),
		},
		Telegram: &TelegramClient{
			APIBaseURL: telegram.URL,
			Token:      "test-token",
			Client:     telegram.Client(),
		},
		AllowedChatIDs:   map[int64]struct{}{42: {}},
		MessageLimit:     260,
		ExpandableQuotes: true,
	}

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/?")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if !strings.Contains(activeQuery, "active=true") || !strings.Contains(activeQuery, "silenced=false") || !strings.Contains(activeQuery, "inhibited=false") {
		t.Fatalf("Alertmanager query filters missing: %s", activeQuery)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(messages) < 2 {
		t.Fatalf("expected multi-message Telegram reply, got %d", len(messages))
	}
	if strings.Contains(strings.Join(messages, ""), "ignore") {
		t.Fatalf("tenant-0 alert leaked into reply: %q", messages)
	}
}

func TestTelegramClientSendPhotoMultipart(t *testing.T) {
	t.Parallel()

	var sawPhoto bool
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendPhoto") {
			t.Fatalf("unexpected Telegram path %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data;") {
			t.Fatalf("unexpected Content-Type %q", r.Header.Get("Content-Type"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if got := r.FormValue("chat_id"); got != "42" {
			t.Fatalf("chat_id=%q want 42", got)
		}
		if got := r.FormValue("caption"); got != "<b>CPU</b>" {
			t.Fatalf("caption=%q want HTML caption", got)
		}
		if got := r.FormValue("parse_mode"); got != "HTML" {
			t.Fatalf("parse_mode=%q want HTML", got)
		}
		file, header, err := r.FormFile("photo")
		if err != nil {
			t.Fatalf("photo form file missing: %v", err)
		}
		defer file.Close()
		body, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("read photo: %v", err)
		}
		if header.Filename != "graph.png" || string(body) != "png-bytes" {
			t.Fatalf("unexpected photo upload filename=%q body=%q", header.Filename, string(body))
		}
		sawPhoto = true
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
	}))
	defer telegram.Close()

	client := &TelegramClient{
		APIBaseURL: telegram.URL,
		Token:      "test-token",
		Client:     telegram.Client(),
	}
	if err := client.SendPhoto(context.Background(), 42, "graph.png", []byte("png-bytes"), "<b>CPU</b>"); err != nil {
		t.Fatalf("SendPhoto returned error: %v", err)
	}
	if !sawPhoto {
		t.Fatal("sendPhoto request was not observed")
	}
}

func TestTelegramRequestErrorRedactsToken(t *testing.T) {
	t.Parallel()

	client := &TelegramClient{Token: "secret-token"}
	err := client.requestError("poll Telegram updates", errors.New(`Get "https://api.telegram.org/botsecret-token/getUpdates": timeout`))
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("token leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("redacted marker missing: %v", err)
	}
}
