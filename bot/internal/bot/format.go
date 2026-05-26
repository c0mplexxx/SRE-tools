package bot

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var renderNow = time.Now

type bucket int

const (
	bucketCritical bucket = iota
	bucketWarning
	bucketOther
)

const expandableQuoteLineThreshold = 4

type alertView struct {
	bucket    bucket
	alertname string
	instance  string
	entity    string
	body      string
}

type silenceView struct {
	bucket    bucket
	alertname string
	instance  string
	entity    string
	body      string
}

func RenderAlertMessages(alerts []Alert, limit int, expandableQuotes bool) ([]string, error) {
	return renderAlertMessages(alerts, limit, expandableQuotes, false)
}

func RenderAlertIDMessages(alerts []Alert, limit int, expandableQuotes bool) ([]string, error) {
	return renderAlertMessages(alerts, limit, expandableQuotes, true)
}

func RenderSilenceMessages(silences []AlertmanagerSilence, limit int) ([]string, error) {
	if limit <= 0 {
		limit = DefaultTelegramMessageLimit
	}
	if len(silences) == 0 {
		return []string{"Active silences tenant 1: 0"}, nil
	}

	sorted := append([]AlertmanagerSilence(nil), silences...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left, right := sorted[i], sorted[j]
		if !left.EndsAt.Equal(right.EndsAt) {
			return left.EndsAt.Before(right.EndsAt)
		}
		return left.ID < right.ID
	})

	views := normalizeSilences(sorted)
	sort.SliceStable(views, func(i, j int) bool {
		left, right := views[i], views[j]
		switch {
		case left.bucket != right.bucket:
			return left.bucket < right.bucket
		case left.alertname != right.alertname:
			return left.alertname < right.alertname
		case left.instance != right.instance:
			return left.instance < right.instance
		case left.entity != right.entity:
			return left.entity < right.entity
		default:
			return left.body < right.body
		}
	})

	blocks := []string{fmt.Sprintf("Active silences tenant 1: %d", len(sorted))}
	for start := 0; start < len(views); {
		currentBucket := views[start].bucket
		bucketEnd := start + 1
		for bucketEnd < len(views) && views[bucketEnd].bucket == currentBucket {
			bucketEnd++
		}
		blocks = append(blocks, "\n\n"+bucketTitle(currentBucket, bucketEnd-start))
		for start < bucketEnd {
			end := start + 1
			for end < bucketEnd && views[end].alertname == views[start].alertname {
				end++
			}
			groupBlocks, err := splitSilenceGroupBlocks(views[start:end], limit)
			if err != nil {
				return nil, err
			}
			for _, block := range groupBlocks {
				blocks = append(blocks, "\n"+block)
			}
			start = end
		}
	}
	return chunkBlocks(blocks, limit)
}

func RenderInstanceCheckMessage(check InstanceCheck) string {
	tenant := strings.TrimSpace(check.Tenant)
	if tenant == "" {
		tenant = TenantOne
	}
	header := "<b>check</b> tenant <code>" + html.EscapeString(tenant) + "</code> | <code>" + html.EscapeString(check.Instance) + "</code> | <code>" + html.EscapeString(check.Window) + "</code>"
	lines := []string{
		"up: " + formatUp(check.Up),
		"cpu: " + formatPercent(check.CPUUsagePercent) + " | cores: " + formatNumber(check.CPUCores, 0),
		"la: " + formatNumber(check.Load1, 2) + " / " + formatNumber(check.Load5, 2) + " / " + formatNumber(check.Load15, 2),
		"mem: " + formatPercent(check.MemoryPercent) + " | " + formatBytesPair(check.MemoryUsedBytes, check.MemoryTotalBytes),
		"disk: " + formatMetricList(check.DiskUsage, formatMetricPercent, 3),
		"io: " + formatMetricList(check.DiskIOBusy, formatMetricPercent, 3),
		"rx: " + formatMetricList(check.NetworkReceive, formatMetricBitsPerSecond, 2),
		"tx: " + formatMetricList(check.NetworkTransmit, formatMetricBitsPerSecond, 2),
	}
	return header + "\n" + quoteBlock(lines, false)
}

func normalizeSilences(silences []AlertmanagerSilence) []silenceView {
	views := make([]silenceView, 0, len(silences))
	for _, silence := range silences {
		alert := silenceAlert(silence)
		alertname := alert.label("alertname")
		if alertname == "" {
			alertname = "unknown_alert"
		}
		entity := entityLabel(alert)
		views = append(views, silenceView{
			bucket:    severityBucket(silenceSeverity(alert)),
			alertname: alertname,
			instance:  alert.label("instance"),
			entity:    entity,
			body:      renderSilenceLine(silence, alert, entity, alertname),
		})
	}
	return views
}

func renderAlertMessages(alerts []Alert, limit int, expandableQuotes, includeIDs bool) ([]string, error) {
	if limit <= 0 {
		limit = DefaultTelegramMessageLimit
	}
	if len(alerts) == 0 {
		return []string{"Active alerts tenant 1: 0"}, nil
	}

	views := normalize(alerts, includeIDs)
	sort.SliceStable(views, func(i, j int) bool {
		left, right := views[i], views[j]
		switch {
		case left.bucket != right.bucket:
			return left.bucket < right.bucket
		case left.alertname != right.alertname:
			return left.alertname < right.alertname
		case left.instance != right.instance:
			return left.instance < right.instance
		case left.entity != right.entity:
			return left.entity < right.entity
		default:
			return left.body < right.body
		}
	})

	blocks := []string{fmt.Sprintf("Active alerts tenant 1: %d", len(alerts))}
	for start := 0; start < len(views); {
		currentBucket := views[start].bucket
		bucketEnd := start + 1
		for bucketEnd < len(views) && views[bucketEnd].bucket == currentBucket {
			bucketEnd++
		}
		bucketCount := bucketEnd - start
		sectionStarted := false
		for start < bucketEnd {
			end := start + 1
			for end < len(views) && views[end].bucket == currentBucket && views[end].alertname == views[start].alertname {
				end++
			}

			prefix := ""
			if !sectionStarted {
				prefix = "\n\n" + bucketTitle(currentBucket, bucketCount) + "\n"
				sectionStarted = true
			}
			groupBlocks, err := splitGroupBlocks(prefix, views[start:end], limit, expandableQuotes)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, groupBlocks...)
			start = end
		}
	}
	return chunkBlocks(blocks, limit)
}

func renderSilenceLine(silence AlertmanagerSilence, alert Alert, entity, alertname string) string {
	endsAt := "unknown-end"
	if !silence.EndsAt.IsZero() {
		endsAt = silence.EndsAt.Format("2006-01-02T15:04:05Z07:00") + " (" + timeLeft(silence.EndsAt, renderNow()) + " left)"
	}

	lines := []string{
		renderBody(alert, entity, alertname),
		"id: <code>" + html.EscapeString(silenceID(silence)) + "</code>",
		"until: " + html.EscapeString(endsAt),
	}

	if createdBy := silenceCreatorDisplay(silence); createdBy != "" {
		lines = append(lines, "silenced by: "+html.EscapeString(createdBy))
	}
	return strings.Join(lines, "\n")
}

func silenceGroupBlock(views []silenceView) string {
	lines := make([]string, 0, len(views))
	for _, view := range views {
		lines = append(lines, view.body)
	}
	return "<b>" + html.EscapeString(views[0].alertname) + "</b>\n" + quoteBlock(lines, false)
}

func splitSilenceGroupBlocks(views []silenceView, limit int) ([]string, error) {
	var (
		blocks  []string
		current []silenceView
	)
	for _, view := range views {
		candidate := append(append([]silenceView(nil), current...), view)
		if utf8.RuneCountInString(silenceGroupBlock(candidate)) <= limit {
			current = candidate
			continue
		}
		if len(current) == 0 {
			return nil, fmt.Errorf("silence block for %q exceeds Telegram limit", view.alertname)
		}

		blocks = append(blocks, silenceGroupBlock(current))
		current = []silenceView{view}
		if utf8.RuneCountInString(silenceGroupBlock(current)) > limit {
			return nil, fmt.Errorf("silence block for %q exceeds Telegram limit", view.alertname)
		}
	}
	if len(current) > 0 {
		blocks = append(blocks, silenceGroupBlock(current))
	}
	return blocks, nil
}

func silenceID(silence AlertmanagerSilence) string {
	if strings.TrimSpace(silence.ID) == "" {
		return "missing-id"
	}
	return silence.ID
}

func silenceCreatorDisplay(silence AlertmanagerSilence) string {
	createdBy := strings.TrimSpace(silence.CreatedBy)
	if strings.HasPrefix(createdBy, "telegram ") {
		createdBy = strings.TrimSpace(strings.TrimPrefix(createdBy, "telegram "))
		if beforeID, _, ok := strings.Cut(createdBy, " (id "); ok {
			createdBy = strings.TrimSpace(beforeID)
		}
	}
	return createdBy
}

func timeLeft(until, now time.Time) string {
	duration := until.Sub(now)
	if duration <= 0 {
		return "expired"
	}
	totalMinutes := int(duration.Round(time.Minute) / time.Minute)
	days := totalMinutes / (24 * 60)
	totalMinutes %= 24 * 60
	hours := totalMinutes / 60
	minutes := totalMinutes % 60

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, " ")
}

func formatUp(value *float64) string {
	if value == nil {
		return "n/a"
	}
	if *value >= 1 {
		return "1"
	}
	return "0"
}

func formatPercent(value *float64) string {
	if value == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", *value)
}

func formatNumber(value *float64, precision int) string {
	if value == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.*f", precision, *value)
}

func formatBytesPair(used, total *float64) string {
	if used == nil || total == nil {
		return "n/a"
	}
	return formatBytes(*used) + "/" + formatBytes(*total)
}

func formatMetricList(values []MetricValue, formatter func(MetricValue) string, limit int) string {
	if len(values) == 0 {
		return "n/a"
	}
	if limit > 0 && len(values) > limit {
		values = values[:limit]
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, formatter(value))
	}
	return strings.Join(parts, ", ")
}

func formatMetricPercent(value MetricValue) string {
	return html.EscapeString(value.Name) + " " + fmt.Sprintf("%.1f%%", value.Value)
}

func formatMetricBitsPerSecond(value MetricValue) string {
	return html.EscapeString(value.Name) + " " + formatBitsPerSecond(value.Value)
}

func formatBytes(value float64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%.0fB", value)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	scaled := value
	for _, suffix := range units {
		scaled /= unit
		if scaled < unit {
			return fmt.Sprintf("%.1f%s", scaled, suffix)
		}
	}
	return fmt.Sprintf("%.1fPiB", scaled/unit)
}

func formatBitsPerSecond(value float64) string {
	const unit = 1000
	if value < unit {
		return fmt.Sprintf("%.0fb/s", value)
	}
	units := []string{"Kb/s", "Mb/s", "Gb/s", "Tb/s"}
	scaled := value
	for _, suffix := range units {
		scaled /= unit
		if scaled < unit {
			return fmt.Sprintf("%.1f%s", scaled, suffix)
		}
	}
	return fmt.Sprintf("%.1fPb/s", scaled/unit)
}

func silenceAlert(silence AlertmanagerSilence) Alert {
	labels := make(map[string]string)
	for _, matcher := range silence.Matchers {
		if !matcher.IsEqual || strings.TrimSpace(matcher.Name) == "" || !isKeySilenceLabel(matcher.Name) {
			continue
		}
		labels[matcher.Name] = humanMatcherValue(matcher)
	}
	return Alert{Labels: labels}
}

func humanMatcherValue(matcher SilenceMatcher) string {
	if !matcher.IsRegex {
		return matcher.Value
	}
	parts := strings.Split(matcher.Value, "|")
	if len(parts) < 2 {
		return matcher.Value
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" || strings.ContainsAny(part, `()[]{}+*?^$\\`) {
			return matcher.Value
		}
	}
	return strings.Join(parts, " and ")
}

func isKeySilenceLabel(name string) bool {
	switch name {
	case "alertgroup", "alertname", "device", "instance", "job", "mountpoint", "name", "service", "severity", "status", "threshold":
		return true
	default:
		return false
	}
}

func silenceSeverity(alert Alert) string {
	if severity := alert.label("severity"); severity != "" {
		return severity
	}
	switch alert.label("alertname") {
	case "scrape_down", "systemd_down":
		return "critical"
	default:
		return ""
	}
}

func normalize(alerts []Alert, includeIDs bool) []alertView {
	views := make([]alertView, 0, len(alerts))
	for _, alert := range alerts {
		alertname := alert.label("alertname")
		if alertname == "" {
			alertname = "unknown_alert"
		}
		entity := entityLabel(alert)
		body := renderBody(alert, entity, alertname)
		if includeIDs {
			body = renderIDBody(alert, body)
		}
		views = append(views, alertView{
			bucket:    severityBucket(alert.label("severity")),
			alertname: alertname,
			instance:  alert.label("instance"),
			entity:    entity,
			body:      body,
		})
	}
	return views
}

func renderIDBody(alert Alert, body string) string {
	id := strings.TrimSpace(alert.Fingerprint)
	if id == "" {
		id = "missing-id"
	}
	return "<code>" + html.EscapeString(id) + "</code> | " + body
}

func severityBucket(severity string) bucket {
	switch strings.ToLower(severity) {
	case "critical", "high":
		return bucketCritical
	case "warning":
		return bucketWarning
	default:
		return bucketOther
	}
}

func bucketTitle(value bucket, count int) string {
	switch value {
	case bucketCritical:
		return fmt.Sprintf("🟥 <b>CRITICAL</b> (%d)", count)
	case bucketWarning:
		return fmt.Sprintf("🟨 <b>WARNING</b> (%d)", count)
	default:
		return fmt.Sprintf("⬜ <b>OTHER</b> (%d)", count)
	}
}

func groupBlock(views []alertView, expandableQuotes bool) string {
	lines := make([]string, 0, len(views))
	for _, view := range views {
		lines = append(lines, view.body)
	}
	return "<b>" + html.EscapeString(views[0].alertname) + "</b>\n" + quoteBlock(lines, expandableQuotes && len(views) >= 3)
}

func quoteBlock(lines []string, forceExpandable bool) string {
	return blockquoteOpen(forceExpandable || lineCount(lines) > expandableQuoteLineThreshold) + strings.Join(lines, "\n") + "</blockquote>"
}

func blockquoteOpen(expandable bool) string {
	if expandable {
		return "<blockquote expandable>"
	}
	return "<blockquote>"
}

func lineCount(lines []string) int {
	count := 0
	for _, line := range lines {
		if line == "" {
			count++
			continue
		}
		count += strings.Count(line, "\n") + 1
	}
	return count
}

func splitGroupBlocks(prefix string, views []alertView, limit int, expandableQuotes bool) ([]string, error) {
	var (
		blocks  []string
		current []alertView
	)
	for _, view := range views {
		candidate := append(append([]alertView(nil), current...), view)
		if utf8.RuneCountInString(prefix+groupBlock(candidate, expandableQuotes)) <= limit {
			current = candidate
			continue
		}
		if len(current) == 0 {
			return nil, fmt.Errorf("alert block for %q exceeds Telegram limit", view.alertname)
		}

		blocks = append(blocks, prefix+groupBlock(current, expandableQuotes))
		prefix = ""
		current = []alertView{view}
		if utf8.RuneCountInString(groupBlock(current, expandableQuotes)) > limit {
			return nil, fmt.Errorf("alert block for %q exceeds Telegram limit", view.alertname)
		}
	}
	if len(current) > 0 {
		blocks = append(blocks, prefix+groupBlock(current, expandableQuotes))
	}
	return blocks, nil
}

func renderBody(alert Alert, entity, alertname string) string {
	if alert.label("alertgroup") == "dosgate-cpu-usage" {
		return renderDosGateCPU(alert)
	}
	if alert.label("service") == "systemd" && alert.label("name") != "" {
		return joinEscaped("DOWN", alert.label("instance"), alert.label("name"))
	}
	if line := alert.annotation("line"); line != "" {
		return html.EscapeString(line)
	}
	return joinEscaped("DOWN", alert.label("instance"), entity, alertname)
}

func renderDosGateCPU(alert Alert) string {
	level := "CPU"
	switch alert.label("threshold") {
	case "40":
		level = "WARN"
	case "70":
		level = "HIGH"
	case "90":
		level = "CRIT"
	}
	return joinEscaped(level, alert.label("instance"), cpuValue(alert))
}

func cpuValue(alert Alert) string {
	if line := alert.annotation("line"); line != "" {
		parts := strings.Split(line, "|")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	if value := alert.annotation("value"); value != "" {
		return value
	}
	if threshold := alert.label("threshold"); threshold != "" {
		return threshold + "% threshold"
	}
	return "threshold crossed"
}

func entityLabel(alert Alert) string {
	for _, key := range []string{"name", "device", "mountpoint", "service", "job"} {
		if value := alert.label(key); value != "" {
			return value
		}
	}
	return "unknown"
}

func joinEscaped(parts ...string) string {
	for i := range parts {
		if parts[i] == "" {
			parts[i] = "unknown"
		}
		parts[i] = html.EscapeString(parts[i])
	}
	return strings.Join(parts, " | ")
}

func chunkBlocks(blocks []string, limit int) ([]string, error) {
	var chunks []string
	var current string
	for _, block := range blocks {
		candidate := block
		if current != "" {
			candidate = current + block
		}
		if utf8.RuneCountInString(candidate) <= limit {
			current = candidate
			continue
		}
		if current == "" {
			return nil, fmt.Errorf("Telegram block exceeds message limit")
		}
		chunks = append(chunks, current)
		current = block
	}
	if current != "" {
		chunks = append(chunks, current)
	}
	return chunks, nil
}
