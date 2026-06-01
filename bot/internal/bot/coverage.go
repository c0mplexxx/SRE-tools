package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type vmalertRulesResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Groups []vmalertRuleGroup `json:"groups"`
	} `json:"data"`
}

type vmalertRuleGroup struct {
	Rules []vmalertRule `json:"rules"`
}

type vmalertRule struct {
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Query  string            `json:"query"`
	Labels map[string]string `json:"labels"`
}

var instanceMatcherPattern = regexp.MustCompile(`\binstance\s*(=~|!~|!=|=)\s*("(?:\\.|[^"\\])*")`)

func (c *AlertmanagerClient) CoverageInstance(ctx context.Context, tenant, instance string) (InstanceCoverage, error) {
	tenant = strings.TrimSpace(tenant)
	instance = strings.TrimSpace(instance)
	if tenant == "" {
		return InstanceCoverage{}, fmt.Errorf("tenant is required")
	}
	if instance == "" {
		return InstanceCoverage{}, fmt.Errorf("instance is required")
	}

	vmalertURL := strings.TrimSpace(c.VmalertBaseURLs[tenant])
	if vmalertURL == "" {
		return InstanceCoverage{}, fmt.Errorf("vmalert URL for tenant %s is not configured", tenant)
	}
	metricsURL := strings.TrimSpace(c.MetricsBaseURLs[tenant])
	if metricsURL == "" {
		return InstanceCoverage{}, fmt.Errorf("metrics URL for tenant %s is not configured", tenant)
	}

	rules, err := c.fetchVmalertRules(ctx, vmalertURL)
	if err != nil {
		return InstanceCoverage{}, err
	}

	covered := make(map[string]struct{})
	probeCache := make(map[string]bool)
	for _, rule := range rules {
		alertname := strings.TrimSpace(rule.Name)
		if alertname == "" || rule.Type != "alerting" {
			continue
		}
		if strings.TrimSpace(rule.Labels["kind"]) == "notify" {
			continue
		}
		if ruleTenant := strings.TrimSpace(rule.Labels["tenant"]); ruleTenant != "" && ruleTenant != tenant {
			continue
		}

		if labelInstance := strings.TrimSpace(rule.Labels["instance"]); labelInstance != "" {
			if labelInstance == instance {
				covered[alertname] = struct{}{}
			}
			if !strings.Contains(labelInstance, "{{") && !strings.Contains(labelInstance, "$labels.") {
				continue
			}
		}

		ok, err := c.ruleCoveredBySourceMetric(ctx, metricsURL, instance, rule, probeCache)
		if err != nil {
			return InstanceCoverage{}, err
		}
		if ok {
			covered[alertname] = struct{}{}
		}
	}

	alertnames := make([]string, 0, len(covered))
	for alertname := range covered {
		alertnames = append(alertnames, alertname)
	}
	sort.Strings(alertnames)
	return InstanceCoverage{Tenant: tenant, Instance: instance, Alertnames: alertnames}, nil
}

func (c *AlertmanagerClient) fetchVmalertRules(ctx context.Context, baseURL string) ([]vmalertRule, error) {
	endpoint, err := url.Parse(strings.TrimRight(baseURL, "/") + "/api/v1/rules")
	if err != nil {
		return nil, fmt.Errorf("build vmalert rules URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build vmalert rules request: %w", err)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("query vmalert rules: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query vmalert rules: unexpected HTTP %s", resp.Status)
	}

	var result vmalertRulesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode vmalert rules response: %w", err)
	}
	if result.Status != "success" {
		if strings.TrimSpace(result.Error) != "" {
			return nil, fmt.Errorf("query vmalert rules: %s", result.Error)
		}
		return nil, fmt.Errorf("query vmalert rules: status %q", result.Status)
	}

	var rules []vmalertRule
	for _, group := range result.Data.Groups {
		rules = append(rules, group.Rules...)
	}
	return rules, nil
}

func (c *AlertmanagerClient) ruleCoveredBySourceMetric(ctx context.Context, metricsURL, instance string, rule vmalertRule, cache map[string]bool) (bool, error) {
	if !ruleQueryAllowsInstance(rule.Query, instance) {
		return false, nil
	}

	query := strings.ToLower(rule.Query)
	selector := strconv.Quote(instance)

	switch {
	case strings.Contains(query, "up{") || strings.Contains(query, "up {"):
		return c.hasSourceSeries(ctx, metricsURL, fmt.Sprintf(`up{instance=%s}`, selector), cache)
	case strings.Contains(query, "node_systemd_unit_state"):
		return c.hasSourceSeries(ctx, metricsURL, fmt.Sprintf(`node_systemd_unit_state{job="node_exporter",instance=%s,state="active"}`, selector), cache)
	case strings.Contains(query, "node_memory_swaptotal_bytes"):
		return c.hasPositiveSourceValue(ctx, metricsURL, fmt.Sprintf(`node_memory_SwapTotal_bytes{job="node_exporter",instance=%s}`, selector), cache)
	case strings.Contains(query, "node_load5") && strings.Contains(query, "node_cpu_seconds_total"):
		return c.hasAllSourceSeries(ctx, metricsURL, cache,
			fmt.Sprintf(`node_load5{job="node_exporter",instance=%s}`, selector),
			fmt.Sprintf(`node_cpu_seconds_total{job="node_exporter",instance=%s}`, selector),
		)
	case strings.Contains(query, "haproxy_"):
		return c.hasSourceSeries(ctx, metricsURL, fmt.Sprintf(`{__name__=~"haproxy_.+",job="haproxy",instance=%s}`, selector), cache)
	case strings.Contains(query, "node_ethtool_") || strings.Contains(query, "node_network_"):
		return c.hasAnySourceSeries(ctx, metricsURL, cache,
			fmt.Sprintf(`{__name__=~"node_ethtool_.+",instance=%s}`, selector),
			fmt.Sprintf(`{__name__=~"node_network_.+",instance=%s}`, selector),
		)
	case strings.Contains(query, "node_filesystem_") || strings.Contains(query, "node_disk_"):
		return c.hasAnySourceSeries(ctx, metricsURL, cache,
			fmt.Sprintf(`{__name__=~"node_filesystem_.+",job="node_exporter",instance=%s}`, selector),
			fmt.Sprintf(`{__name__=~"node_disk_.+",job="node_exporter",instance=%s}`, selector),
		)
	case strings.Contains(query, "node_memory_") || strings.Contains(query, "node_vmstat_"):
		return c.hasAnySourceSeries(ctx, metricsURL, cache,
			fmt.Sprintf(`{__name__=~"node_memory_.+",job="node_exporter",instance=%s}`, selector),
			fmt.Sprintf(`node_vmstat_oom_kill{job="node_exporter",instance=%s}`, selector),
			fmt.Sprintf(`node_vmstat_pgmajfault{job="node_exporter",instance=%s}`, selector),
		)
	default:
		return false, nil
	}
}

func ruleQueryAllowsInstance(query, instance string) bool {
	matches := instanceMatcherPattern.FindAllStringSubmatch(query, -1)
	if len(matches) == 0 {
		return true
	}

	hasPositiveMatcher := false
	positiveMatched := false
	for _, match := range matches {
		if len(match) != 3 {
			continue
		}
		operator := match[1]
		value, err := strconv.Unquote(match[2])
		if err != nil {
			return false
		}

		switch operator {
		case "=":
			hasPositiveMatcher = true
			if instance == value {
				positiveMatched = true
			}
		case "=~":
			hasPositiveMatcher = true
			matched, err := regexp.MatchString(value, instance)
			if err != nil {
				return false
			}
			if matched {
				positiveMatched = true
			}
		case "!=":
			if instance == value {
				return false
			}
		case "!~":
			matched, err := regexp.MatchString(value, instance)
			if err != nil {
				return false
			}
			if matched {
				return false
			}
		}
	}
	return !hasPositiveMatcher || positiveMatched
}

func (c *AlertmanagerClient) hasAllSourceSeries(ctx context.Context, baseURL string, cache map[string]bool, queries ...string) (bool, error) {
	for _, query := range queries {
		ok, err := c.hasSourceSeries(ctx, baseURL, query, cache)
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

func (c *AlertmanagerClient) hasAnySourceSeries(ctx context.Context, baseURL string, cache map[string]bool, queries ...string) (bool, error) {
	for _, query := range queries {
		ok, err := c.hasSourceSeries(ctx, baseURL, query, cache)
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

func (c *AlertmanagerClient) hasSourceSeries(ctx context.Context, baseURL, query string, cache map[string]bool) (bool, error) {
	cacheKey := "series:" + query
	if ok, found := cache[cacheKey]; found {
		return ok, nil
	}
	samples, err := c.queryPrometheus(ctx, baseURL, query)
	if err != nil {
		return false, err
	}
	ok := len(samples) > 0
	cache[cacheKey] = ok
	return ok, nil
}

func (c *AlertmanagerClient) hasPositiveSourceValue(ctx context.Context, baseURL, query string, cache map[string]bool) (bool, error) {
	cacheKey := "positive:" + query
	if ok, found := cache[cacheKey]; found {
		return ok, nil
	}
	samples, err := c.queryPrometheus(ctx, baseURL, query)
	if err != nil {
		return false, err
	}
	for _, sample := range samples {
		value, ok := sampleFloat(sample)
		if ok && value > 0 {
			cache[cacheKey] = true
			return true, nil
		}
	}
	cache[cacheKey] = false
	return false, nil
}
