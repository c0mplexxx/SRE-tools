package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Messenger interface {
	GetUpdates(context.Context, int, time.Duration) ([]Update, error)
	SendMessage(context.Context, int64, string) error
	SendPhoto(context.Context, int64, string, []byte, string) error
}

type TelegramClient struct {
	APIBaseURL string
	Token      string
	Client     *http.Client
}

func (c *TelegramClient) GetUpdates(ctx context.Context, offset int, timeout time.Duration) ([]Update, error) {
	endpoint := c.methodURL("getUpdates")
	query := endpoint.Query()
	if offset > 0 {
		query.Set("offset", strconv.Itoa(offset))
	}
	query.Set("timeout", strconv.Itoa(max(1, int(timeout.Seconds()))))
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build Telegram getUpdates request: %w", err)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, c.requestError("poll Telegram updates", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poll Telegram updates: unexpected HTTP %s", resp.Status)
	}

	var payload struct {
		OK          bool     `json:"ok"`
		Result      []Update `json:"result"`
		Description string   `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Telegram updates: %w", err)
	}
	if !payload.OK {
		return nil, fmt.Errorf("poll Telegram updates: Telegram rejected request: %s", payload.Description)
	}
	return payload.Result, nil
}

func (c *TelegramClient) SendMessage(ctx context.Context, chatID int64, text string) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Telegram message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL("sendMessage").String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build Telegram sendMessage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return c.requestError("send Telegram message", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("send Telegram message: unexpected HTTP %s", resp.Status)
	}

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode Telegram sendMessage response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("send Telegram message: Telegram rejected request: %s", result.Description)
	}
	return nil
}

func (c *TelegramClient) SendPhoto(ctx context.Context, chatID int64, filename string, photo []byte, caption string) error {
	if len(photo) == 0 {
		return fmt.Errorf("send Telegram photo: empty photo")
	}
	if strings.TrimSpace(filename) == "" {
		filename = "graph.png"
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return fmt.Errorf("build Telegram photo payload: %w", err)
	}
	if strings.TrimSpace(caption) != "" {
		if err := writer.WriteField("caption", caption); err != nil {
			return fmt.Errorf("build Telegram photo payload: %w", err)
		}
		if err := writer.WriteField("parse_mode", "HTML"); err != nil {
			return fmt.Errorf("build Telegram photo payload: %w", err)
		}
	}
	part, err := writer.CreateFormFile("photo", filename)
	if err != nil {
		return fmt.Errorf("build Telegram photo payload: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(photo)); err != nil {
		return fmt.Errorf("build Telegram photo payload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("build Telegram photo payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL("sendPhoto").String(), &body)
	if err != nil {
		return fmt.Errorf("build Telegram sendPhoto request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return c.requestError("send Telegram photo", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("send Telegram photo: unexpected HTTP %s", resp.Status)
	}

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode Telegram sendPhoto response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("send Telegram photo: Telegram rejected request: %s", result.Description)
	}
	return nil
}

func (c *TelegramClient) methodURL(method string) *url.URL {
	base := strings.TrimRight(c.APIBaseURL, "/")
	endpoint, _ := url.Parse(base + "/bot" + c.Token + "/" + method)
	return endpoint
}

func (c *TelegramClient) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

func (c *TelegramClient) requestError(action string, err error) error {
	message := err.Error()
	if c.Token != "" {
		message = strings.ReplaceAll(message, c.Token, "<redacted>")
	}
	return fmt.Errorf("%s: %s", action, message)
}
