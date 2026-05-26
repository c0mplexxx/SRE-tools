package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

type AlertSource interface {
	ActiveTenantAlerts(context.Context, string) ([]Alert, error)
	Status(context.Context) (AlertmanagerStatus, error)
	ActiveSilences(context.Context) ([]AlertmanagerSilence, error)
	CheckInstance(context.Context, string, string, string) (InstanceCheck, error)
	SilenceAlert(context.Context, Alert, time.Duration, string, string) (Silence, error)
	SilenceMatchers(context.Context, []SilenceMatcher, time.Duration, string, string) (Silence, error)
	ExpireSilence(context.Context, string) error
}

type AlertmanagerClient struct {
	BaseURL         string
	MetricsBaseURLs map[string]string
	Client          *http.Client
	Now             func() time.Time
}

type Silence struct {
	ID        string
	StartsAt  time.Time
	EndsAt    time.Time
	AlertID   string
	Matchers  []SilenceMatcher
	CreatedBy string
	Comment   string
}

type AlertmanagerStatus struct {
	Ready              bool
	ActiveTenantAlerts int
}

type AlertmanagerSilence struct {
	ID        string           `json:"id"`
	Status    SilenceStatus    `json:"status"`
	StartsAt  time.Time        `json:"startsAt"`
	EndsAt    time.Time        `json:"endsAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
	Matchers  []SilenceMatcher `json:"matchers"`
	CreatedBy string           `json:"createdBy"`
	Comment   string           `json:"comment"`
}

type SilenceStatus struct {
	State string `json:"state"`
}

type SilenceMatcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

func (c *AlertmanagerClient) Status(ctx context.Context) (AlertmanagerStatus, error) {
	endpoint, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/-/ready")
	if err != nil {
		return AlertmanagerStatus{}, fmt.Errorf("build Alertmanager readiness URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return AlertmanagerStatus{}, fmt.Errorf("build Alertmanager readiness request: %w", err)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return AlertmanagerStatus{}, fmt.Errorf("query Alertmanager readiness: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return AlertmanagerStatus{}, fmt.Errorf("query Alertmanager readiness: unexpected HTTP %s", resp.Status)
	}

	alerts, err := c.ActiveTenantAlerts(ctx, TenantOne)
	if err != nil {
		return AlertmanagerStatus{}, err
	}
	return AlertmanagerStatus{Ready: true, ActiveTenantAlerts: len(alerts)}, nil
}

func (c *AlertmanagerClient) ActiveTenantAlerts(ctx context.Context, tenant string) ([]Alert, error) {
	endpoint, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/api/v2/alerts")
	if err != nil {
		return nil, fmt.Errorf("build Alertmanager alerts URL: %w", err)
	}

	query := endpoint.Query()
	query.Set("active", "true")
	query.Set("silenced", "false")
	query.Set("inhibited", "false")
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build Alertmanager request: %w", err)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("query Alertmanager: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query Alertmanager: unexpected HTTP %s", resp.Status)
	}

	var alerts []Alert
	if err := json.NewDecoder(resp.Body).Decode(&alerts); err != nil {
		return nil, fmt.Errorf("decode Alertmanager alerts: %w", err)
	}

	filtered := alerts[:0]
	for _, alert := range alerts {
		if alert.label("tenant") == tenant && alert.label("kind") != "notify" {
			filtered = append(filtered, alert)
		}
	}
	return filtered, nil
}

func (c *AlertmanagerClient) ActiveSilences(ctx context.Context) ([]AlertmanagerSilence, error) {
	endpoint, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/api/v2/silences")
	if err != nil {
		return nil, fmt.Errorf("build Alertmanager silences URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build Alertmanager silences request: %w", err)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("query Alertmanager silences: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query Alertmanager silences: unexpected HTTP %s", resp.Status)
	}

	var silences []AlertmanagerSilence
	if err := json.NewDecoder(resp.Body).Decode(&silences); err != nil {
		return nil, fmt.Errorf("decode Alertmanager silences: %w", err)
	}

	filtered := silences[:0]
	for _, silence := range silences {
		if strings.EqualFold(silence.Status.State, "active") && silenceTargetsTenant(silence, TenantOne) {
			filtered = append(filtered, silence)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		left, right := filtered[i], filtered[j]
		if !left.EndsAt.Equal(right.EndsAt) {
			return left.EndsAt.Before(right.EndsAt)
		}
		return left.ID < right.ID
	})
	return filtered, nil
}

func (c *AlertmanagerClient) SilenceAlert(ctx context.Context, alert Alert, duration time.Duration, createdBy, comment string) (Silence, error) {
	if duration <= 0 {
		return Silence{}, fmt.Errorf("silence duration must be positive")
	}
	if alert.Fingerprint == "" {
		return Silence{}, fmt.Errorf("alert fingerprint is required")
	}
	if len(alert.Labels) == 0 {
		return Silence{}, fmt.Errorf("alert labels are required")
	}

	now := c.now()
	silence := Silence{
		StartsAt:  now,
		EndsAt:    now.Add(duration),
		AlertID:   alert.Fingerprint,
		Matchers:  exactMatchers(alert.Labels),
		CreatedBy: createdBy,
		Comment:   comment,
	}
	if strings.TrimSpace(silence.CreatedBy) == "" {
		silence.CreatedBy = "alert-list-bot"
	}
	if strings.TrimSpace(silence.Comment) == "" {
		silence.Comment = "Silenced from Telegram for active alert " + alert.Fingerprint
	}
	return c.createSilence(ctx, silence)
}

func (c *AlertmanagerClient) SilenceMatchers(ctx context.Context, matchers []SilenceMatcher, duration time.Duration, createdBy, comment string) (Silence, error) {
	if duration <= 0 {
		return Silence{}, fmt.Errorf("silence duration must be positive")
	}
	if len(matchers) == 0 {
		return Silence{}, fmt.Errorf("silence matchers are required")
	}

	now := c.now()
	silence := Silence{
		StartsAt:  now,
		EndsAt:    now.Add(duration),
		Matchers:  normalizeMatchers(matchers),
		CreatedBy: createdBy,
		Comment:   comment,
	}
	if strings.TrimSpace(silence.CreatedBy) == "" {
		silence.CreatedBy = "alert-list-bot"
	}
	if strings.TrimSpace(silence.Comment) == "" {
		silence.Comment = "Silenced from Telegram by label matchers"
	}
	return c.createSilence(ctx, silence)
}

func (c *AlertmanagerClient) createSilence(ctx context.Context, silence Silence) (Silence, error) {
	endpoint, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/api/v2/silences")
	if err != nil {
		return Silence{}, fmt.Errorf("build Alertmanager silences URL: %w", err)
	}

	payload := struct {
		Matchers  []SilenceMatcher `json:"matchers"`
		StartsAt  time.Time        `json:"startsAt"`
		EndsAt    time.Time        `json:"endsAt"`
		CreatedBy string           `json:"createdBy"`
		Comment   string           `json:"comment"`
	}{
		Matchers:  silence.Matchers,
		StartsAt:  silence.StartsAt,
		EndsAt:    silence.EndsAt,
		CreatedBy: silence.CreatedBy,
		Comment:   silence.Comment,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Silence{}, fmt.Errorf("encode Alertmanager silence: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return Silence{}, fmt.Errorf("build Alertmanager silence request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Silence{}, fmt.Errorf("create Alertmanager silence: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Silence{}, fmt.Errorf("create Alertmanager silence: unexpected HTTP %s", resp.Status)
	}
	var result struct {
		SilenceID string `json:"silenceID"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Silence{}, fmt.Errorf("decode Alertmanager silence response: %w", err)
	}
	if result.SilenceID == "" {
		return Silence{}, fmt.Errorf("Alertmanager silence response did not include silenceID")
	}
	silence.ID = result.SilenceID
	return silence, nil
}

func (c *AlertmanagerClient) ExpireSilence(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("silence id is required")
	}

	endpoint, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/api/v2/silence/" + url.PathEscape(id))
	if err != nil {
		return fmt.Errorf("build Alertmanager expire silence URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("build Alertmanager expire silence request: %w", err)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("expire Alertmanager silence: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("expire Alertmanager silence: unexpected HTTP %s", resp.Status)
	}
	return nil
}

func silenceTargetsTenant(silence AlertmanagerSilence, tenant string) bool {
	foundTenantMatcher := false
	for _, matcher := range silence.Matchers {
		if matcher.Name != "tenant" {
			continue
		}
		foundTenantMatcher = true
		if matcher.IsRegex {
			matched, err := regexp.MatchString(matcher.Value, tenant)
			if err != nil {
				return false
			}
			if matcher.IsEqual {
				return matched
			}
			return !matched
		}
		if matcher.IsEqual {
			return matcher.Value == tenant
		}
		return matcher.Value != tenant
	}
	return !foundTenantMatcher
}

func exactMatchers(labels map[string]string) []SilenceMatcher {
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)

	matchers := make([]SilenceMatcher, 0, len(names))
	for _, name := range names {
		matchers = append(matchers, SilenceMatcher{
			Name:    name,
			Value:   labels[name],
			IsEqual: true,
		})
	}
	return matchers
}

func normalizeMatchers(matchers []SilenceMatcher) []SilenceMatcher {
	normalized := make([]SilenceMatcher, 0, len(matchers))
	for _, matcher := range matchers {
		normalized = append(normalized, SilenceMatcher{
			Name:    strings.TrimSpace(matcher.Name),
			Value:   strings.TrimSpace(matcher.Value),
			IsRegex: matcher.IsRegex,
			IsEqual: matcher.IsEqual,
		})
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Name != normalized[j].Name {
			return normalized[i].Name < normalized[j].Name
		}
		return normalized[i].Value < normalized[j].Value
	})
	return normalized
}

func (c *AlertmanagerClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *AlertmanagerClient) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}
