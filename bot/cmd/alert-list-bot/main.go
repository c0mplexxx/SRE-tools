package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"alert-list-bot/internal/bot"
)

func main() {
	cfg, err := readConfig()
	if err != nil {
		log.Fatal(err)
	}

	logger := log.New(os.Stdout, "alert-list-bot: ", log.LstdFlags|log.Lmsgprefix)
	client := &http.Client{Timeout: cfg.HTTPTimeout}
	service := &bot.Service{
		Alerts: &bot.AlertmanagerClient{
			BaseURL:         cfg.AlertmanagerURL,
			MetricsBaseURLs: cfg.MetricsURLs,
			Client:          client,
		},
		Telegram: &bot.TelegramClient{
			APIBaseURL: cfg.TelegramAPIBaseURL,
			Token:      cfg.TelegramBotToken,
			Client:     client,
		},
		AllowedChatIDs:   cfg.AllowedChatIDs,
		Logger:           logger,
		PollTimeout:      cfg.PollTimeout,
		RetryDelay:       cfg.RetryDelay,
		MessageLimit:     cfg.MessageLimit,
		ExpandableQuotes: cfg.ExpandableQuotes,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Printf("starting Alertmanager polling from %s", cfg.AlertmanagerURL)
	if err := service.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

type config struct {
	AlertmanagerURL    string
	MetricsURLs        map[string]string
	TelegramAPIBaseURL string
	TelegramBotToken   string
	AllowedChatIDs     map[int64]struct{}
	HTTPTimeout        time.Duration
	PollTimeout        time.Duration
	RetryDelay         time.Duration
	MessageLimit       int
	ExpandableQuotes   bool
}

func readConfig() (config, error) {
	var cfg config
	var allowedCSV string
	var metricsTenant0 string
	var metricsTenant1 string

	flag.StringVar(&cfg.AlertmanagerURL, "alertmanager-url", envString("ALERTMANAGER_URL", "http://127.0.0.1:9093"), "Alertmanager base URL")
	flag.StringVar(&metricsTenant0, "metrics-url-tenant-0", os.Getenv("METRICS_URL_TENANT_0"), "Prometheus/VictoriaMetrics base URL for tenant 0 commands")
	flag.StringVar(&metricsTenant1, "metrics-url-tenant-1", envString("METRICS_URL_TENANT_1", os.Getenv("METRICS_URL")), "Prometheus/VictoriaMetrics base URL for tenant 1 /check")
	flag.StringVar(&cfg.TelegramAPIBaseURL, "telegram-api-base-url", envString("TELEGRAM_API_BASE_URL", "https://api.telegram.org"), "Telegram Bot API base URL")
	flag.StringVar(&cfg.TelegramBotToken, "telegram-bot-token", os.Getenv("TELEGRAM_BOT_TOKEN"), "Telegram bot token")
	flag.StringVar(&allowedCSV, "telegram-allowed-chat-ids", os.Getenv("TELEGRAM_ALLOWED_CHAT_IDS"), "comma-separated Telegram chat IDs")
	flag.DurationVar(&cfg.HTTPTimeout, "http-timeout", envDuration("HTTP_TIMEOUT", 45*time.Second), "HTTP client timeout")
	flag.DurationVar(&cfg.PollTimeout, "poll-timeout", envDuration("TELEGRAM_POLL_TIMEOUT", 30*time.Second), "Telegram getUpdates long poll timeout")
	flag.DurationVar(&cfg.RetryDelay, "retry-delay", envDuration("RETRY_DELAY", 2*time.Second), "delay after polling failures")
	flag.IntVar(&cfg.MessageLimit, "telegram-message-limit", envInt("TELEGRAM_MESSAGE_LIMIT", bot.DefaultTelegramMessageLimit), "Telegram message size guard")
	flag.BoolVar(&cfg.ExpandableQuotes, "telegram-expandable-quotes", envBool("TELEGRAM_EXPANDABLE_QUOTES", true), "collapse alertname groups with more than three rows in Telegram")
	flag.Parse()

	if cfg.TelegramBotToken == "" {
		return config{}, fmt.Errorf("TELEGRAM_BOT_TOKEN or -telegram-bot-token is required")
	}
	if allowedCSV == "" {
		return config{}, fmt.Errorf("TELEGRAM_ALLOWED_CHAT_IDS or -telegram-allowed-chat-ids is required")
	}
	if cfg.PollTimeout <= 0 || cfg.HTTPTimeout <= 0 || cfg.RetryDelay <= 0 {
		return config{}, fmt.Errorf("timeouts and retry delay must be positive")
	}
	if cfg.HTTPTimeout <= cfg.PollTimeout {
		return config{}, fmt.Errorf("HTTP timeout must be longer than Telegram poll timeout")
	}
	if cfg.MessageLimit <= 0 || cfg.MessageLimit > bot.DefaultTelegramMessageLimit {
		return config{}, fmt.Errorf("telegram message limit must be between 1 and %d", bot.DefaultTelegramMessageLimit)
	}

	allowed, err := bot.ParseAllowedChatIDs(allowedCSV)
	if err != nil {
		return config{}, err
	}
	cfg.AllowedChatIDs = allowed
	cfg.MetricsURLs = map[string]string{}
	if metricsTenant0 != "" {
		cfg.MetricsURLs["0"] = metricsTenant0
	}
	if metricsTenant1 != "" {
		cfg.MetricsURLs[bot.TenantOne] = metricsTenant1
	}
	return cfg, nil
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		log.Printf("invalid %s=%q, using %s", name, value, fallback)
		return fallback
	}
	return parsed
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		log.Printf("invalid %s=%q, using %d", name, value, fallback)
		return fallback
	}
	return parsed
}

func envBool(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		log.Printf("invalid %s=%q, using %t", name, value, fallback)
		return fallback
	}
	return parsed
}
