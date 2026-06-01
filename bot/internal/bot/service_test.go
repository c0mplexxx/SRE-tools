package bot

import (
	"bytes"
	"context"
	"errors"
	"log"
	"testing"
	"time"
)

func TestHandleUpdateAllowlistedCommandReplies(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{alerts: []Alert{{
		Labels:      map[string]string{"tenant": "1", "severity": "warning", "alertname": "disk_space"},
		Annotations: map[string]string{"line": "low disk"},
	}}}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/?")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.calls != 1 {
		t.Fatalf("Alertmanager calls=%d want 1", alerts.calls)
	}
	if len(telegram.sent) != 1 || telegram.sent[0].chatID != 42 || !bytes.Contains([]byte(telegram.sent[0].text), []byte("disk_space")) {
		t.Fatalf("unexpected Telegram messages: %#v", telegram.sent)
	}
}

func TestHandleUpdateIDCommandIncludesFingerprint(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{alerts: []Alert{{
		Fingerprint: "0df2e7e6da8f1fc5",
		Labels:      map[string]string{"tenant": "1", "severity": "critical", "alertname": "systemd_down"},
		Annotations: map[string]string{"line": "systemd down"},
	}}}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/id")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if len(telegram.sent) != 1 || !bytes.Contains([]byte(telegram.sent[0].text), []byte("<code>0df2e7e6da8f1fc5</code>")) {
		t.Fatalf("id reply did not include fingerprint: %#v", telegram.sent)
	}
}

func TestHandleUpdateHelpReplies(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/help")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if len(telegram.sent) != 1 || !bytes.Contains([]byte(telegram.sent[0].text), []byte("/silence alert-id duration")) {
		t.Fatalf("unexpected help reply: %#v", telegram.sent)
	}
	for _, unsafe := range []string{"<alert-id>", "<duration>", "<silence-id>"} {
		if bytes.Contains([]byte(telegram.sent[0].text), []byte(unsafe)) {
			t.Fatalf("help reply contains unsafe angle-bracket example %q: %q", unsafe, telegram.sent[0].text)
		}
	}
	for _, want := range []string{"/status", "/silences", "/check instance range", "node_exporter metrics", "/silence label=value,... duration", "instance=node-01,job=node_exporter", "/ack alert-id", "/unsilence silence-id"} {
		if !bytes.Contains([]byte(telegram.sent[0].text), []byte(want)) {
			t.Fatalf("help reply missing %q: %q", want, telegram.sent[0].text)
		}
	}
	if !bytes.Contains([]byte(telegram.sent[0].text), []byte("/coverage instance - alert rule coverage for one instance")) {
		t.Fatalf("help reply missing coverage command: %q", telegram.sent[0].text)
	}
	if alerts.calls != 0 {
		t.Fatalf("help fetched Alertmanager alerts: calls=%d", alerts.calls)
	}
}

func TestHandleUpdateStatusRepliesWithCounts(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{
		status:   AlertmanagerStatus{Ready: true, ActiveTenantAlerts: 7},
		silences: []AlertmanagerSilence{{ID: "s1"}, {ID: "s2"}},
	}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/status")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.statusCalls != 1 || alerts.silenceListCalls != 1 || alerts.silenceCalls != 0 || alerts.expireCalls != 0 {
		t.Fatalf("unexpected calls: status=%d list=%d silence=%d expire=%d", alerts.statusCalls, alerts.silenceListCalls, alerts.silenceCalls, alerts.expireCalls)
	}
	if len(telegram.sent) != 1 {
		t.Fatalf("unexpected Telegram messages: %#v", telegram.sent)
	}
	for _, want := range []string{"Bot: ok", "Alertmanager: ready", "Active tenant-1 alerts: 7", "Active tenant-1 silences: 2"} {
		if !bytes.Contains([]byte(telegram.sent[0].text), []byte(want)) {
			t.Fatalf("status reply missing %q: %q", want, telegram.sent[0].text)
		}
	}
}

func TestHandleUpdateCheckRepliesWithInstanceMetrics(t *testing.T) {
	t.Parallel()

	value := func(v float64) *float64 { return &v }
	alerts := &fakeAlerts{check: InstanceCheck{
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
		DiskUsage:        []MetricValue{{Name: "/", Value: 68.1}},
		DiskIOBusy:       []MetricValue{{Name: "sda", Value: 8.2}},
		NetworkReceive:   []MetricValue{{Name: "eth0", Value: 12_300_000}},
		NetworkTransmit:  []MetricValue{{Name: "eth0", Value: 8_100_000}},
	}}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/check vm<1> 1h")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.checkCalls != 1 || alerts.checkTenant != "1" || alerts.checkInstance != "vm<1>" || alerts.checkWindow != "1h" {
		t.Fatalf("unexpected check call: calls=%d tenant=%q instance=%q window=%q", alerts.checkCalls, alerts.checkTenant, alerts.checkInstance, alerts.checkWindow)
	}
	if len(telegram.sent) != 1 {
		t.Fatalf("unexpected Telegram messages: %#v", telegram.sent)
	}
	for _, want := range []string{
		"<b>check</b> tenant <code>1</code> | <code>vm&lt;1&gt;</code> | <code>1h</code>",
		"<blockquote expandable>up: 1",
		"cpu: 12.3% | cores: 8",
		"mem: 71.2% | 24.0GiB/32.0GiB",
		"rx: eth0 12.3Mb/s",
	} {
		if !bytes.Contains([]byte(telegram.sent[0].text), []byte(want)) {
			t.Fatalf("check reply missing %q: %q", want, telegram.sent[0].text)
		}
	}
}

func TestHandleUpdateCheckRejectsInvalidWindow(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/check vm1 7d")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.checkCalls != 0 {
		t.Fatalf("invalid check window queried metrics: calls=%d", alerts.checkCalls)
	}
	if len(telegram.sent) != 1 || !bytes.Contains([]byte(telegram.sent[0].text), []byte("Invalid range")) {
		t.Fatalf("unexpected invalid-window reply: %#v", telegram.sent)
	}
}

func TestHandleUpdateCoverageRequiresOneInstanceArg(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	for _, command := range []string{"/coverage", "/coverage node-01 extra"} {
		if err := service.HandleUpdate(context.Background(), commandUpdate(42, command)); err != nil {
			t.Fatalf("HandleUpdate(%q) returned error: %v", command, err)
		}
	}
	if alerts.coverageCalls != 0 || alerts.calls != 0 {
		t.Fatalf("invalid coverage usage queried APIs: coverage=%d activeAlerts=%d", alerts.coverageCalls, alerts.calls)
	}
	if len(telegram.sent) != 2 {
		t.Fatalf("unexpected validation replies: %#v", telegram.sent)
	}
	for _, sent := range telegram.sent {
		if !bytes.Contains([]byte(sent.text), []byte("Usage: <code>/coverage instance</code>")) {
			t.Fatalf("unexpected coverage usage reply: %#v", telegram.sent)
		}
	}
}

func TestHandleUpdateCoverageRepliesWithEscapedCoveredAlertnames(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{coverage: InstanceCoverage{
		Tenant:     "1",
		Instance:   "vm<1>",
		Alertnames: []string{"rule_systemd_probe", "rule<disk>", "rule_systemd_probe"},
	}}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/coverage vm<1>")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.coverageCalls != 1 || alerts.coverageTenant != "1" || alerts.coverageInstance != "vm<1>" || alerts.calls != 0 {
		t.Fatalf("unexpected coverage calls: coverage=%d tenant=%q instance=%q activeAlerts=%d", alerts.coverageCalls, alerts.coverageTenant, alerts.coverageInstance, alerts.calls)
	}
	if len(telegram.sent) != 1 {
		t.Fatalf("unexpected Telegram messages: %#v", telegram.sent)
	}
	got := telegram.sent[0].text
	for _, want := range []string{
		"coverage tenant 1 | vm&lt;1&gt;",
		"<blockquote>rule&lt;disk&gt;\nrule_systemd_probe</blockquote>",
	} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Fatalf("coverage reply missing %q: %q", want, got)
		}
	}
	if bytes.Contains([]byte(got), []byte("vm<1>")) || bytes.Contains([]byte(got), []byte("rule<disk>")) {
		t.Fatalf("coverage reply was not HTML escaped: %q", got)
	}
}

func TestHandleUpdateCoverageRepliesWhenEmpty(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/coverage node-01")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.coverageCalls != 1 {
		t.Fatalf("CoverageInstance calls=%d want 1", alerts.coverageCalls)
	}
	if len(telegram.sent) != 1 || !bytes.Contains([]byte(telegram.sent[0].text), []byte("covered alertnames: 0")) {
		t.Fatalf("unexpected empty coverage reply: %#v", telegram.sent)
	}
}

func TestHandleUpdateSilencesRendersEscapedHTML(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{silences: []AlertmanagerSilence{{
		ID:        "sil<one>",
		EndsAt:    time.Date(2026, 5, 22, 18, 0, 0, 0, time.UTC),
		CreatedBy: "me & you",
		Comment:   "host <down>",
		Matchers: []SilenceMatcher{
			{Name: "tenant", Value: "1", IsEqual: true},
			{Name: "severity", Value: "critical", IsEqual: true},
			{Name: "alertname", Value: "systemd_down", IsEqual: true},
			{Name: "instance", Value: "vm<1>", IsEqual: true},
			{Name: "service", Value: "systemd", IsEqual: true},
			{Name: "name", Value: "vmagent<noc>.service", IsEqual: true},
			{Name: "noisy_label", Value: "should-not-render", IsEqual: true},
		},
	}}}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/silences")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.silenceListCalls != 1 {
		t.Fatalf("ActiveSilences calls=%d want 1", alerts.silenceListCalls)
	}
	if len(telegram.sent) != 1 {
		t.Fatalf("unexpected Telegram messages: %#v", telegram.sent)
	}
	got := telegram.sent[0].text
	for _, want := range []string{
		"Active silences tenant 1: 1",
		"🟥 <b>CRITICAL</b> (1)",
		"<b>systemd_down</b>",
		"<blockquote>DOWN | vm&lt;1&gt; | vmagent&lt;noc&gt;.service",
		"id: <code>sil&lt;one&gt;</code>",
		"silenced by: me &amp; you",
	} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Fatalf("silences reply missing %q: %q", want, got)
		}
	}
	if bytes.Contains([]byte(got), []byte("reason:")) || bytes.Contains([]byte(got), []byte("host &lt;down&gt;")) || bytes.Contains([]byte(got), []byte("action:")) || bytes.Contains([]byte(got), []byte("should-not-render")) || bytes.Contains([]byte(got), []byte("noisy_label")) {
		t.Fatalf("silences reply leaked non-key matcher labels: %q", got)
	}
}

func TestHandleUpdateSilenceByID(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{alerts: []Alert{{
		Fingerprint: "e2b25051ad7705d5",
		Labels: map[string]string{
			"tenant":    "1",
			"alertname": "scrape_down",
			"instance":  "node-01",
		},
	}}}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/silence e2b25051ad7705d5 10d")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.silenceCalls != 1 || alerts.silenced.Fingerprint != "e2b25051ad7705d5" || alerts.duration != 10*24*time.Hour {
		t.Fatalf("unexpected silence call: calls=%d alert=%#v duration=%s", alerts.silenceCalls, alerts.silenced, alerts.duration)
	}
	if alerts.comment != "" {
		t.Fatalf("regular silence should use default client comment, got %q", alerts.comment)
	}
	if alerts.createdBy != "telegram @test_operator (id 100500)" {
		t.Fatalf("unexpected silence creator: %q", alerts.createdBy)
	}
	if len(telegram.sent) != 1 || !bytes.Contains([]byte(telegram.sent[0].text), []byte("silence-test-id")) {
		t.Fatalf("unexpected silence reply: %#v", telegram.sent)
	}
}

func TestHandleUpdateSilenceByLabels(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/silence instance=node-01,job=node_exporter 2h")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.silenceMatcherCalls != 1 || alerts.duration != 2*time.Hour {
		t.Fatalf("unexpected matcher silence call: calls=%d duration=%s", alerts.silenceMatcherCalls, alerts.duration)
	}
	if alerts.createdBy != "telegram @test_operator (id 100500)" {
		t.Fatalf("unexpected silence creator: %q", alerts.createdBy)
	}
	if alerts.comment != "Silenced from Telegram by labels: instance=node-01,job=node_exporter,tenant=1" {
		t.Fatalf("unexpected silence comment: %q", alerts.comment)
	}
	if got := formatMatcherExpression(alerts.matchers); got != "instance=node-01,job=node_exporter,tenant=1" {
		t.Fatalf("unexpected matchers: %s", got)
	}
	if len(telegram.sent) != 1 || !bytes.Contains([]byte(telegram.sent[0].text), []byte("Silenced labels <code>instance=node-01,job=node_exporter,tenant=1</code>")) {
		t.Fatalf("unexpected silence reply: %#v", telegram.sent)
	}
}

func TestHandleUpdateSilenceByLabelsAllowsWhitespace(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/silence instance=node-01, job=node_exporter 30m")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.silenceMatcherCalls != 1 {
		t.Fatalf("expected matcher silence call, got %d", alerts.silenceMatcherCalls)
	}
	if got := formatMatcherExpression(alerts.matchers); got != "instance=node-01,job=node_exporter,tenant=1" {
		t.Fatalf("unexpected matchers: %s", got)
	}
}

func TestHandleUpdateAckByID(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{alerts: []Alert{{
		Fingerprint: "e2b25051ad7705d5",
		Labels:      map[string]string{"tenant": "1", "alertname": "scrape_down"},
	}}}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/ack e2b25051ad7705d5")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.silenceCalls != 1 || alerts.silenced.Fingerprint != "e2b25051ad7705d5" || alerts.duration != 30*time.Minute {
		t.Fatalf("unexpected ack silence call: calls=%d alert=%#v duration=%s", alerts.silenceCalls, alerts.silenced, alerts.duration)
	}
	if alerts.comment != "Acked from Telegram for active alert e2b25051ad7705d5" {
		t.Fatalf("unexpected ack comment: %q", alerts.comment)
	}
	if alerts.createdBy != "telegram @test_operator (id 100500)" {
		t.Fatalf("unexpected ack creator: %q", alerts.createdBy)
	}
	if len(telegram.sent) != 1 || !bytes.Contains([]byte(telegram.sent[0].text), []byte("Acked <code>e2b25051ad7705d5</code> for 30m")) {
		t.Fatalf("unexpected ack reply: %#v", telegram.sent)
	}
}

func TestHandleUpdateAckRejectsUnknownID(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{alerts: []Alert{{Fingerprint: "known", Labels: map[string]string{"tenant": "1"}}}}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/ack missing")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.silenceCalls != 0 {
		t.Fatalf("unknown ack id created a silence: calls=%d", alerts.silenceCalls)
	}
	if len(telegram.sent) != 1 || !bytes.Contains([]byte(telegram.sent[0].text), []byte("not found")) {
		t.Fatalf("unexpected ack validation reply: %#v", telegram.sent)
	}
}

func TestHandleUpdateUnsilenceExpiresID(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/unsilence silence-123")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.expireCalls != 1 || alerts.expiredID != "silence-123" {
		t.Fatalf("unexpected expire call: calls=%d id=%q", alerts.expireCalls, alerts.expiredID)
	}
	if len(telegram.sent) != 1 || telegram.sent[0].text != "Expired silence <code>silence-123</code>." {
		t.Fatalf("unexpected unsilence reply: %#v", telegram.sent)
	}
}

func TestHandleUpdateUnsilenceFailure(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{expireErr: errors.New("delete failed")}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/unsilence silence-123")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if alerts.expireCalls != 1 {
		t.Fatalf("ExpireSilence calls=%d want 1", alerts.expireCalls)
	}
	if len(telegram.sent) != 1 || !bytes.Contains([]byte(telegram.sent[0].text), []byte("Could not expire")) {
		t.Fatalf("unexpected unsilence failure reply: %#v", telegram.sent)
	}
}

func TestHandleUpdateSilenceRejectsUnknownIDAndDuration(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{alerts: []Alert{{Fingerprint: "known", Labels: map[string]string{"tenant": "1"}}}}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/silence known 0d")); err != nil {
		t.Fatalf("invalid-duration HandleUpdate returned error: %v", err)
	}
	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/silence missing 10m")); err != nil {
		t.Fatalf("missing-id HandleUpdate returned error: %v", err)
	}
	if alerts.silenceCalls != 0 {
		t.Fatalf("invalid silence inputs created a silence: calls=%d", alerts.silenceCalls)
	}
	if len(telegram.sent) != 2 ||
		!bytes.Contains([]byte(telegram.sent[0].text), []byte("Invalid duration")) ||
		!bytes.Contains([]byte(telegram.sent[1].text), []byte("not found")) {
		t.Fatalf("unexpected validation replies: %#v", telegram.sent)
	}
}

func TestHandleUpdateSilenceRejectsUnsafeMatchers(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	for _, command := range []string{
		"/silence severity=warning 10m",
		"/silence instance=node-01,tenant=2 10m",
		"/silence unknown=label 10m",
	} {
		if err := service.HandleUpdate(context.Background(), commandUpdate(42, command)); err != nil {
			t.Fatalf("HandleUpdate(%q) returned error: %v", command, err)
		}
	}
	if alerts.silenceMatcherCalls != 0 || alerts.silenceCalls != 0 {
		t.Fatalf("unsafe matcher inputs created a silence: labelCalls=%d idCalls=%d", alerts.silenceMatcherCalls, alerts.silenceCalls)
	}
	if len(telegram.sent) != 3 {
		t.Fatalf("unexpected validation replies: %#v", telegram.sent)
	}
	for _, sent := range telegram.sent {
		if !bytes.Contains([]byte(sent.text), []byte("Invalid matchers")) {
			t.Fatalf("unexpected matcher validation reply: %#v", telegram.sent)
		}
	}
}

func TestParseSilenceDuration(t *testing.T) {
	t.Parallel()

	tests := map[string]time.Duration{
		"10s":    10 * time.Second,
		"10m":    10 * time.Minute,
		"10h":    10 * time.Hour,
		"10d":    10 * 24 * time.Hour,
		"1month": 30 * 24 * time.Hour,
	}
	for input, want := range tests {
		got, err := parseSilenceDuration(input)
		if err != nil {
			t.Fatalf("parseSilenceDuration(%q) returned error: %v", input, err)
		}
		if got != want {
			t.Fatalf("parseSilenceDuration(%q)=%s want %s", input, got, want)
		}
	}
	for _, input := range []string{"0s", "10w", "month", "1mo", "-1h"} {
		if _, err := parseSilenceDuration(input); err == nil {
			t.Fatalf("parseSilenceDuration(%q) unexpectedly succeeded", input)
		}
	}
}

func TestParseSilenceMatchers(t *testing.T) {
	t.Parallel()

	matchers, err := parseSilenceMatchers(`instance="node-01",job=node_exporter`)
	if err != nil {
		t.Fatalf("parseSilenceMatchers returned error: %v", err)
	}
	if got := formatMatcherExpression(matchers); got != "instance=node-01,job=node_exporter,tenant=1" {
		t.Fatalf("unexpected matchers: %s", got)
	}

	for _, input := range []string{
		"",
		"severity=warning",
		"instance=node-01,tenant=2",
		"instance=node-01,unknown=value",
		"instance",
		"instance=",
	} {
		if _, err := parseSilenceMatchers(input); err == nil {
			t.Fatalf("parseSilenceMatchers(%q) unexpectedly succeeded", input)
		}
	}
}

func TestHandleUpdateIgnoresOtherChatsAndText(t *testing.T) {
	t.Parallel()

	alerts := &fakeAlerts{}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)

	if err := service.HandleUpdate(context.Background(), commandUpdate(7, "/?")); err != nil {
		t.Fatalf("non-allowlisted HandleUpdate returned error: %v", err)
	}
	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/start")); err != nil {
		t.Fatalf("other-text HandleUpdate returned error: %v", err)
	}
	if alerts.calls != 0 || len(telegram.sent) != 0 {
		t.Fatalf("ignored updates caused work: calls=%d sent=%#v", alerts.calls, telegram.sent)
	}
}

func TestHandleUpdateAlertmanagerFailureRepliesAndLogs(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	alerts := &fakeAlerts{err: errors.New("dial refused")}
	telegram := &fakeTelegram{}
	service := testService(alerts, telegram)
	service.Logger = log.New(&logs, "", 0)

	if err := service.HandleUpdate(context.Background(), commandUpdate(42, "/?")); err != nil {
		t.Fatalf("HandleUpdate returned error: %v", err)
	}
	if len(telegram.sent) != 1 || telegram.sent[0].text != "Could not fetch active Alertmanager alerts right now." {
		t.Fatalf("unexpected error reply: %#v", telegram.sent)
	}
	if !bytes.Contains(logs.Bytes(), []byte("dial refused")) {
		t.Fatalf("log did not include detailed failure: %s", logs.String())
	}
}

type fakeAlerts struct {
	alerts              []Alert
	err                 error
	calls               int
	status              AlertmanagerStatus
	statusErr           error
	statusCalls         int
	silences            []AlertmanagerSilence
	silenceListErr      error
	silenceListCalls    int
	check               InstanceCheck
	checkErr            error
	checkTenant         string
	checkInstance       string
	checkWindow         string
	checkCalls          int
	coverage            InstanceCoverage
	coverageErr         error
	coverageTenant      string
	coverageInstance    string
	coverageCalls       int
	silence             Silence
	silenceErr          error
	silenced            Alert
	matchers            []SilenceMatcher
	duration            time.Duration
	createdBy           string
	comment             string
	silenceCalls        int
	silenceMatcherCalls int
	expiredID           string
	expireErr           error
	expireCalls         int
}

func (f *fakeAlerts) ActiveTenantAlerts(context.Context, string) ([]Alert, error) {
	f.calls++
	return f.alerts, f.err
}

func (f *fakeAlerts) Status(context.Context) (AlertmanagerStatus, error) {
	f.statusCalls++
	return f.status, f.statusErr
}

func (f *fakeAlerts) ActiveSilences(context.Context) ([]AlertmanagerSilence, error) {
	f.silenceListCalls++
	return f.silences, f.silenceListErr
}

func (f *fakeAlerts) CheckInstance(_ context.Context, tenant, instance, window string) (InstanceCheck, error) {
	f.checkCalls++
	f.checkTenant = tenant
	f.checkInstance = instance
	f.checkWindow = window
	if f.check.Tenant == "" {
		f.check.Tenant = tenant
	}
	if f.check.Instance == "" {
		f.check.Instance = instance
	}
	if f.check.Window == "" {
		f.check.Window = window
	}
	return f.check, f.checkErr
}

func (f *fakeAlerts) CoverageInstance(_ context.Context, tenant, instance string) (InstanceCoverage, error) {
	f.coverageCalls++
	f.coverageTenant = tenant
	f.coverageInstance = instance
	if f.coverage.Tenant == "" {
		f.coverage.Tenant = tenant
	}
	if f.coverage.Instance == "" {
		f.coverage.Instance = instance
	}
	return f.coverage, f.coverageErr
}

func (f *fakeAlerts) SilenceAlert(_ context.Context, alert Alert, duration time.Duration, createdBy, comment string) (Silence, error) {
	f.silenceCalls++
	f.silenced = alert
	f.duration = duration
	f.createdBy = createdBy
	f.comment = comment
	if f.silence.ID == "" {
		f.silence = Silence{ID: "silence-test-id", EndsAt: time.Date(2026, 5, 22, 18, 0, 0, 0, time.FixedZone("MSK", 3*60*60))}
	}
	return f.silence, f.silenceErr
}

func (f *fakeAlerts) SilenceMatchers(_ context.Context, matchers []SilenceMatcher, duration time.Duration, createdBy, comment string) (Silence, error) {
	f.silenceMatcherCalls++
	f.matchers = matchers
	f.duration = duration
	f.createdBy = createdBy
	f.comment = comment
	if f.silence.ID == "" {
		f.silence = Silence{ID: "silence-test-id", EndsAt: time.Date(2026, 5, 22, 18, 0, 0, 0, time.FixedZone("MSK", 3*60*60))}
	}
	return f.silence, f.silenceErr
}

func (f *fakeAlerts) ExpireSilence(_ context.Context, id string) error {
	f.expireCalls++
	f.expiredID = id
	return f.expireErr
}

type sentMessage struct {
	chatID int64
	text   string
}

type fakeTelegram struct {
	sent []sentMessage
}

func (f *fakeTelegram) GetUpdates(context.Context, int, time.Duration) ([]Update, error) {
	return nil, nil
}

func (f *fakeTelegram) SendMessage(_ context.Context, chatID int64, text string) error {
	f.sent = append(f.sent, sentMessage{chatID: chatID, text: text})
	return nil
}

func commandUpdate(chatID int64, text string) Update {
	return Update{UpdateID: 1, Message: &Message{
		Chat: Chat{ID: chatID},
		From: &User{ID: 100500, FirstName: "Test", LastName: "Operator", Username: "test_operator"},
		Text: text,
	}}
}

func testService(alerts AlertSource, telegram Messenger) *Service {
	return &Service{
		Alerts:           alerts,
		Telegram:         telegram,
		AllowedChatIDs:   map[int64]struct{}{42: {}},
		MessageLimit:     DefaultTelegramMessageLimit,
		ExpandableQuotes: true,
	}
}
