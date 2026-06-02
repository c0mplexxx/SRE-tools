package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAlertmanagerClientStatus(t *testing.T) {
	t.Parallel()

	var readyCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/-/ready":
			readyCalled = true
			_, _ = w.Write([]byte("OK"))
		case "/api/v2/alerts":
			_, _ = w.Write([]byte(`[
				{"labels": {"tenant": "1", "alertname": "systemd_down"}},
				{"labels": {"tenant": "4", "alertname": "scrape_down"}},
				{"labels": {"tenant": "1", "kind": "notify", "alertname": "note"}},
				{"labels": {"tenant": "0", "alertname": "other"}},
				{"labels": {"alertname": "missing_tenant"}}
			]`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &AlertmanagerClient{BaseURL: server.URL, Client: server.Client()}
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if !readyCalled || !status.Ready || status.ActiveTenantAlerts != 2 {
		t.Fatalf("unexpected status: readyCalled=%v status=%#v", readyCalled, status)
	}
}

func TestAlertmanagerClientActiveTenantAlerts(t *testing.T) {
	t.Parallel()

	var sawQuery bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/alerts" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		query := r.URL.Query()
		sawQuery = query.Get("active") == "true" &&
			query.Get("silenced") == "false" &&
			query.Get("inhibited") == "false"
		_, _ = w.Write([]byte(`[
			{
				"labels": {"tenant": "1", "alertname": "systemd_down"},
				"annotations": {"line": "example.service is DOWN on instance node-01"},
				"status": {"state": "active", "silencedBy": [], "inhibitedBy": [], "mutedBy": []}
			},
			{
				"fingerprint": "tenant-four",
				"labels": {"tenant": "4", "alertname": "disk_space"},
				"status": {"state": "active"}
			},
			{
				"labels": {"tenant": "1", "alertname": "scrape_target_added", "kind": "notify"},
				"status": {"state": "active"}
			},
			{
				"labels": {"tenant": "0", "alertname": "report"},
				"status": {"state": "active"}
			},
			{
				"labels": {"alertname": "missing_tenant"},
				"status": {"state": "active"}
			}
		]`))
	}))
	defer server.Close()

	client := &AlertmanagerClient{BaseURL: server.URL, Client: server.Client()}
	alerts, err := client.ActiveTenantAlerts(context.Background(), TenantNonZero)
	if err != nil {
		t.Fatalf("ActiveTenantAlerts returned error: %v", err)
	}
	if !sawQuery {
		t.Fatal("Alertmanager filters were not present in query")
	}
	if len(alerts) != 2 || alerts[0].label("alertname") != "systemd_down" || alerts[1].label("tenant") != "4" {
		t.Fatalf("unexpected tenant alerts: %#v", alerts)
	}
}

func TestAlertmanagerClientSilenceAlert(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 21, 18, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	var payload struct {
		Matchers  []SilenceMatcher `json:"matchers"`
		StartsAt  time.Time        `json:"startsAt"`
		EndsAt    time.Time        `json:"endsAt"`
		CreatedBy string           `json:"createdBy"`
		Comment   string           `json:"comment"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/silences" {
			t.Fatalf("unexpected silence request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type=%q want application/json", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode silence payload: %v", err)
		}
		_, _ = w.Write([]byte(`{"silenceID":"am-silence-id"}`))
	}))
	defer server.Close()

	client := &AlertmanagerClient{
		BaseURL: server.URL,
		Client:  server.Client(),
		Now:     func() time.Time { return now },
	}
	silence, err := client.SilenceAlert(context.Background(), Alert{
		Fingerprint: "fingerprint-id",
		Labels: map[string]string{
			"tenant":    "1",
			"instance":  "node-01",
			"alertname": "systemd_down",
		},
	}, 10*time.Minute, "", "")
	if err != nil {
		t.Fatalf("SilenceAlert returned error: %v", err)
	}
	if silence.ID != "am-silence-id" || !silence.EndsAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("unexpected silence result: %#v", silence)
	}
	if payload.CreatedBy != "alert-list-bot" || payload.Comment == "" || !payload.StartsAt.Equal(now) || !payload.EndsAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("unexpected silence payload metadata: %#v", payload)
	}
	if len(payload.Matchers) != 3 ||
		payload.Matchers[0].Name != "alertname" ||
		payload.Matchers[1].Name != "instance" ||
		payload.Matchers[2].Name != "tenant" {
		t.Fatalf("matchers were not exact and sorted: %#v", payload.Matchers)
	}
	for _, matcher := range payload.Matchers {
		if matcher.IsRegex || !matcher.IsEqual {
			t.Fatalf("matcher should be exact: %#v", matcher)
		}
	}
}

func TestAlertmanagerClientSilenceAlertAckComment(t *testing.T) {
	t.Parallel()

	var payload struct {
		CreatedBy string `json:"createdBy"`
		Comment   string `json:"comment"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode silence payload: %v", err)
		}
		_, _ = w.Write([]byte(`{"silenceID":"am-silence-id"}`))
	}))
	defer server.Close()

	client := &AlertmanagerClient{BaseURL: server.URL, Client: server.Client()}
	_, err := client.SilenceAlert(context.Background(), Alert{
		Fingerprint: "fingerprint-id",
		Labels:      map[string]string{"tenant": "1", "alertname": "systemd_down"},
	}, 30*time.Minute, "telegram @operator (id 42)", "Acked from Telegram for active alert fingerprint-id")
	if err != nil {
		t.Fatalf("SilenceAlert returned error: %v", err)
	}
	if payload.Comment != "Acked from Telegram for active alert fingerprint-id" {
		t.Fatalf("unexpected comment %q", payload.Comment)
	}
	if payload.CreatedBy != "telegram @operator (id 42)" {
		t.Fatalf("unexpected createdBy %q", payload.CreatedBy)
	}
}

func TestAlertmanagerClientSilenceMatchers(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 21, 18, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	var payload struct {
		Matchers  []SilenceMatcher `json:"matchers"`
		StartsAt  time.Time        `json:"startsAt"`
		EndsAt    time.Time        `json:"endsAt"`
		CreatedBy string           `json:"createdBy"`
		Comment   string           `json:"comment"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/silences" {
			t.Fatalf("unexpected silence request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode silence payload: %v", err)
		}
		_, _ = w.Write([]byte(`{"silenceID":"label-silence-id"}`))
	}))
	defer server.Close()

	client := &AlertmanagerClient{
		BaseURL: server.URL,
		Client:  server.Client(),
		Now:     func() time.Time { return now },
	}
	silence, err := client.SilenceMatchers(context.Background(), []SilenceMatcher{
		{Name: "job", Value: "node_exporter", IsEqual: true},
		{Name: "instance", Value: "node-01", IsEqual: true},
		{Name: "tenant", Value: "1", IsEqual: true},
	}, 2*time.Hour, "telegram @operator (id 42)", "Silenced from Telegram by labels: instance=node-01,job=node_exporter,tenant=1")
	if err != nil {
		t.Fatalf("SilenceMatchers returned error: %v", err)
	}
	if silence.ID != "label-silence-id" || !silence.EndsAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("unexpected silence result: %#v", silence)
	}
	if payload.CreatedBy != "telegram @operator (id 42)" || payload.Comment == "" || !payload.StartsAt.Equal(now) || !payload.EndsAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("unexpected silence payload metadata: %#v", payload)
	}
	if len(payload.Matchers) != 3 ||
		payload.Matchers[0].Name != "instance" ||
		payload.Matchers[1].Name != "job" ||
		payload.Matchers[2].Name != "tenant" {
		t.Fatalf("matchers were not sorted: %#v", payload.Matchers)
	}
	for _, matcher := range payload.Matchers {
		if matcher.IsRegex || !matcher.IsEqual {
			t.Fatalf("matcher should be exact: %#v", matcher)
		}
	}
}

func TestAlertmanagerClientActiveSilences(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/silences" {
			t.Fatalf("unexpected silences request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
			{
				"id": "later",
				"status": {"state": "active"},
				"endsAt": "2026-05-22T12:00:00Z",
				"createdBy": "operator",
				"comment": "tenant one",
				"matchers": [{"name":"tenant","value":"1","isEqual":true}]
			},
			{
				"id": "tenant-four",
				"status": {"state": "active"},
				"endsAt": "2026-05-21T12:30:00Z",
				"matchers": [{"name":"tenant","value":"4","isEqual":true}]
			},
			{
				"id": "inactive",
				"status": {"state": "expired"},
				"endsAt": "2026-05-20T12:00:00Z",
				"matchers": [{"name":"tenant","value":"1","isEqual":true}]
			},
			{
				"id": "tenant-zero",
				"status": {"state": "active"},
				"endsAt": "2026-05-21T12:00:00Z",
				"matchers": [{"name":"tenant","value":"0","isEqual":true}]
			},
			{
				"id": "global",
				"status": {"state": "active"},
				"endsAt": "2026-05-21T13:00:00Z",
				"matchers": [{"name":"alertname","value":"systemd_down","isEqual":true}]
			},
			{
				"id": "first",
				"status": {"state": "active"},
				"endsAt": "2026-05-21T13:00:00Z",
				"matchers": [{"name":"tenant","value":"1","isEqual":true}]
			}
		]`))
	}))
	defer server.Close()

	client := &AlertmanagerClient{BaseURL: server.URL, Client: server.Client()}
	silences, err := client.ActiveSilences(context.Background())
	if err != nil {
		t.Fatalf("ActiveSilences returned error: %v", err)
	}
	if len(silences) != 3 {
		t.Fatalf("unexpected silences: %#v", silences)
	}
	if got := []string{silences[0].ID, silences[1].ID, silences[2].ID}; strings.Join(got, ",") != "tenant-four,first,later" {
		t.Fatalf("silences not filtered/sorted: %#v", got)
	}
}

func TestAlertmanagerClientExpireSilence(t *testing.T) {
	t.Parallel()

	var sawDelete bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v2/silence/silence-id" {
			t.Fatalf("unexpected expire request %s %s", r.Method, r.URL.Path)
		}
		sawDelete = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &AlertmanagerClient{BaseURL: server.URL, Client: server.Client()}
	if err := client.ExpireSilence(context.Background(), "silence-id"); err != nil {
		t.Fatalf("ExpireSilence returned error: %v", err)
	}
	if !sawDelete {
		t.Fatal("DELETE was not called")
	}
}

func TestAlertmanagerClientExpireSilenceNon2xx(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &AlertmanagerClient{BaseURL: server.URL, Client: server.Client()}
	if err := client.ExpireSilence(context.Background(), "silence-id"); err == nil {
		t.Fatal("ExpireSilence unexpectedly succeeded")
	}
}

func TestAlertmanagerClientCheckInstance(t *testing.T) {
	t.Parallel()

	queries := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/query" {
			t.Fatalf("unexpected metrics request %s %s", r.Method, r.URL.Path)
		}
		query := r.URL.Query().Get("query")
		queries = append(queries, query)
		writeQueryResult(t, w, query)
	}))
	defer server.Close()

	client := &AlertmanagerClient{MetricsBaseURLs: map[string]string{"1": server.URL}, Client: server.Client()}
	check, err := client.CheckInstance(context.Background(), "1", `vm"one`, "1h")
	if err != nil {
		t.Fatalf("CheckInstance returned error: %v", err)
	}
	if check.Tenant != "1" || check.Instance != `vm"one` || check.Window != "1h" {
		t.Fatalf("unexpected check identity: %#v", check)
	}
	if check.Up == nil || *check.Up != 1 || check.CPUUsagePercent == nil || *check.CPUUsagePercent != 12.5 {
		t.Fatalf("unexpected core check values: %#v", check)
	}
	if len(check.DiskUsage) != 1 || check.DiskUsage[0].Name != "/" || check.DiskUsage[0].Value != 68.1 {
		t.Fatalf("unexpected disk usage: %#v", check.DiskUsage)
	}
	joined := strings.Join(queries, "\n")
	if !strings.Contains(joined, `instance="vm\"one"`) || !strings.Contains(joined, "[1h]") {
		t.Fatalf("queries did not escape instance or include window:\n%s", joined)
	}
}

func TestAlertmanagerClientCoverageInstance(t *testing.T) {
	t.Parallel()

	var vmalertCalled bool
	var metricQueries []string
	vmalert := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/rules" {
			t.Fatalf("unexpected vmalert request %s %s", r.Method, r.URL.Path)
		}
		vmalertCalled = true
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {
				"groups": [{
					"rules": [
						{"name": "rule_static_direct", "type": "alerting", "query": "vector(1)", "labels": {"tenant": "1", "instance": "node-01"}},
						{"name": "rule_static_other", "type": "alerting", "query": "vector(1)", "labels": {"tenant": "1", "instance": "node-02"}},
						{"name": "rule_notify_static", "type": "alerting", "query": "vector(1)", "labels": {"tenant": "1", "kind": "notify", "instance": "node-01"}},
						{"name": "rule_recording", "type": "recording", "query": "vector(1)", "labels": {"tenant": "1", "instance": "node-01"}},
						{"name": "rule_up_probe", "type": "alerting", "query": "up{job=~\".+\"} == 0", "labels": {"tenant": "1"}},
						{"name": "rule_systemd_probe", "type": "alerting", "query": "node_systemd_unit_state{state=\"active\"} == 0", "labels": {"tenant": "1"}},
						{"name": "rule_load_probe", "type": "alerting", "query": "node_load5 / count(node_cpu_seconds_total) > 2", "labels": {"tenant": "1"}},
						{"name": "rule_disk_probe", "type": "alerting", "query": "node_filesystem_avail_bytes / node_filesystem_size_bytes < 0.1", "labels": {"tenant": "1", "severity": "warning"}},
						{"name": "rule_disk_probe", "type": "alerting", "query": "node_filesystem_avail_bytes / node_filesystem_size_bytes < 0.05", "labels": {"tenant": "1", "severity": "critical"}},
						{"name": "rule_memory_probe", "type": "alerting", "query": "node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes < 0.1", "labels": {"tenant": "1"}},
						{"name": "rule_swap_probe", "type": "alerting", "query": "node_memory_SwapTotal_bytes > 0", "labels": {"tenant": "1"}},
						{"name": "rule_proxy_probe", "type": "alerting", "query": "haproxy_backend_status == 0", "labels": {"tenant": "1"}},
						{"name": "rule_network_probe", "type": "alerting", "query": "node_network_up == 0", "labels": {"tenant": "1"}},
						{"name": "rule_scoped_network_probe", "type": "alerting", "query": "rate(node_ethtool_received_errors{job=\"node_exporter\",instance=~\"edge-.*\"}[1m]) > 0", "labels": {"tenant": "1"}},
						{"name": "rule_placeholder", "type": "alerting", "query": "vector(1)", "labels": {"tenant": "1"}}
					]
				}]
			}
		}`))
	}))
	defer vmalert.Close()

	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/query" {
			t.Fatalf("unexpected metrics request %s %s", r.Method, r.URL.Path)
		}
		query := r.URL.Query().Get("query")
		metricQueries = append(metricQueries, query)
		writeCoverageQueryResult(t, w, query)
	}))
	defer metrics.Close()

	client := &AlertmanagerClient{
		MetricsBaseURLs: map[string]string{"1": metrics.URL},
		VmalertBaseURLs: map[string]string{"1": vmalert.URL},
		Client:          metrics.Client(),
	}
	coverage, err := client.CoverageInstance(context.Background(), "1", "node-01")
	if err != nil {
		t.Fatalf("CoverageInstance returned error: %v", err)
	}
	if !vmalertCalled {
		t.Fatal("vmalert rules endpoint was not called")
	}
	want := "rule_disk_probe,rule_memory_probe,rule_network_probe,rule_proxy_probe,rule_static_direct,rule_systemd_probe,rule_up_probe"
	if got := strings.Join(coverage.Alertnames, ","); got != want {
		t.Fatalf("unexpected coverage alertnames:\ngot:  %s\nwant: %s", got, want)
	}
	joined := strings.Join(metricQueries, "\n")
	for _, wantQueryPart := range []string{
		`up{instance="node-01"}`,
		`node_systemd_unit_state{job="node_exporter",instance="node-01",state="active"}`,
		`node_filesystem_`,
		`node_memory_`,
		`haproxy_`,
	} {
		if !strings.Contains(joined, wantQueryPart) {
			t.Fatalf("coverage query missing %q in:\n%s", wantQueryPart, joined)
		}
	}
	for _, leaked := range []string{"rule_static_other", "rule_notify_static", "rule_recording", "rule_load_probe", "rule_swap_probe", "rule_scoped_network_probe", "rule_placeholder"} {
		if strings.Contains(strings.Join(coverage.Alertnames, ","), leaked) {
			t.Fatalf("coverage leaked %s: %#v", leaked, coverage.Alertnames)
		}
	}
}

func TestAlertmanagerClientCoverageNetworkUsesSourceMetricProbe(t *testing.T) {
	t.Parallel()

	vmalert := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {"groups": [{"rules": [
				{"name": "rule_network_probe", "type": "alerting", "query": "node_ethtool_link_detected == 0", "labels": {"tenant": "1"}}
			]}]}
		}`))
	}))
	defer vmalert.Close()

	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeCoverageQueryResult(t, w, r.URL.Query().Get("query"))
	}))
	defer metrics.Close()

	client := &AlertmanagerClient{
		MetricsBaseURLs: map[string]string{"1": metrics.URL},
		VmalertBaseURLs: map[string]string{"1": vmalert.URL},
		Client:          metrics.Client(),
	}
	coverage, err := client.CoverageInstance(context.Background(), "1", "node-02")
	if err != nil {
		t.Fatalf("CoverageInstance returned error: %v", err)
	}
	if got := strings.Join(coverage.Alertnames, ","); got != "rule_network_probe" {
		t.Fatalf("unexpected network coverage: %q", got)
	}
}

func TestRuleQueryAllowsInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		query    string
		instance string
		want     bool
	}{
		{
			name:     "no instance matcher",
			query:    `up{job="node_exporter"} == 0`,
			instance: "node-01",
			want:     true,
		},
		{
			name:     "exact match",
			query:    `up{instance="node-01"} == 0`,
			instance: "node-01",
			want:     true,
		},
		{
			name:     "exact mismatch",
			query:    `up{instance="node-01"} == 0`,
			instance: "node-02",
			want:     false,
		},
		{
			name:     "regex match",
			query:    `rate(node_ethtool_received_errors{instance=~"edge-.*"}[1m]) > 0`,
			instance: "edge-01",
			want:     true,
		},
		{
			name:     "regex mismatch",
			query:    `rate(node_ethtool_received_errors{instance=~"edge-.*"}[1m]) > 0`,
			instance: "vminsert-02",
			want:     false,
		},
		{
			name:     "negative exact rejects",
			query:    `up{instance!="node-01"} == 0`,
			instance: "node-01",
			want:     false,
		},
		{
			name:     "negative regex rejects",
			query:    `up{instance!~"node-.*"} == 0`,
			instance: "node-01",
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ruleQueryAllowsInstance(tt.query, tt.instance); got != tt.want {
				t.Fatalf("ruleQueryAllowsInstance(%q, %q)=%v want %v", tt.query, tt.instance, got, tt.want)
			}
		})
	}
}

func writeQueryResult(t *testing.T, w http.ResponseWriter, query string) {
	t.Helper()
	type sample struct {
		Metric map[string]string `json:"metric"`
		Value  []any             `json:"value"`
	}
	value := func(metric map[string]string, v float64) sample {
		return sample{Metric: metric, Value: []any{float64(1760000000), strconv.FormatFloat(v, 'f', -1, 64)}}
	}

	var result []sample
	switch {
	case strings.HasPrefix(query, "up{"):
		result = []sample{value(nil, 1)}
	case strings.Contains(query, "node_cpu_seconds_total") && strings.Contains(query, "rate("):
		result = []sample{value(nil, 12.5)}
	case strings.HasPrefix(query, "count("):
		result = []sample{value(nil, 8)}
	case strings.Contains(query, "node_load1"):
		result = []sample{value(nil, 0.42)}
	case strings.Contains(query, "node_load5"):
		result = []sample{value(nil, 0.38)}
	case strings.Contains(query, "node_load15"):
		result = []sample{value(nil, 0.31)}
	case strings.Contains(query, "node_memory_MemAvailable_bytes") && strings.Contains(query, "100 *"):
		result = []sample{value(nil, 71.2)}
	case strings.Contains(query, "node_memory_MemTotal_bytes") && strings.Contains(query, "-"):
		result = []sample{value(nil, 24*1024*1024*1024)}
	case strings.Contains(query, "node_memory_MemTotal_bytes"):
		result = []sample{value(nil, 32*1024*1024*1024)}
	case strings.Contains(query, "node_filesystem_avail_bytes"):
		result = []sample{value(map[string]string{"mountpoint": "/"}, 68.1)}
	case strings.Contains(query, "node_disk_io_time_seconds_total"):
		result = []sample{value(map[string]string{"device": "sda"}, 8.2)}
	case strings.Contains(query, "node_network_receive_bytes_total"):
		result = []sample{value(map[string]string{"device": "eth0"}, 12_300_000)}
	case strings.Contains(query, "node_network_transmit_bytes_total"):
		result = []sample{value(map[string]string{"device": "eth0"}, 8_100_000)}
	default:
		t.Fatalf("unexpected query: %s", query)
	}

	if err := json.NewEncoder(w).Encode(struct {
		Status string `json:"status"`
		Data   struct {
			Result []sample `json:"result"`
		} `json:"data"`
	}{Status: "success", Data: struct {
		Result []sample `json:"result"`
	}{Result: result}}); err != nil {
		t.Fatalf("encode query result: %v", err)
	}
}

func writeCoverageQueryResult(t *testing.T, w http.ResponseWriter, query string) {
	t.Helper()
	type sample struct {
		Metric map[string]string `json:"metric"`
		Value  []any             `json:"value"`
	}
	value := func(v float64) sample {
		return sample{Metric: map[string]string{"instance": "node-01"}, Value: []any{float64(1760000000), strconv.FormatFloat(v, 'f', -1, 64)}}
	}

	var result []sample
	switch {
	case strings.Contains(query, `up{instance="node-01"`):
		result = []sample{value(1)}
	case strings.Contains(query, `node_systemd_unit_state`) && strings.Contains(query, `state="active"`):
		result = []sample{value(1)}
	case strings.Contains(query, `node_load5`):
		result = []sample{value(0.2)}
	case strings.Contains(query, `node_cpu_seconds_total`) && strings.Contains(query, `instance="node-01"`):
		result = nil
	case strings.Contains(query, `node_filesystem_`):
		result = []sample{value(1)}
	case strings.Contains(query, `node_disk_`):
		result = nil
	case strings.Contains(query, `node_memory_SwapTotal_bytes`):
		result = []sample{value(0)}
	case strings.Contains(query, `node_memory_`):
		result = []sample{value(1)}
	case strings.Contains(query, `node_vmstat_`):
		result = nil
	case strings.Contains(query, `haproxy_`):
		result = []sample{value(1)}
	case strings.Contains(query, `node_ethtool_`) && strings.Contains(query, `instance="node-02"`):
		result = []sample{value(1)}
	case strings.Contains(query, `node_network_`) && strings.Contains(query, `instance="node-02"`):
		result = []sample{value(1)}
	case strings.Contains(query, `node_network_`) && strings.Contains(query, `instance="node-01"`):
		result = []sample{value(1)}
	default:
		result = nil
	}

	if err := json.NewEncoder(w).Encode(struct {
		Status string `json:"status"`
		Data   struct {
			Result []sample `json:"result"`
		} `json:"data"`
	}{Status: "success", Data: struct {
		Result []sample `json:"result"`
	}{Result: result}}); err != nil {
		t.Fatalf("encode coverage query result: %v", err)
	}
}
