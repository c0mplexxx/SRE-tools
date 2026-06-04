package bot

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSeverityBucket(t *testing.T) {
	t.Parallel()

	tests := map[string]bucket{
		"critical": bucketCritical,
		"high":     bucketCritical,
		"warning":  bucketWarning,
		"info":     bucketOther,
	}
	for severity, want := range tests {
		if got := severityBucket(severity); got != want {
			t.Fatalf("severityBucket(%q)=%v want %v", severity, got, want)
		}
	}
}

func TestRenderAlertMessagesStandardAndCPUFormatting(t *testing.T) {
	t.Parallel()

	alerts := []Alert{
		{
			Labels: map[string]string{
				"tenant":    "1",
				"severity":  "warning",
				"alertname": "disk_space",
				"instance":  "node-01",
			},
			Annotations: map[string]string{"line": "disk <space> & pressure"},
		},
		{
			Labels: map[string]string{
				"tenant":     "1",
				"severity":   "critical",
				"alertname":  "systemd_down",
				"instance":   "node-01",
				"service":    "systemd",
				"name":       "example.service",
				"alertgroup": "systemd",
			},
		},
		{
			Labels: map[string]string{
				"tenant":     "1",
				"severity":   "high",
				"alertname":  "dosgate_cpu",
				"instance":   "node-02",
				"alertgroup": "dosgate-cpu-usage",
				"threshold":  "70",
			},
			Annotations: map[string]string{"line": "CPU 70% | node-02 | 70.2%"},
		},
		{
			Labels: map[string]string{
				"tenant":    "1",
				"severity":  "info",
				"alertname": "fallback_alert",
				"instance":  "blackbox",
				"job":       "blackbox_exporter",
			},
		},
	}

	messages, err := RenderAlertMessages(alerts, DefaultTelegramMessageLimit, true)
	if err != nil {
		t.Fatalf("RenderAlertMessages returned error: %v", err)
	}
	got := strings.Join(messages, "")

	for _, want := range []string{
		"Active alerts non-zero tenants: 4",
		"🟥 <b>CRITICAL</b> (2)",
		"HIGH | node-02 | 70.2%",
		"DOWN | node-01 | example.service",
		"🟨 <b>WARNING</b> (1)",
		"disk &lt;space&gt; &amp; pressure",
		"⬜ <b>OTHER</b> (1)",
		"DOWN | blackbox | blackbox_exporter | fallback_alert",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered output missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "dosgate_cpu") > strings.Index(got, "disk_space") {
		t.Fatalf("critical alert should precede warning alert:\n%s", got)
	}
}

func TestRenderAlertMessagesGroupsNonZeroTenants(t *testing.T) {
	t.Parallel()

	messages, err := RenderAlertMessages([]Alert{
		{
			Labels:      map[string]string{"tenant": "4", "severity": "warning", "alertname": "disk_space", "instance": "node-04"},
			Annotations: map[string]string{"line": "tenant 4 disk"},
		},
		{
			Labels:      map[string]string{"tenant": "1", "severity": "critical", "alertname": "systemd_down", "instance": "node-01", "name": "vmagent.service"},
			Annotations: map[string]string{"line": "tenant 1 systemd"},
		},
		{
			Labels:      map[string]string{"tenant": "10", "severity": "critical", "alertname": "scrape_down", "instance": "node-10", "job": "vmagent"},
			Annotations: map[string]string{"line": "tenant 10 scrape"},
		},
	}, DefaultTelegramMessageLimit, true)
	if err != nil {
		t.Fatalf("RenderAlertMessages returned error: %v", err)
	}
	got := strings.Join(messages, "")
	for _, want := range []string{
		"Active alerts non-zero tenants: 3",
		"tenant 1 systemd",
		"<b>tenant 4</b>",
		"tenant 4 disk",
		"<b>tenant 10</b>",
		"tenant 10 scrape",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("multi-tenant output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<b>tenant 1</b>") {
		t.Fatalf("tenant 1 should keep the legacy section style:\n%s", got)
	}
	if !(strings.Index(got, "tenant 1 systemd") < strings.Index(got, "<b>tenant 4</b>") &&
		strings.Index(got, "<b>tenant 4</b>") < strings.Index(got, "<b>tenant 10</b>")) {
		t.Fatalf("tenant sections are not sorted as expected:\n%s", got)
	}
}

func TestRenderAlertIDMessagesPrefixesFingerprint(t *testing.T) {
	t.Parallel()

	messages, err := RenderAlertIDMessages([]Alert{{
		Fingerprint: "e2b25051ad7705d5",
		Labels: map[string]string{
			"tenant":    "1",
			"severity":  "critical",
			"alertname": "scrape_down",
			"instance":  "node-01",
			"job":       "vmagent",
		},
		Annotations: map[string]string{"line": "DOWN | node-01 | vmagent | scrape failed"},
	}}, DefaultTelegramMessageLimit, true)
	if err != nil {
		t.Fatalf("RenderAlertIDMessages returned error: %v", err)
	}
	got := strings.Join(messages, "")
	for _, want := range []string{"🟥 <b>CRITICAL</b> (1)", "<code>e2b25051ad7705d5</code>", "DOWN | node-01 | vmagent | scrape failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("id output missing %q:\n%s", want, got)
		}
	}
}

func TestRenderSilenceMessagesUsesAlertLikeBlocks(t *testing.T) {
	setRenderNow(t, mustTime(t, "2026-05-20T05:30:00Z"))

	messages, err := RenderSilenceMessages([]AlertmanagerSilence{{
		ID:        "silence-id",
		EndsAt:    mustTime(t, "2026-05-22T18:00:00Z"),
		CreatedBy: "telegram @test_operator (id 100500)",
		Comment:   "Acked <from> Telegram",
		Matchers: []SilenceMatcher{
			{Name: "tenant", Value: "1", IsEqual: true},
			{Name: "severity", Value: "warning", IsEqual: true},
			{Name: "alertname", Value: "disk_space", IsEqual: true},
			{Name: "instance", Value: "node-01", IsEqual: true},
			{Name: "mountpoint", Value: "/var", IsEqual: true},
			{Name: "extra_noise", Value: "do-not-print", IsEqual: true},
		},
	}}, DefaultTelegramMessageLimit)
	if err != nil {
		t.Fatalf("RenderSilenceMessages returned error: %v", err)
	}
	got := strings.Join(messages, "")
	for _, want := range []string{
		"Active silences non-zero tenants: 1",
		"🟨 <b>WARNING</b> (1)",
		"<b>disk_space</b>",
		"<blockquote>DOWN | node-01 | /var | disk_space",
		"id: <code>silence-id</code>",
		"until: 2026-05-22T18:00:00Z (2d 12h left)",
		"silenced by: @test_operator",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("silence output missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"action:", "reason:", "Acked &lt;from&gt; Telegram", "alert-list-bot:", "extra_noise", "do-not-print", "tenant=1", "severity=warning"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("silence output leaked raw matcher %q:\n%s", unwanted, got)
		}
	}
	if strings.Count(got, "<blockquote") != strings.Count(got, "</blockquote>") {
		t.Fatalf("silence output has broken blockquote HTML:\n%s", got)
	}
}

func TestRenderSilenceMessagesShowsMatcherOnlySilences(t *testing.T) {
	setRenderNow(t, mustTime(t, "2026-06-04T21:29:59Z"))

	messages, err := RenderSilenceMessages([]AlertmanagerSilence{{
		ID:        "7d878d4f-f856-4f93-9c72-47da6642a5eb",
		EndsAt:    mustTime(t, "2026-06-04T21:30:59Z"),
		CreatedBy: "telegram @barinov_sp (id 42)",
		Matchers: []SilenceMatcher{
			{Name: "tenant", Value: "1", IsEqual: true},
			{Name: "instance", Value: "^dg-srv.*", IsRegex: true, IsEqual: true},
		},
	}}, DefaultTelegramMessageLimit)
	if err != nil {
		t.Fatalf("RenderSilenceMessages returned error: %v", err)
	}
	got := strings.Join(messages, "")
	for _, want := range []string{
		"<b>matcher_silence</b>",
		"MATCHERS | instance=~^dg-srv.*,tenant=1",
		"id: <code>7d878d4f-f856-4f93-9c72-47da6642a5eb</code>",
		"until: 2026-06-04T21:30:59Z (1m left)",
		"silenced by: @barinov_sp",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("matcher-only silence output missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"unknown_alert", "DOWN | ^dg-srv.* | unknown"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("matcher-only silence output leaked %q:\n%s", unwanted, got)
		}
	}
}

func TestRenderSilenceMessagesGroupsNonZeroTenants(t *testing.T) {
	setRenderNow(t, mustTime(t, "2026-05-20T05:30:00Z"))

	messages, err := RenderSilenceMessages([]AlertmanagerSilence{
		{
			ID:     "tenant-four",
			EndsAt: mustTime(t, "2026-05-22T18:00:00Z"),
			Matchers: []SilenceMatcher{
				{Name: "tenant", Value: "4", IsEqual: true},
				{Name: "severity", Value: "warning", IsEqual: true},
				{Name: "alertname", Value: "disk_space", IsEqual: true},
				{Name: "instance", Value: "node-04", IsEqual: true},
			},
		},
		{
			ID:     "tenant-one",
			EndsAt: mustTime(t, "2026-05-22T17:00:00Z"),
			Matchers: []SilenceMatcher{
				{Name: "tenant", Value: "1", IsEqual: true},
				{Name: "severity", Value: "critical", IsEqual: true},
				{Name: "alertname", Value: "systemd_down", IsEqual: true},
				{Name: "instance", Value: "node-01", IsEqual: true},
				{Name: "name", Value: "vmagent.service", IsEqual: true},
			},
		},
	}, DefaultTelegramMessageLimit)
	if err != nil {
		t.Fatalf("RenderSilenceMessages returned error: %v", err)
	}
	got := strings.Join(messages, "")
	for _, want := range []string{
		"Active silences non-zero tenants: 2",
		"id: <code>tenant-one</code>",
		"<b>tenant 4</b>",
		"id: <code>tenant-four</code>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("multi-tenant silence output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<b>tenant 1</b>") {
		t.Fatalf("tenant 1 silence should keep the legacy section style:\n%s", got)
	}
	if strings.Index(got, "id: <code>tenant-one</code>") > strings.Index(got, "<b>tenant 4</b>") {
		t.Fatalf("tenant 1 silence should render before tenant 4:\n%s", got)
	}
}

func TestRenderSilenceMessagesInfersCriticalScrapeDown(t *testing.T) {
	setRenderNow(t, mustTime(t, "2026-05-20T15:05:57Z"))

	messages, err := RenderSilenceMessages([]AlertmanagerSilence{
		{
			ID:     "scrape-silence-a",
			EndsAt: mustTime(t, "2026-06-20T15:05:57Z"),
			Matchers: []SilenceMatcher{
				{Name: "tenant", Value: "1", IsEqual: true},
				{Name: "alertname", Value: "scrape_down", IsEqual: true},
				{Name: "job", Value: "vmagent", IsEqual: true},
				{Name: "instance", Value: "node-01", IsEqual: true},
			},
		},
		{
			ID:     "scrape-silence-b",
			EndsAt: mustTime(t, "2026-06-20T15:10:57Z"),
			Matchers: []SilenceMatcher{
				{Name: "tenant", Value: "1", IsEqual: true},
				{Name: "alertname", Value: "scrape_down", IsEqual: true},
				{Name: "job", Value: "vmagent", IsEqual: true},
				{Name: "instance", Value: "node-02", IsEqual: true},
			},
		},
		{
			ID:     "systemd-silence",
			EndsAt: mustTime(t, "2026-06-20T15:05:57Z"),
			Matchers: []SilenceMatcher{
				{Name: "tenant", Value: "1", IsEqual: true},
				{Name: "alertname", Value: "systemd_down", IsEqual: true},
				{Name: "service", Value: "systemd", IsEqual: true},
				{Name: "name", Value: "example.service", IsEqual: true},
				{Name: "instance", Value: "node-01|node-02", IsRegex: true, IsEqual: true},
			},
		},
	}, DefaultTelegramMessageLimit)
	if err != nil {
		t.Fatalf("RenderSilenceMessages returned error: %v", err)
	}
	got := strings.Join(messages, "")
	for _, want := range []string{
		"🟥 <b>CRITICAL</b> (3)",
		"<b>scrape_down</b>",
		"DOWN | node-01 | vmagent | scrape_down",
		"id: <code>scrape-silence-a</code>",
		"DOWN | node-02 | vmagent | scrape_down",
		"id: <code>scrape-silence-b</code>",
		"<b>systemd_down</b>",
		"DOWN | node-01 and node-02 | example.service",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("scrape_down silence output missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "<b>scrape_down</b>") != 1 {
		t.Fatalf("scrape_down title should be grouped once:\n%s", got)
	}
	if strings.Count(got, "<blockquote") != 2 {
		t.Fatalf("expected one quote per alertname group:\n%s", got)
	}
}

func TestTimeLeft(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-20T10:00:00Z")
	tests := map[string]string{
		"2026-05-20T10:00:30Z": "1m",
		"2026-05-20T12:05:00Z": "2h 5m",
		"2026-05-22T22:00:00Z": "2d 12h",
		"2026-05-20T09:59:00Z": "expired",
	}
	for value, want := range tests {
		if got := timeLeft(mustTime(t, value), now); got != want {
			t.Fatalf("timeLeft(%s)=%q want %q", value, got, want)
		}
	}
}

func setRenderNow(t *testing.T, now time.Time) {
	t.Helper()
	previous := renderNow
	renderNow = func() time.Time { return now }
	t.Cleanup(func() { renderNow = previous })
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse test time: %v", err)
	}
	return parsed
}

func TestDosGateCPUThresholdLevels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		threshold string
		want      string
	}{
		{threshold: "40", want: "WARN | node-02 | 40.8%"},
		{threshold: "70", want: "HIGH | node-02 | 70.8%"},
		{threshold: "90", want: "CRIT | node-02 | 90.8%"},
	}
	for _, tt := range tests {
		alert := Alert{
			Labels: map[string]string{
				"alertgroup": "dosgate-cpu-usage",
				"instance":   "node-02",
				"threshold":  tt.threshold,
			},
			Annotations: map[string]string{"line": "CPU " + tt.threshold + "% | node-02 | " + tt.threshold + ".8%"},
		}
		if got := renderDosGateCPU(alert); got != tt.want {
			t.Fatalf("threshold %s rendered %q want %q", tt.threshold, got, tt.want)
		}
	}
}

func TestRenderAlertMessagesChunksCompleteHTMLBlocks(t *testing.T) {
	t.Parallel()

	var alerts []Alert
	for i := 0; i < 8; i++ {
		alerts = append(alerts, Alert{
			Labels: map[string]string{
				"tenant":    "1",
				"severity":  "warning",
				"alertname": "disk_space",
				"instance":  "host",
			},
			Annotations: map[string]string{"line": "low disk line " + strings.Repeat("a", 24)},
		})
	}

	messages, err := RenderAlertMessages(alerts, 220, true)
	if err != nil {
		t.Fatalf("RenderAlertMessages returned error: %v", err)
	}
	if len(messages) < 2 {
		t.Fatalf("expected split messages, got %d", len(messages))
	}
	for _, message := range messages {
		if utf8.RuneCountInString(message) > 220 {
			t.Fatalf("message exceeds limit: %d", utf8.RuneCountInString(message))
		}
		if strings.Count(message, "<blockquote") != strings.Count(message, "</blockquote>") {
			t.Fatalf("message split an HTML block:\n%s", message)
		}
	}
}

func TestAlertGroupUsesExpandableQuoteAfterThreeAlerts(t *testing.T) {
	t.Parallel()

	views := []alertView{
		{alertname: "systemd_down", body: "alert 1"},
		{alertname: "systemd_down", body: "alert 2"},
		{alertname: "systemd_down", body: "alert 3"},
		{alertname: "systemd_down", body: "alert 4"},
	}
	if got := groupBlock(views[:3], true); !strings.Contains(got, "<blockquote expandable>") {
		t.Fatalf("alert group did not use expandable quote:\n%s", got)
	}

	if got := groupBlock(views[:2], true); strings.Contains(got, "<blockquote expandable>") {
		t.Fatalf("two alerts should stay expanded:\n%s", got)
	}

	if got := groupBlock(views, false); strings.Contains(got, "<blockquote expandable>") {
		t.Fatalf("disabled expandable quotes should stay expanded:\n%s", got)
	}
}

func TestQuoteBlockExpandsAfterFourLines(t *testing.T) {
	t.Parallel()

	if got := quoteBlock([]string{"1", "2", "3", "4"}, false); strings.Contains(got, "<blockquote expandable>") {
		t.Fatalf("four-line quote should stay expanded:\n%s", got)
	}
	if got := quoteBlock([]string{"1", "2", "3", "4", "5"}, false); !strings.Contains(got, "<blockquote expandable>") {
		t.Fatalf("five-line quote should be expandable:\n%s", got)
	}
	if got := quoteBlock([]string{"1\n2\n3", "4\n5"}, false); !strings.Contains(got, "<blockquote expandable>") {
		t.Fatalf("multi-line quote should be expandable by total line count:\n%s", got)
	}
}

func TestRenderInstanceCheckUsesExpandableQuote(t *testing.T) {
	t.Parallel()

	value := func(v float64) *float64 { return &v }
	got := RenderInstanceCheckMessage(InstanceCheck{
		Tenant:           "1",
		Instance:         "vm<1>",
		Window:           "1h",
		Up:               value(1),
		CPUUsagePercent:  value(12.3),
		CPUCores:         value(8),
		Load1:            value(0.42),
		Load5:            value(0.38),
		Load15:           value(0.31),
		MemoryPercent:    value(71.2),
		MemoryUsedBytes:  value(24 * 1024 * 1024 * 1024),
		MemoryTotalBytes: value(32 * 1024 * 1024 * 1024),
	})
	if !strings.Contains(got, "<blockquote expandable>up: 1") {
		t.Fatalf("check output should use expandable quote:\n%s", got)
	}
}

func TestRenderInstanceCoverageUsesExpandableQuoteAndDedups(t *testing.T) {
	t.Parallel()

	got := RenderInstanceCoverageMessage(InstanceCoverage{
		Tenant:     "1",
		Instance:   "vm<1>",
		Alertnames: []string{"rule_zeta", "rule_alpha", "rule<memory>", "rule_disk", "rule_alpha", "rule_systemd"},
	})
	for _, want := range []string{
		"coverage tenant 1 | vm&lt;1&gt;",
		"<blockquote expandable>rule&lt;memory&gt;\nrule_alpha\nrule_disk\nrule_systemd\nrule_zeta</blockquote>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("coverage output missing %q:\n%s", want, got)
		}
	}
}
