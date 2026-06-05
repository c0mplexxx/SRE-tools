package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	graphUnitPercent = "%"
	graphUnitLoad    = "load"
	graphUnitBits    = "bit/s"

	DefaultGraphRegexHostLimit = 6
)

var graphRegexMetaPattern = regexp.MustCompile(`[\\.\+\*\?\^\$\[\]\(\)\{\}\|]`)

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

type operatorCommandError struct {
	Message string
}

func (e operatorCommandError) Error() string {
	return e.Message
}

func operatorCommandErrorMessage(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if typed, ok := err.(operatorCommandError); ok {
		return typed.Message, true
	}
	if typed, ok := err.(*operatorCommandError); ok {
		return typed.Message, true
	}
	return "", false
}

func (c *AlertmanagerClient) GraphInstance(ctx context.Context, tenant, command, targetRaw string, window GraphRange) (InstanceGraph, error) {
	tenant = strings.TrimSpace(tenant)
	command = strings.TrimSpace(command)
	target, err := parseGraphTarget(targetRaw)
	if err != nil {
		return InstanceGraph{}, err
	}
	if tenant == "" {
		return InstanceGraph{}, fmt.Errorf("tenant is required")
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

	target, err = c.resolveGraphTarget(ctx, baseURL, target)
	if err != nil {
		return InstanceGraph{}, err
	}

	queries, graph, err := graphQueries(command, target, window, func(query string, labels ...string) ([]MetricValue, error) {
		return c.queryNamed(ctx, baseURL, query, labels...)
	})
	if err != nil {
		return InstanceGraph{}, err
	}
	graph.Tenant = tenant
	graph.Command = command
	graph.Instance = target.Raw
	graph.Target = target
	graph.Range = window
	if graph.EmptyMessage == "" {
		graph.EmptyMessage = graphEmptyMessage(graph.Title, target, window)
	}

	for _, query := range queries {
		series, err := c.queryRange(ctx, baseURL, query.Query, window)
		if err != nil {
			return InstanceGraph{}, err
		}
		graph.Series = append(graph.Series, rangeSeriesToGraph(series, query.NameLabel, query.FixedName)...)
	}
	return graph, nil
}

func parseGraphTarget(value string) (GraphTarget, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return GraphTarget{}, operatorCommandError{Message: "Graph target is required."}
	}
	target := GraphTarget{Raw: value, HostCap: DefaultGraphRegexHostLimit}
	if !graphRegexMetaPattern.MatchString(value) {
		target.Matcher = `instance=` + strconv.Quote(value)
		return target, nil
	}
	if _, err := regexp.Compile(value); err != nil {
		return GraphTarget{}, operatorCommandError{Message: "Invalid instance regex " + strconv.Quote(value) + ". Fix the regex or use an exact instance name."}
	}
	target.Regex = true
	target.Matcher = `instance=~` + strconv.Quote(value)
	return target, nil
}

func (c *AlertmanagerClient) resolveGraphTarget(ctx context.Context, baseURL string, target GraphTarget) (GraphTarget, error) {
	if !target.Regex {
		return target, nil
	}
	hostCap := target.HostCap
	if hostCap <= 0 {
		hostCap = DefaultGraphRegexHostLimit
		target.HostCap = hostCap
	}
	values, err := c.queryNamed(ctx, baseURL, fmt.Sprintf(`group by (instance) (up{job="node_exporter",%s})`, target.Matcher), "instance")
	if err != nil {
		return GraphTarget{}, err
	}
	target.Hosts = uniqueMetricLabelValues(values, "instance")
	if len(target.Hosts) > hostCap {
		return GraphTarget{}, operatorCommandError{Message: fmt.Sprintf("Regex /%s/ matched %d hosts; narrow the regex to %d or fewer hosts.", target.Raw, len(target.Hosts), hostCap)}
	}
	return target, nil
}

func uniqueMetricLabelValues(values []MetricValue, label string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		item := strings.TrimSpace(value.Labels[label])
		if item == "" {
			item = strings.TrimSpace(value.Name)
		}
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func graphQueries(command string, target GraphTarget, window GraphRange, top func(string, ...string) ([]MetricValue, error)) ([]graphRangeQuery, InstanceGraph, error) {
	selector := target.Matcher
	rateWindow := window.RateWindow
	switch command {
	case "/cpu":
		if target.Regex {
			return []graphRangeQuery{{
				Query:     fmt.Sprintf(`100 * (1 - avg by (instance) (rate(node_cpu_seconds_total{job="node_exporter",%s,mode="idle"}[%s])))`, selector, rateWindow),
				NameLabel: "instance",
			}}, InstanceGraph{Title: "CPU usage", Unit: graphUnitPercent}, nil
		}
		return []graphRangeQuery{{
			Query:     fmt.Sprintf(`100 * (1 - avg(rate(node_cpu_seconds_total{job="node_exporter",%s,mode="idle"}[%s])))`, selector, rateWindow),
			FixedName: "cpu used",
		}}, InstanceGraph{Title: "CPU usage", Unit: graphUnitPercent}, nil
	case "/mem":
		nameLabel := ""
		fixedName := "memory used"
		if target.Regex {
			nameLabel = "instance"
			fixedName = ""
		}
		return []graphRangeQuery{{
			Query:     fmt.Sprintf(`100 * (1 - (node_memory_MemAvailable_bytes{job="node_exporter",%s} / node_memory_MemTotal_bytes{job="node_exporter",%s}))`, selector, selector),
			NameLabel: nameLabel,
			FixedName: fixedName,
		}}, InstanceGraph{Title: "Memory usage", Unit: graphUnitPercent}, nil
	case "/la":
		if target.Regex {
			return []graphRangeQuery{{
				Query:     fmt.Sprintf(`node_load5{job="node_exporter",%s}`, selector),
				NameLabel: "instance",
			}}, InstanceGraph{Title: "Load average", Unit: graphUnitLoad}, nil
		}
		return []graphRangeQuery{{
			Query:     fmt.Sprintf(`{__name__=~"node_load1|node_load5|node_load15",job="node_exporter",%s}`, selector),
			NameLabel: "__name__",
		}}, InstanceGraph{Title: "Load average", Unit: graphUnitLoad}, nil
	case "/swap":
		nameLabel := ""
		fixedName := "swap used"
		if target.Regex {
			nameLabel = "instance"
			fixedName = ""
		}
		return []graphRangeQuery{{
			Query:     fmt.Sprintf(`100 * (1 - (node_memory_SwapFree_bytes{job="node_exporter",%s} / node_memory_SwapTotal_bytes{job="node_exporter",%s}))`, selector, selector),
			NameLabel: nameLabel,
			FixedName: fixedName,
		}}, InstanceGraph{Title: "Swap usage", Unit: graphUnitPercent, EmptyMessage: graphEmptyMessage("Swap usage", target, window)}, nil
	case "/space":
		limit := graphTopSeriesLimit(target)
		query := fmt.Sprintf(`topk(%d, 100 * (1 - (node_filesystem_avail_bytes{job="node_exporter",%s,fstype!~"^(tmpfs|devtmpfs|overlay|squashfs|iso9660|fuse.lxcfs|nsfs|proc|sysfs|cgroup2?)$",mountpoint!~"^/(run|var/lib/docker|snap)($|/)"} / node_filesystem_size_bytes{job="node_exporter",%s,fstype!~"^(tmpfs|devtmpfs|overlay|squashfs|iso9660|fuse.lxcfs|nsfs|proc|sysfs|cgroup2?)$",mountpoint!~"^/(run|var/lib/docker|snap)($|/)"})))`, limit, selector, selector)
		values, err := top(query, "mountpoint", "device", "instance")
		if err != nil {
			return nil, InstanceGraph{}, err
		}
		queries := make([]graphRangeQuery, 0, len(values))
		for _, value := range limitMetricValues(values, limit) {
			instance := graphMetricInstance(target, value)
			mountpointName := firstNonEmpty(value.Labels["mountpoint"], value.Name)
			mountpoint := strconv.Quote(mountpointName)
			queries = append(queries, graphRangeQuery{
				Query:     fmt.Sprintf(`100 * (1 - (node_filesystem_avail_bytes{job="node_exporter",instance=%s,mountpoint=%s} / node_filesystem_size_bytes{job="node_exporter",instance=%s,mountpoint=%s}))`, strconv.Quote(instance), mountpoint, strconv.Quote(instance), mountpoint),
				FixedName: graphMetricSeriesName(target, instance, mountpointName),
			})
		}
		return queries, InstanceGraph{Title: "Filesystem usage", Unit: graphUnitPercent, EmptyMessage: graphEmptyMessage("Filesystem usage", target, window)}, nil
	case "/io":
		limit := graphTopSeriesLimit(target)
		query := fmt.Sprintf(`topk(%d, rate(node_disk_io_time_seconds_total{job="node_exporter",%s,device!~"^(loop|ram|fd).*|^sr[0-9]+$"}[%s]) * 100)`, limit, selector, rateWindow)
		values, err := top(query, "device")
		if err != nil {
			return nil, InstanceGraph{}, err
		}
		queries := make([]graphRangeQuery, 0, len(values))
		for _, value := range limitMetricValues(values, limit) {
			instance := graphMetricInstance(target, value)
			deviceName := firstNonEmpty(value.Labels["device"], value.Name)
			device := strconv.Quote(deviceName)
			queries = append(queries, graphRangeQuery{
				Query:     fmt.Sprintf(`rate(node_disk_io_time_seconds_total{job="node_exporter",instance=%s,device=%s}[%s]) * 100`, strconv.Quote(instance), device, rateWindow),
				FixedName: graphMetricSeriesName(target, instance, deviceName),
			})
		}
		return queries, InstanceGraph{Title: "Disk IO busy", Unit: graphUnitPercent, EmptyMessage: graphEmptyMessage("Disk IO busy", target, window)}, nil
	case "/rx", "/tx":
		metric := "node_network_receive_bytes_total"
		title := "Network receive"
		if command == "/tx" {
			metric = "node_network_transmit_bytes_total"
			title = "Network transmit"
		}
		limit := graphTopSeriesLimit(target)
		query := fmt.Sprintf(`topk(%d, rate(%s{job="node_exporter",%s,device!~"^(lo|docker.*|veth.*|br-.*|virbr.*)$"}[%s]) * 8)`, limit, metric, selector, rateWindow)
		values, err := top(query, "device")
		if err != nil {
			return nil, InstanceGraph{}, err
		}
		queries := make([]graphRangeQuery, 0, len(values))
		for _, value := range limitMetricValues(values, limit) {
			instance := graphMetricInstance(target, value)
			deviceName := firstNonEmpty(value.Labels["device"], value.Name)
			device := strconv.Quote(deviceName)
			queries = append(queries, graphRangeQuery{
				Query:     fmt.Sprintf(`rate(%s{job="node_exporter",instance=%s,device=%s}[%s]) * 8`, metric, strconv.Quote(instance), device, rateWindow),
				FixedName: graphMetricSeriesName(target, instance, deviceName),
			})
		}
		return queries, InstanceGraph{Title: title, Unit: graphUnitBits, EmptyMessage: graphEmptyMessage(title, target, window)}, nil
	default:
		return nil, InstanceGraph{}, fmt.Errorf("unsupported graph command %q", command)
	}
}

func graphTopSeriesLimit(target GraphTarget) int {
	if target.Regex {
		return DefaultGraphRegexHostLimit
	}
	return 3
}

func graphMetricInstance(target GraphTarget, value MetricValue) string {
	if instance := strings.TrimSpace(value.Labels["instance"]); instance != "" {
		return instance
	}
	return target.Raw
}

func graphMetricSeriesName(target GraphTarget, instance, entity string) string {
	entity = strings.TrimSpace(entity)
	if entity == "" {
		entity = "value"
	}
	if target.Regex {
		instance = strings.TrimSpace(instance)
		if instance == "" {
			instance = target.Raw
		}
		return instance + " / " + entity
	}
	return entity
}

func graphEmptyMessage(title string, target GraphTarget, window GraphRange) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Graph"
	}
	if target.Regex {
		return fmt.Sprintf("No %s data for /%s/ over %s (%d hosts).", strings.ToLower(title), target.Raw, window.Raw, len(target.Hosts))
	}
	return fmt.Sprintf("No %s data for %s over %s.", strings.ToLower(title), target.Raw, window.Raw)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
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
