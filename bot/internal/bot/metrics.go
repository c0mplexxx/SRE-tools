package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type prometheusQueryResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []prometheusSample `json:"result"`
	} `json:"data"`
}

type prometheusSample struct {
	Metric map[string]string `json:"metric"`
	Value  []any             `json:"value"`
}

func (c *AlertmanagerClient) CheckInstance(ctx context.Context, tenant, instance, window string) (InstanceCheck, error) {
	tenant = strings.TrimSpace(tenant)
	instance = strings.TrimSpace(instance)
	window = strings.TrimSpace(window)
	if tenant == "" {
		return InstanceCheck{}, fmt.Errorf("tenant is required")
	}
	if instance == "" {
		return InstanceCheck{}, fmt.Errorf("instance is required")
	}
	if window == "" {
		return InstanceCheck{}, fmt.Errorf("check window is required")
	}
	baseURL := strings.TrimSpace(c.MetricsBaseURLs[tenant])
	if baseURL == "" {
		return InstanceCheck{}, fmt.Errorf("metrics URL for tenant %s is not configured", tenant)
	}

	selector := strconv.Quote(instance)
	check := InstanceCheck{Tenant: tenant, Instance: instance, Window: window}
	var err error
	if check.Up, err = c.queryNumber(ctx, baseURL, fmt.Sprintf(`up{job="node_exporter",instance=%s}`, selector)); err != nil {
		return InstanceCheck{}, err
	}
	if check.CPUUsagePercent, err = c.queryNumber(ctx, baseURL, fmt.Sprintf(`100 * (1 - avg(rate(node_cpu_seconds_total{job="node_exporter",instance=%s,mode="idle"}[%s])))`, selector, window)); err != nil {
		return InstanceCheck{}, err
	}
	if check.CPUCores, err = c.queryNumber(ctx, baseURL, fmt.Sprintf(`count(count by (cpu) (node_cpu_seconds_total{job="node_exporter",instance=%s,mode="idle"}))`, selector)); err != nil {
		return InstanceCheck{}, err
	}
	if check.Load1, err = c.queryNumber(ctx, baseURL, fmt.Sprintf(`node_load1{job="node_exporter",instance=%s}`, selector)); err != nil {
		return InstanceCheck{}, err
	}
	if check.Load5, err = c.queryNumber(ctx, baseURL, fmt.Sprintf(`node_load5{job="node_exporter",instance=%s}`, selector)); err != nil {
		return InstanceCheck{}, err
	}
	if check.Load15, err = c.queryNumber(ctx, baseURL, fmt.Sprintf(`node_load15{job="node_exporter",instance=%s}`, selector)); err != nil {
		return InstanceCheck{}, err
	}
	if check.MemoryPercent, err = c.queryNumber(ctx, baseURL, fmt.Sprintf(`100 * (1 - (node_memory_MemAvailable_bytes{job="node_exporter",instance=%s} / node_memory_MemTotal_bytes{job="node_exporter",instance=%s}))`, selector, selector)); err != nil {
		return InstanceCheck{}, err
	}
	if check.MemoryUsedBytes, err = c.queryNumber(ctx, baseURL, fmt.Sprintf(`node_memory_MemTotal_bytes{job="node_exporter",instance=%s} - node_memory_MemAvailable_bytes{job="node_exporter",instance=%s}`, selector, selector)); err != nil {
		return InstanceCheck{}, err
	}
	if check.MemoryTotalBytes, err = c.queryNumber(ctx, baseURL, fmt.Sprintf(`node_memory_MemTotal_bytes{job="node_exporter",instance=%s}`, selector)); err != nil {
		return InstanceCheck{}, err
	}
	if check.DiskUsage, err = c.queryNamed(ctx, baseURL, fmt.Sprintf(`topk(3, 100 * (1 - (node_filesystem_avail_bytes{job="node_exporter",instance=%s,fstype!~"^(tmpfs|devtmpfs|overlay|squashfs|iso9660|fuse.lxcfs|nsfs|proc|sysfs|cgroup2?)$",mountpoint!~"^/(run|var/lib/docker|snap)($|/)"} / node_filesystem_size_bytes{job="node_exporter",instance=%s,fstype!~"^(tmpfs|devtmpfs|overlay|squashfs|iso9660|fuse.lxcfs|nsfs|proc|sysfs|cgroup2?)$",mountpoint!~"^/(run|var/lib/docker|snap)($|/)"})))`, selector, selector), "mountpoint", "device"); err != nil {
		return InstanceCheck{}, err
	}
	if check.DiskIOBusy, err = c.queryNamed(ctx, baseURL, fmt.Sprintf(`topk(3, rate(node_disk_io_time_seconds_total{job="node_exporter",instance=%s,device!~"^(loop|ram|fd).*|^sr[0-9]+$"}[%s]) * 100)`, selector, window), "device"); err != nil {
		return InstanceCheck{}, err
	}
	if check.NetworkReceive, err = c.queryNamed(ctx, baseURL, fmt.Sprintf(`topk(2, rate(node_network_receive_bytes_total{job="node_exporter",instance=%s,device!~"^(lo|docker.*|veth.*|br-.*|virbr.*)$"}[%s]) * 8)`, selector, window), "device"); err != nil {
		return InstanceCheck{}, err
	}
	if check.NetworkTransmit, err = c.queryNamed(ctx, baseURL, fmt.Sprintf(`topk(2, rate(node_network_transmit_bytes_total{job="node_exporter",instance=%s,device!~"^(lo|docker.*|veth.*|br-.*|virbr.*)$"}[%s]) * 8)`, selector, window), "device"); err != nil {
		return InstanceCheck{}, err
	}
	return check, nil
}

func (c *AlertmanagerClient) queryNumber(ctx context.Context, baseURL, query string) (*float64, error) {
	samples, err := c.queryPrometheus(ctx, baseURL, query)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, nil
	}
	value, ok := sampleFloat(samples[0])
	if !ok {
		return nil, nil
	}
	return &value, nil
}

func (c *AlertmanagerClient) queryNamed(ctx context.Context, baseURL, query string, labels ...string) ([]MetricValue, error) {
	samples, err := c.queryPrometheus(ctx, baseURL, query)
	if err != nil {
		return nil, err
	}
	values := make([]MetricValue, 0, len(samples))
	for _, sample := range samples {
		value, ok := sampleFloat(sample)
		if !ok {
			continue
		}
		name := firstMetricLabel(sample.Metric, labels...)
		if name == "" {
			name = "unknown"
		}
		values = append(values, MetricValue{Name: name, Value: value})
	}
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].Value != values[j].Value {
			return values[i].Value > values[j].Value
		}
		return values[i].Name < values[j].Name
	})
	return values, nil
}

func (c *AlertmanagerClient) queryPrometheus(ctx context.Context, baseURL, query string) ([]prometheusSample, error) {
	endpoint, err := url.Parse(strings.TrimRight(baseURL, "/") + "/api/v1/query")
	if err != nil {
		return nil, fmt.Errorf("build metrics query URL: %w", err)
	}
	params := endpoint.Query()
	params.Set("query", query)
	endpoint.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build metrics query request: %w", err)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("query metrics datasource: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query metrics datasource: unexpected HTTP %s", resp.Status)
	}

	var result prometheusQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode metrics query response: %w", err)
	}
	if result.Status != "success" {
		if strings.TrimSpace(result.Error) != "" {
			return nil, fmt.Errorf("query metrics datasource: %s", result.Error)
		}
		return nil, fmt.Errorf("query metrics datasource: status %q", result.Status)
	}
	return result.Data.Result, nil
}

func sampleFloat(sample prometheusSample) (float64, bool) {
	if len(sample.Value) < 2 {
		return 0, false
	}
	raw, ok := sample.Value[1].(string)
	if !ok {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

func firstMetricLabel(metric map[string]string, labels ...string) string {
	for _, label := range labels {
		if value := strings.TrimSpace(metric[label]); value != "" {
			return value
		}
	}
	return ""
}
