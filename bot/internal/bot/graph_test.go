package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAlertmanagerClientGraphInstanceQueryRange(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	var sawRange bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		query := r.URL.Query()
		if !strings.Contains(query.Get("query"), "node_memory_MemAvailable_bytes") {
			t.Fatalf("unexpected graph query: %s", query.Get("query"))
		}
		if !strings.Contains(query.Get("query"), `instance="vm<1>"`) || strings.Contains(query.Get("query"), `instance=~`) {
			t.Fatalf("exact graph query did not use exact instance matcher: %s", query.Get("query"))
		}
		if query.Get("start") == "" || query.Get("end") == "" || query.Get("step") != "15s" {
			t.Fatalf("range params missing or wrong: %s", r.URL.RawQuery)
		}
		sawRange = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"result": []any{map[string]any{
				"metric": map[string]string{},
				"values": [][]any{
					{float64(now.Add(-time.Hour).Unix()), "10"},
					{float64(now.Add(-30 * time.Minute).Unix()), "20"},
					{float64(now.Unix()), "NaN"},
				},
			}}},
		})
	}))
	defer server.Close()

	window := GraphRange{Raw: "1h", Duration: time.Hour, Step: 15 * time.Second, RateWindow: "1m", Start: now.Add(-time.Hour), End: now}
	client := &AlertmanagerClient{
		MetricsBaseURLs: map[string]string{TenantOne: server.URL},
		Client:          server.Client(),
	}
	graph, err := client.GraphInstance(context.Background(), TenantOne, "/mem", "vm<1>", window)
	if err != nil {
		t.Fatalf("GraphInstance returned error: %v", err)
	}
	if !sawRange {
		t.Fatal("query_range was not called")
	}
	if graph.Tenant != TenantOne || graph.Command != "/mem" || graph.Instance != "vm<1>" || len(graph.Series) != 1 {
		t.Fatalf("unexpected graph metadata: %#v", graph)
	}
	if len(graph.Series[0].Points) != 3 || graph.Series[0].Points[2].Valid {
		t.Fatalf("range points were not decoded with invalid NaN marker: %#v", graph.Series[0].Points)
	}
}

func TestParseGraphTargetExactRegexAndInvalid(t *testing.T) {
	t.Parallel()

	exact, err := parseGraphTarget("vmalert-01")
	if err != nil {
		t.Fatalf("parse exact target returned error: %v", err)
	}
	if exact.Regex || exact.Matcher != `instance="vmalert-01"` || exact.HostCap != DefaultGraphRegexHostLimit {
		t.Fatalf("unexpected exact target: %#v", exact)
	}

	regexTarget, err := parseGraphTarget("vlogs-edge.*")
	if err != nil {
		t.Fatalf("parse regex target returned error: %v", err)
	}
	if !regexTarget.Regex || regexTarget.Matcher != `instance=~"vlogs-edge.*"` {
		t.Fatalf("unexpected regex target: %#v", regexTarget)
	}

	if _, err := parseGraphTarget("vlogs-edge["); err == nil {
		t.Fatal("invalid regex target unexpectedly succeeded")
	} else if message, ok := operatorCommandErrorMessage(err); !ok || !strings.Contains(message, "Invalid instance regex") {
		t.Fatalf("invalid regex returned wrong error: %T %v", err, err)
	}
}

func TestAlertmanagerClientGraphInstanceRegexQueryRange(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	var sawHostQuery, sawRange bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		switch r.URL.Path {
		case "/api/v1/query":
			if !strings.Contains(query, `instance=~"vlogs-edge.*"`) {
				t.Fatalf("regex host query did not use regex matcher: %s", query)
			}
			sawHostQuery = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"result": []any{
					map[string]any{"metric": map[string]string{"instance": "vlogs-edge-02"}, "value": []any{float64(now.Unix()), "1"}},
					map[string]any{"metric": map[string]string{"instance": "vlogs-edge-01"}, "value": []any{float64(now.Unix()), "1"}},
				}},
			})
		case "/api/v1/query_range":
			if !strings.Contains(query, `instance=~"vlogs-edge.*"`) || !strings.Contains(query, "avg by (instance)") {
				t.Fatalf("regex range query did not keep per-instance regex matcher: %s", query)
			}
			sawRange = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"result": []any{
					map[string]any{
						"metric": map[string]string{"instance": "vlogs-edge-01"},
						"values": [][]any{{float64(now.Unix()), "42"}},
					},
					map[string]any{
						"metric": map[string]string{"instance": "vlogs-edge-02"},
						"values": [][]any{{float64(now.Unix()), "43"}},
					},
				}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	window := GraphRange{Raw: "1h", Duration: time.Hour, Step: 15 * time.Second, RateWindow: "1m", Start: now.Add(-time.Hour), End: now}
	client := &AlertmanagerClient{
		MetricsBaseURLs: map[string]string{TenantOne: server.URL},
		Client:          server.Client(),
	}
	graph, err := client.GraphInstance(context.Background(), TenantOne, "/cpu", "vlogs-edge.*", window)
	if err != nil {
		t.Fatalf("GraphInstance returned error: %v", err)
	}
	if !sawHostQuery || !sawRange {
		t.Fatalf("expected host and range queries, sawHost=%v sawRange=%v", sawHostQuery, sawRange)
	}
	if !graph.Target.Regex || strings.Join(graph.Target.Hosts, ",") != "vlogs-edge-01,vlogs-edge-02" {
		t.Fatalf("unexpected regex target metadata: %#v", graph.Target)
	}
	if len(graph.Series) != 2 || graph.Series[0].Name == "" || graph.Series[1].Name == "" {
		t.Fatalf("unexpected regex series: %#v", graph.Series)
	}
}

func TestAlertmanagerClientGraphInstanceRegexHostCap(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	var rangeCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/query":
			result := make([]any, 0, DefaultGraphRegexHostLimit+1)
			for i := 0; i < DefaultGraphRegexHostLimit+1; i++ {
				result = append(result, map[string]any{
					"metric": map[string]string{"instance": "node-0" + strconv.Itoa(i)},
					"value":  []any{float64(now.Unix()), "1"},
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data":   map[string]any{"result": result},
			})
		case "/api/v1/query_range":
			rangeCalls++
			t.Fatalf("host-cap rejection should not query range")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	window := GraphRange{Raw: "1h", Duration: time.Hour, Step: 15 * time.Second, RateWindow: "1m", Start: now.Add(-time.Hour), End: now}
	client := &AlertmanagerClient{
		MetricsBaseURLs: map[string]string{TenantOne: server.URL},
		Client:          server.Client(),
	}
	_, err := client.GraphInstance(context.Background(), TenantOne, "/cpu", "node-0.*", window)
	if err == nil {
		t.Fatal("GraphInstance unexpectedly accepted too many regex hosts")
	}
	message, ok := operatorCommandErrorMessage(err)
	if !ok || !strings.Contains(message, "matched 7 hosts") || !strings.Contains(message, "narrow the regex") {
		t.Fatalf("unexpected host-cap error: %T %v", err, err)
	}
	if rangeCalls != 0 {
		t.Fatalf("range calls=%d want 0", rangeCalls)
	}
}

func TestAlertmanagerClientGraphInstanceTopSpaceThenRange(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	var instantCalls, rangeCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/query":
			instantCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"result": []any{
					map[string]any{"metric": map[string]string{"mountpoint": "/"}, "value": []any{float64(now.Unix()), "80"}},
					map[string]any{"metric": map[string]string{"mountpoint": "/var"}, "value": []any{float64(now.Unix()), "70"}},
					map[string]any{"metric": map[string]string{"mountpoint": "/data"}, "value": []any{float64(now.Unix()), "60"}},
					map[string]any{"metric": map[string]string{"mountpoint": "/extra"}, "value": []any{float64(now.Unix()), "50"}},
				}},
			})
		case "/api/v1/query_range":
			rangeCalls++
			query := r.URL.Query().Get("query")
			if !strings.Contains(query, `mountpoint=`) {
				t.Fatalf("range query does not pin mountpoint: %s", query)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"result": []any{map[string]any{
					"metric": map[string]string{},
					"values": [][]any{{float64(now.Unix()), "42"}},
				}}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	window := GraphRange{Raw: "1h", Duration: time.Hour, Step: 15 * time.Second, RateWindow: "1m", Start: now.Add(-time.Hour), End: now}
	client := &AlertmanagerClient{
		MetricsBaseURLs: map[string]string{TenantOne: server.URL},
		Client:          server.Client(),
	}
	graph, err := client.GraphInstance(context.Background(), TenantOne, "/space", "node-01", window)
	if err != nil {
		t.Fatalf("GraphInstance returned error: %v", err)
	}
	if instantCalls != 1 || rangeCalls != 3 || len(graph.Series) != 3 {
		t.Fatalf("unexpected top/range calls: instant=%d range=%d series=%d", instantCalls, rangeCalls, len(graph.Series))
	}
}

func TestAlertmanagerClientGraphInstanceRegexTopSpaceCombinedSeries(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	var topQuery string
	var rangeQueries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		switch r.URL.Path {
		case "/api/v1/query":
			if strings.Contains(query, "group by (instance)") {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": "success",
					"data": map[string]any{"result": []any{
						map[string]any{"metric": map[string]string{"instance": "vlogs-edge-01"}, "value": []any{float64(now.Unix()), "1"}},
						map[string]any{"metric": map[string]string{"instance": "vlogs-edge-02"}, "value": []any{float64(now.Unix()), "1"}},
					}},
				})
				return
			}
			topQuery = query
			if !strings.Contains(query, `topk(6,`) || !strings.Contains(query, `instance=~"vlogs-edge.*"`) {
				t.Fatalf("regex top query did not use top 6 regex selector: %s", query)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"result": []any{
					map[string]any{"metric": map[string]string{"instance": "vlogs-edge-01", "mountpoint": "/"}, "value": []any{float64(now.Unix()), "80"}},
					map[string]any{"metric": map[string]string{"instance": "vlogs-edge-02", "mountpoint": "/data"}, "value": []any{float64(now.Unix()), "70"}},
				}},
			})
		case "/api/v1/query_range":
			rangeQueries = append(rangeQueries, query)
			if strings.Contains(query, `instance=~`) || !strings.Contains(query, `instance="vlogs-edge-`) {
				t.Fatalf("selected combined range query should pin exact instance: %s", query)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"result": []any{map[string]any{
					"metric": map[string]string{},
					"values": [][]any{{float64(now.Unix()), "42"}},
				}}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	window := GraphRange{Raw: "12h", Duration: 12 * time.Hour, Step: 5 * time.Minute, RateWindow: "20m", Start: now.Add(-12 * time.Hour), End: now}
	client := &AlertmanagerClient{
		MetricsBaseURLs: map[string]string{TenantOne: server.URL},
		Client:          server.Client(),
	}
	graph, err := client.GraphInstance(context.Background(), TenantOne, "/space", "vlogs-edge.*", window)
	if err != nil {
		t.Fatalf("GraphInstance returned error: %v", err)
	}
	if topQuery == "" || len(rangeQueries) != 2 {
		t.Fatalf("unexpected query counts: top=%q ranges=%d", topQuery, len(rangeQueries))
	}
	if len(graph.Series) != 2 || graph.Series[0].Name != "vlogs-edge-01 / /" || graph.Series[1].Name != "vlogs-edge-02 / /data" {
		t.Fatalf("unexpected combined series labels: %#v", graph.Series)
	}
}

func TestRenderGraphPNGValid(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	graph := InstanceGraph{
		Tenant:   TenantOne,
		Command:  "/rx",
		Title:    "Network receive",
		Unit:     graphUnitBits,
		Instance: "node-01",
		Range:    GraphRange{Raw: "1h", Duration: time.Hour, Step: 15 * time.Second, Start: now.Add(-time.Hour), End: now},
		Series: []GraphSeries{
			{Name: "eth0", Points: []GraphPoint{{Time: now.Add(-time.Hour), Value: 1_000_000, Valid: true}, {Time: now, Value: 2_000_000, Valid: true}}},
			{Name: "eth1", Points: []GraphPoint{{Time: now.Add(-time.Hour), Value: 500_000, Valid: true}, {Time: now, Value: 700_000, Valid: true}}},
		},
	}
	out, err := RenderGraphPNG(graph)
	if err != nil {
		t.Fatalf("RenderGraphPNG returned error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("RenderGraphPNG returned empty output")
	}
	decoded, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("PNG decode failed: %v", err)
	}
	if decoded.Bounds().Dx() != graphWidth || decoded.Bounds().Dy() != graphHeight {
		t.Fatalf("unexpected PNG size: %v", decoded.Bounds())
	}
}

func TestRenderGraphCaptionEscapesHTML(t *testing.T) {
	t.Parallel()

	caption := RenderGraphCaption(InstanceGraph{
		Tenant:   TenantOne,
		Title:    "CPU <usage>",
		Instance: "vm<1>",
		Range:    GraphRange{Raw: "1h"},
	})
	if strings.Contains(caption, "vm<1>") || strings.Contains(caption, "CPU <usage>") {
		t.Fatalf("caption is not escaped: %q", caption)
	}
	if !strings.Contains(caption, "vm&lt;1&gt;") || !strings.Contains(caption, "CPU &lt;usage&gt;") {
		t.Fatalf("caption missing escaped values: %q", caption)
	}
}

func TestRenderGraphCaptionRegexMode(t *testing.T) {
	t.Parallel()

	caption := RenderGraphCaption(InstanceGraph{
		Tenant:  TenantOne,
		Title:   "Filesystem usage",
		Range:   GraphRange{Raw: "12h"},
		Target:  GraphTarget{Raw: "vlogs-edge.*", Regex: true, Hosts: []string{"vlogs-edge-01", "vlogs-edge-02"}},
		Series:  []GraphSeries{{Name: "vlogs-edge-01 / /"}},
		Command: "/space",
	})
	if !strings.Contains(caption, "<code>/vlogs-edge.*/</code>") || !strings.Contains(caption, "2 hosts") {
		t.Fatalf("regex caption missing target or host count: %q", caption)
	}
	if !strings.Contains(caption, "<b>Filesystem usage</b> tenant <code>1</code>") {
		t.Fatalf("regex caption missing readable title/tenant: %q", caption)
	}
}
