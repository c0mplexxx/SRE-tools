package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	graphUnitPercent = "%"
	graphUnitLoad    = "load"
	graphUnitBits    = "bit/s"
)

type prometheusRangeQueryResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []prometheusRangeSeries `json:"result"`
	} `json:"data"`
}

type prometheusRangeSeries struct {
	Metric map[string]string `json:"metric"`
	Values [][]any           `json:"values"`
}

type graphRangeQuery struct {
	Query     string
	NameLabel string
	FixedName string
}

func (c *AlertmanagerClient) GraphInstance(ctx context.Context, tenant, command, instance string, window GraphRange) (InstanceGraph, error) {
	tenant = strings.TrimSpace(tenant)
	command = strings.TrimSpace(command)
	instance = strings.TrimSpace(instance)
	if tenant == "" {
		return InstanceGraph{}, fmt.Errorf("tenant is required")
	}
	if instance == "" {
		return InstanceGraph{}, fmt.Errorf("instance is required")
	}
	if window.Duration <= 0 || window.Step <= 0 {
		return InstanceGraph{}, fmt.Errorf("graph range is required")
	}
	baseURL := strings.TrimSpace(c.MetricsBaseURLs[tenant])
	if baseURL == "" {
		return InstanceGraph{}, fmt.Errorf("metrics URL for tenant %s is not configured", tenant)
	}
	if window.End.IsZero() {
		window.End = c.now()
	}
	if window.Start.IsZero() {
		window.Start = window.End.Add(-window.Duration)
	}
	if window.RateWindow == "" {
		window.RateWindow = prometheusDuration(maxDuration(time.Minute, minDuration(time.Hour, window.Step*4)))
	}

	queries, graph, err := graphQueries(command, instance, window, func(query string, labels ...string) ([]MetricValue, error) {
		return c.queryNamed(ctx, baseURL, query, labels...)
	})
	if err != nil {
		return InstanceGraph{}, err
	}
	graph.Tenant = tenant
	graph.Command = command
	graph.Instance = instance
	graph.Range = window

	for _, query := range queries {
		series, err := c.queryRange(ctx, baseURL, query.Query, window)
		if err != nil {
			return InstanceGraph{}, err
		}
		graph.Series = append(graph.Series, rangeSeriesToGraph(series, query.NameLabel, query.FixedName)...)
	}
	return graph, nil
}

func graphQueries(command, instance string, window GraphRange, top func(string, ...string) ([]MetricValue, error)) ([]graphRangeQuery, InstanceGraph, error) {
	selector := strconv.Quote(instance)
	rateWindow := window.RateWindow
	switch command {
	case "/cpu":
		return []graphRangeQuery{{
			Query:     fmt.Sprintf(`100 * (1 - avg(rate(node_cpu_seconds_total{job="node_exporter",instance=%s,mode="idle"}[%s])))`, selector, rateWindow),
			FixedName: "cpu used",
		}}, InstanceGraph{Title: "CPU usage", Unit: graphUnitPercent}, nil
	case "/mem":
		return []graphRangeQuery{{
			Query:     fmt.Sprintf(`100 * (1 - (node_memory_MemAvailable_bytes{job="node_exporter",instance=%s} / node_memory_MemTotal_bytes{job="node_exporter",instance=%s}))`, selector, selector),
			FixedName: "memory used",
		}}, InstanceGraph{Title: "Memory usage", Unit: graphUnitPercent}, nil
	case "/la":
		return []graphRangeQuery{{
			Query:     fmt.Sprintf(`{__name__=~"node_load1|node_load5|node_load15",job="node_exporter",instance=%s}`, selector),
			NameLabel: "__name__",
		}}, InstanceGraph{Title: "Load average", Unit: graphUnitLoad}, nil
	case "/swap":
		return []graphRangeQuery{{
			Query:     fmt.Sprintf(`100 * (1 - (node_memory_SwapFree_bytes{job="node_exporter",instance=%s} / node_memory_SwapTotal_bytes{job="node_exporter",instance=%s}))`, selector, selector),
			FixedName: "swap used",
		}}, InstanceGraph{Title: "Swap usage", Unit: graphUnitPercent, EmptyMessage: "No swap data for this instance/range."}, nil
	case "/space":
		query := fmt.Sprintf(`topk(3, 100 * (1 - (node_filesystem_avail_bytes{job="node_exporter",instance=%s,fstype!~"^(tmpfs|devtmpfs|overlay|squashfs|iso9660|fuse.lxcfs|nsfs|proc|sysfs|cgroup2?)$",mountpoint!~"^/(run|var/lib/docker|snap)($|/)"} / node_filesystem_size_bytes{job="node_exporter",instance=%s,fstype!~"^(tmpfs|devtmpfs|overlay|squashfs|iso9660|fuse.lxcfs|nsfs|proc|sysfs|cgroup2?)$",mountpoint!~"^/(run|var/lib/docker|snap)($|/)"})))`, selector, selector)
		values, err := top(query, "mountpoint", "device")
		if err != nil {
			return nil, InstanceGraph{}, err
		}
		queries := make([]graphRangeQuery, 0, len(values))
		for _, value := range limitMetricValues(values, 3) {
			mountpoint := strconv.Quote(value.Name)
			queries = append(queries, graphRangeQuery{
				Query:     fmt.Sprintf(`100 * (1 - (node_filesystem_avail_bytes{job="node_exporter",instance=%s,mountpoint=%s} / node_filesystem_size_bytes{job="node_exporter",instance=%s,mountpoint=%s}))`, selector, mountpoint, selector, mountpoint),
				FixedName: value.Name,
			})
		}
		return queries, InstanceGraph{Title: "Filesystem usage", Unit: graphUnitPercent, EmptyMessage: "No filesystem data for this instance/range."}, nil
	case "/io":
		query := fmt.Sprintf(`topk(3, rate(node_disk_io_time_seconds_total{job="node_exporter",instance=%s,device!~"^(loop|ram|fd).*|^sr[0-9]+$"}[%s]) * 100)`, selector, rateWindow)
		values, err := top(query, "device")
		if err != nil {
			return nil, InstanceGraph{}, err
		}
		queries := make([]graphRangeQuery, 0, len(values))
		for _, value := range limitMetricValues(values, 3) {
			device := strconv.Quote(value.Name)
			queries = append(queries, graphRangeQuery{
				Query:     fmt.Sprintf(`rate(node_disk_io_time_seconds_total{job="node_exporter",instance=%s,device=%s}[%s]) * 100`, selector, device, rateWindow),
				FixedName: value.Name,
			})
		}
		return queries, InstanceGraph{Title: "Disk IO busy", Unit: graphUnitPercent, EmptyMessage: "No disk IO data for this instance/range."}, nil
	case "/rx", "/tx":
		metric := "node_network_receive_bytes_total"
		title := "Network receive"
		if command == "/tx" {
			metric = "node_network_transmit_bytes_total"
			title = "Network transmit"
		}
		query := fmt.Sprintf(`topk(3, rate(%s{job="node_exporter",instance=%s,device!~"^(lo|docker.*|veth.*|br-.*|virbr.*)$"}[%s]) * 8)`, metric, selector, rateWindow)
		values, err := top(query, "device")
		if err != nil {
			return nil, InstanceGraph{}, err
		}
		queries := make([]graphRangeQuery, 0, len(values))
		for _, value := range limitMetricValues(values, 3) {
			device := strconv.Quote(value.Name)
			queries = append(queries, graphRangeQuery{
				Query:     fmt.Sprintf(`rate(%s{job="node_exporter",instance=%s,device=%s}[%s]) * 8`, metric, selector, device, rateWindow),
				FixedName: value.Name,
			})
		}
		return queries, InstanceGraph{Title: title, Unit: graphUnitBits, EmptyMessage: "No network data for this instance/range."}, nil
	default:
		return nil, InstanceGraph{}, fmt.Errorf("unsupported graph command %q", command)
	}
}

func (c *AlertmanagerClient) queryRange(ctx context.Context, baseURL, query string, window GraphRange) ([]prometheusRangeSeries, error) {
	endpoint, err := url.Parse(strings.TrimRight(baseURL, "/") + "/api/v1/query_range")
	if err != nil {
		return nil, fmt.Errorf("build metrics range query URL: %w", err)
	}
	params := endpoint.Query()
	params.Set("query", query)
	params.Set("start", strconv.FormatFloat(float64(window.Start.Unix()), 'f', -1, 64))
	params.Set("end", strconv.FormatFloat(float64(window.End.Unix()), 'f', -1, 64))
	params.Set("step", prometheusDuration(window.Step))
	endpoint.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build metrics range query request: %w", err)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("query metrics range datasource: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query metrics range datasource: unexpected HTTP %s", resp.Status)
	}

	var result prometheusRangeQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode metrics range query response: %w", err)
	}
	if result.Status != "success" {
		if strings.TrimSpace(result.Error) != "" {
			return nil, fmt.Errorf("query metrics range datasource: %s", result.Error)
		}
		return nil, fmt.Errorf("query metrics range datasource: status %q", result.Status)
	}
	return result.Data.Result, nil
}

func rangeSeriesToGraph(series []prometheusRangeSeries, nameLabel, fixedName string) []GraphSeries {
	out := make([]GraphSeries, 0, len(series))
	for _, item := range series {
		name := strings.TrimSpace(fixedName)
		if name == "" {
			name = firstMetricLabel(item.Metric, nameLabel, "mountpoint", "device", "__name__")
		}
		if name == "" {
			name = "value"
		}
		points := make([]GraphPoint, 0, len(item.Values))
		for _, raw := range item.Values {
			point, ok := rangePoint(raw)
			if ok {
				points = append(points, point)
			}
		}
		out = append(out, GraphSeries{Name: name, Points: points})
	}
	return out
}

func rangePoint(raw []any) (GraphPoint, bool) {
	if len(raw) < 2 {
		return GraphPoint{}, false
	}
	timestamp, ok := anyFloat(raw[0])
	if !ok {
		return GraphPoint{}, false
	}
	valueRaw, ok := raw[1].(string)
	if !ok {
		return GraphPoint{}, false
	}
	value, err := strconv.ParseFloat(valueRaw, 64)
	valid := err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
	return GraphPoint{
		Time:  time.Unix(int64(timestamp), int64((timestamp-math.Trunc(timestamp))*1e9)).UTC(),
		Value: value,
		Valid: valid,
	}, true
}

func anyFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func limitMetricValues(values []MetricValue, limit int) []MetricValue {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}
