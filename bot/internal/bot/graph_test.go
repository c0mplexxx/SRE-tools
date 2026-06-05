package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
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
