package bot

import (
	"context"
	"crypto/rand"
	"fmt"
	"html"
	"log"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Service struct {
	Alerts           AlertSource
	Telegram         Messenger
	AllowedChatIDs   map[int64]struct{}
	Logger           *log.Logger
	DeployJokeChance func() bool
	PollTimeout      time.Duration
	RetryDelay       time.Duration
	MessageLimit     int
	ExpandableQuotes bool
}

func (s *Service) Run(ctx context.Context) error {
	if s.Alerts == nil || s.Telegram == nil {
		return fmt.Errorf("alert source and Telegram messenger are required")
	}

	pollTimeout := s.PollTimeout
	if pollTimeout <= 0 {
		pollTimeout = 30 * time.Second
	}
	retryDelay := s.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 2 * time.Second
	}

	var offset int
	for {
		updates, err := s.Telegram.GetUpdates(ctx, offset, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.logger().Printf("Telegram getUpdates failed: %v", err)
			if !sleep(ctx, retryDelay) {
				return ctx.Err()
			}
			continue
		}

		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if err := s.HandleUpdate(ctx, update); err != nil {
				s.logger().Printf("update %d failed: %v", update.UpdateID, err)
			}
		}
	}
}

func (s *Service) HandleUpdate(ctx context.Context, update Update) error {
	if update.Message == nil {
		return nil
	}
	chatID := update.Message.Chat.ID
	if _, ok := s.AllowedChatIDs[chatID]; !ok {
		return nil
	}

	text := strings.TrimSpace(update.Message.Text)
	if isDeployJokeIntent(text) {
		if s.shouldReplyDeployJoke() {
			return s.Telegram.SendMessage(ctx, chatID, randomDeployJoke())
		}
		return nil
	}

	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	switch fields[0] {
	case "/?":
		if len(fields) != 1 {
			return nil
		}
		return s.replyAlerts(ctx, chatID, false)
	case "/id":
		if len(fields) != 1 {
			return s.Telegram.SendMessage(ctx, chatID, "Usage: /id")
		}
		return s.replyAlerts(ctx, chatID, true)
	case "/status":
		if len(fields) != 1 {
			return s.Telegram.SendMessage(ctx, chatID, "Usage: /status")
		}
		return s.replyStatus(ctx, chatID)
	case "/silences":
		if len(fields) != 1 {
			return s.Telegram.SendMessage(ctx, chatID, "Usage: /silences")
		}
		return s.replySilences(ctx, chatID)
	case "/check":
		return s.replyCheck(ctx, chatID, fields)
	case "/coverage":
		return s.replyCoverage(ctx, chatID, fields)
	case "/help":
		if len(fields) != 1 {
			return nil
		}
		return s.Telegram.SendMessage(ctx, chatID, helpMessage)
	case "/silence":
		return s.silenceAlert(ctx, chatID, update.Message, fields)
	case "/unsilence":
		return s.unsilence(ctx, chatID, fields)
	case "/ack":
		return s.ackAlert(ctx, chatID, update.Message, fields)
	default:
		return nil
	}
}

func (s *Service) replyStatus(ctx context.Context, chatID int64) error {
	status, err := s.Alerts.Status(ctx)
	if err != nil {
		s.logger().Printf("Alertmanager status failed for chat %d: %v", chatID, err)
		return s.Telegram.SendMessage(ctx, chatID, "Bot: ok\nAlertmanager: unavailable\nActive non-zero tenant alerts: unknown\nActive non-zero tenant silences: unknown")
	}

	silences, err := s.Alerts.ActiveSilences(ctx)
	silenceCount := "unknown"
	if err != nil {
		s.logger().Printf("Alertmanager silences count failed for chat %d: %v", chatID, err)
	} else {
		silenceCount = strconv.Itoa(len(silences))
	}

	alertmanagerState := "unavailable"
	if status.Ready {
		alertmanagerState = "ready"
	}
	return s.Telegram.SendMessage(ctx, chatID, fmt.Sprintf(
		"Bot: ok\nAlertmanager: %s\nActive non-zero tenant alerts: %d\nActive non-zero tenant silences: %s",
		alertmanagerState,
		status.ActiveTenantAlerts,
		silenceCount,
	))
}

func (s *Service) replySilences(ctx context.Context, chatID int64) error {
	silences, err := s.Alerts.ActiveSilences(ctx)
	if err != nil {
		s.logger().Printf("Alertmanager silences fetch failed for chat %d: %v", chatID, err)
		return s.Telegram.SendMessage(ctx, chatID, "Could not fetch active Alertmanager silences right now.")
	}
	messages, err := RenderSilenceMessages(silences, s.messageLimit())
	if err != nil {
		s.logger().Printf("render active silences failed for chat %d: %v", chatID, err)
		return s.Telegram.SendMessage(ctx, chatID, "Could not format the active silences right now.")
	}
	for _, message := range messages {
		if err := s.Telegram.SendMessage(ctx, chatID, message); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) replyCheck(ctx context.Context, chatID int64, fields []string) error {
	if len(fields) != 3 {
		return s.Telegram.SendMessage(ctx, chatID, "Usage: <code>/check instance range</code>\nExample: <code>/check node-01 1h</code>")
	}
	window, err := parseCheckWindow(fields[2])
	if err != nil {
		return s.Telegram.SendMessage(ctx, chatID, "Invalid range. Use 1m..24h, for example: 15m, 1h, 1d.")
	}
	check, err := s.Alerts.CheckInstance(ctx, TenantOne, fields[1], window)
	if err != nil {
		s.logger().Printf("metrics check failed for chat %d instance %s window %s: %v", chatID, fields[1], window, err)
		return s.Telegram.SendMessage(ctx, chatID, "Could not fetch instance metrics right now.")
	}
	return s.Telegram.SendMessage(ctx, chatID, RenderInstanceCheckMessage(check))
}

func (s *Service) replyCoverage(ctx context.Context, chatID int64, fields []string) error {
	if len(fields) != 2 {
		return s.Telegram.SendMessage(ctx, chatID, "Usage: <code>/coverage instance</code>\nExample: <code>/coverage node-01</code>")
	}
	coverage, err := s.Alerts.CoverageInstance(ctx, TenantOne, fields[1])
	if err != nil {
		s.logger().Printf("coverage check failed for chat %d instance %s: %v", chatID, fields[1], err)
		return s.Telegram.SendMessage(ctx, chatID, "Could not fetch instance alert coverage right now.")
	}
	return s.Telegram.SendMessage(ctx, chatID, RenderInstanceCoverageMessage(coverage))
}

func (s *Service) replyAlerts(ctx context.Context, chatID int64, includeIDs bool) error {
	alerts, err := s.Alerts.ActiveTenantAlerts(ctx, TenantNonZero)
	if err != nil {
		s.logger().Printf("Alertmanager fetch failed for chat %d: %v", chatID, err)
		return s.Telegram.SendMessage(ctx, chatID, "Could not fetch active Alertmanager alerts right now.")
	}

	render := RenderAlertMessages
	if includeIDs {
		render = RenderAlertIDMessages
	}
	messages, err := render(alerts, s.messageLimit(), s.ExpandableQuotes)
	if err != nil {
		s.logger().Printf("render active alerts failed for chat %d: %v", chatID, err)
		return s.Telegram.SendMessage(ctx, chatID, "Could not format the active alerts right now.")
	}
	for _, message := range messages {
		if err := s.Telegram.SendMessage(ctx, chatID, message); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) silenceAlert(ctx context.Context, chatID int64, message *Message, fields []string) error {
	if len(fields) < 3 {
		return s.Telegram.SendMessage(ctx, chatID, silenceUsage)
	}

	duration, err := parseSilenceDuration(fields[len(fields)-1])
	if err != nil {
		return s.Telegram.SendMessage(ctx, chatID, "Invalid duration. Use seconds, minutes, hours, days, or month: 10s, 10m, 10h, 10d, 1month.")
	}

	target := strings.Join(fields[1:len(fields)-1], "")
	if strings.Contains(target, "=") {
		return s.silenceLabelMatchers(ctx, chatID, message, target, duration)
	}
	if len(fields) != 3 {
		return s.Telegram.SendMessage(ctx, chatID, silenceUsage)
	}

	alerts, err := s.Alerts.ActiveTenantAlerts(ctx, TenantNonZero)
	if err != nil {
		s.logger().Printf("Alertmanager fetch before silence failed for chat %d: %v", chatID, err)
		return s.Telegram.SendMessage(ctx, chatID, "Could not fetch active Alertmanager alerts right now.")
	}
	alert, ok := alertByID(alerts, fields[1])
	if !ok {
		return s.Telegram.SendMessage(ctx, chatID, "Active non-zero tenant alert id not found. Send /id for current alert ids.")
	}
	silence, err := s.Alerts.SilenceAlert(ctx, alert, duration, telegramOperator(message), "")
	if err != nil {
		s.logger().Printf("Alertmanager silence failed for chat %d alert %s: %v", chatID, alert.Fingerprint, err)
		return s.Telegram.SendMessage(ctx, chatID, "Could not create Alertmanager silence right now.")
	}
	return s.Telegram.SendMessage(ctx, chatID, fmt.Sprintf(
		"Silenced <code>%s</code> until %s.\nSilence id: <code>%s</code>",
		html.EscapeString(alert.Fingerprint),
		html.EscapeString(silence.EndsAt.Format(time.RFC3339)),
		html.EscapeString(silence.ID),
	))
}

func (s *Service) silenceLabelMatchers(ctx context.Context, chatID int64, message *Message, expression string, duration time.Duration) error {
	matchers, err := parseSilenceMatchers(expression)
	if err != nil {
		return s.Telegram.SendMessage(ctx, chatID, "Invalid matchers. Use labels like <code>instance=host,job=node_exporter</code> or <code>instance=~^node-.*</code>; tenant must be explicit non-zero.")
	}
	silence, err := s.Alerts.SilenceMatchers(ctx, matchers, duration, telegramOperator(message), "Silenced from Telegram by labels: "+formatMatcherExpression(matchers))
	if err != nil {
		s.logger().Printf("Alertmanager label silence failed for chat %d matchers %s: %v", chatID, formatMatcherExpression(matchers), err)
		return s.Telegram.SendMessage(ctx, chatID, "Could not create Alertmanager silence right now.")
	}
	return s.Telegram.SendMessage(ctx, chatID, fmt.Sprintf(
		"Silenced labels <code>%s</code> until %s.\nSilence id: <code>%s</code>",
		html.EscapeString(formatMatcherExpression(matchers)),
		html.EscapeString(silence.EndsAt.Format(time.RFC3339)),
		html.EscapeString(silence.ID),
	))
}

func (s *Service) ackAlert(ctx context.Context, chatID int64, message *Message, fields []string) error {
	if len(fields) != 2 {
		return s.Telegram.SendMessage(ctx, chatID, "Usage: <code>/ack alert-id</code>")
	}

	alerts, err := s.Alerts.ActiveTenantAlerts(ctx, TenantNonZero)
	if err != nil {
		s.logger().Printf("Alertmanager fetch before ack failed for chat %d: %v", chatID, err)
		return s.Telegram.SendMessage(ctx, chatID, "Could not fetch active Alertmanager alerts right now.")
	}
	alert, ok := alertByID(alerts, fields[1])
	if !ok {
		return s.Telegram.SendMessage(ctx, chatID, "Active non-zero tenant alert id not found. Send /id for current alert ids.")
	}
	silence, err := s.Alerts.SilenceAlert(ctx, alert, 30*time.Minute, telegramOperator(message), "Acked from Telegram for active alert "+alert.Fingerprint)
	if err != nil {
		s.logger().Printf("Alertmanager ack failed for chat %d alert %s: %v", chatID, alert.Fingerprint, err)
		return s.Telegram.SendMessage(ctx, chatID, "Could not create Alertmanager silence right now.")
	}
	return s.Telegram.SendMessage(ctx, chatID, fmt.Sprintf(
		"Acked <code>%s</code> for 30m until %s.\nSilence id: <code>%s</code>",
		html.EscapeString(alert.Fingerprint),
		html.EscapeString(silence.EndsAt.Format(time.RFC3339)),
		html.EscapeString(silence.ID),
	))
}

func (s *Service) unsilence(ctx context.Context, chatID int64, fields []string) error {
	if len(fields) != 2 {
		return s.Telegram.SendMessage(ctx, chatID, "Usage: <code>/unsilence silence-id</code>")
	}
	id := strings.TrimSpace(fields[1])
	if id == "" {
		return s.Telegram.SendMessage(ctx, chatID, "Usage: <code>/unsilence silence-id</code>")
	}
	if err := s.Alerts.ExpireSilence(ctx, id); err != nil {
		s.logger().Printf("Alertmanager unsilence failed for chat %d silence %s: %v", chatID, id, err)
		return s.Telegram.SendMessage(ctx, chatID, "Could not expire Alertmanager silence right now.")
	}
	return s.Telegram.SendMessage(ctx, chatID, "Expired silence <code>"+html.EscapeString(id)+"</code>.")
}

var silenceDurationPattern = regexp.MustCompile(`^([1-9][0-9]*)(s|m|h|d|month)$`)
var silenceLabelNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

var allowedSilenceMatcherLabels = map[string]struct{}{
	"alertgroup": {},
	"alertname":  {},
	"device":     {},
	"domain":     {},
	"instance":   {},
	"job":        {},
	"kind":       {},
	"mountpoint": {},
	"name":       {},
	"service":    {},
	"severity":   {},
	"tenant":     {},
	"unit":       {},
}

var requiredSilenceTargetLabels = map[string]struct{}{
	"alertgroup": {},
	"alertname":  {},
	"device":     {},
	"instance":   {},
	"job":        {},
	"mountpoint": {},
	"name":       {},
	"service":    {},
	"unit":       {},
}

func parseSilenceDuration(value string) (time.Duration, error) {
	match := silenceDurationPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("invalid silence duration %q", value)
	}
	amount, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, fmt.Errorf("parse silence duration %q: %w", value, err)
	}
	switch match[2] {
	case "s", "m", "h":
		return time.ParseDuration(value)
	case "d":
		return time.Duration(amount) * 24 * time.Hour, nil
	case "month":
		return time.Duration(amount) * 30 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported silence duration %q", value)
	}
}

func parseSilenceMatchers(expression string) ([]SilenceMatcher, error) {
	parts := strings.Split(strings.TrimSpace(expression), ",")
	if len(parts) == 0 || len(parts) > 8 {
		return nil, fmt.Errorf("invalid matcher count")
	}

	seen := make(map[string]SilenceMatcher, len(parts)+1)
	hasTargetMatcher := false
	for _, part := range parts {
		matcher, err := parseSilenceMatcherPart(part)
		if err != nil {
			return nil, err
		}
		if _, ok := allowedSilenceMatcherLabels[matcher.Name]; !ok {
			return nil, fmt.Errorf("matcher label %q is not allowed", matcher.Name)
		}
		if old, ok := seen[matcher.Name]; ok && !sameSilenceMatcher(old, matcher) {
			return nil, fmt.Errorf("duplicate matcher label %q", matcher.Name)
		}
		if matcher.Name == "tenant" && !matcherTargetsExplicitNonZeroTenant(matcher) {
			return nil, fmt.Errorf("tenant matcher must target explicit non-zero tenants")
		}
		if _, ok := requiredSilenceTargetLabels[matcher.Name]; ok {
			if matcher.IsRegex && regexMatchesEmpty(matcher.Value) {
				return nil, fmt.Errorf("target matcher %q regex matches empty string", matcher.Name)
			}
			hasTargetMatcher = true
		}
		seen[matcher.Name] = matcher
	}
	if !hasTargetMatcher {
		return nil, fmt.Errorf("at least one target matcher is required")
	}
	if _, ok := seen["tenant"]; !ok {
		seen["tenant"] = SilenceMatcher{
			Name:    "tenant",
			Value:   TenantOne,
			IsEqual: true,
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	matchers := make([]SilenceMatcher, 0, len(names))
	for _, name := range names {
		matchers = append(matchers, seen[name])
	}
	return matchers, nil
}

func parseSilenceMatcherPart(part string) (SilenceMatcher, error) {
	part = strings.TrimSpace(part)
	var name, value string
	isRegex := false
	if left, right, ok := strings.Cut(part, "=~"); ok {
		name, value, isRegex = left, right, true
	} else if left, right, ok := strings.Cut(part, "="); ok {
		name, value = left, right
	} else {
		return SilenceMatcher{}, fmt.Errorf("matcher %q has no supported operator", part)
	}

	name = strings.TrimSpace(name)
	value = trimMatcherValue(value)
	if name == "" || value == "" || !silenceLabelNamePattern.MatchString(name) {
		return SilenceMatcher{}, fmt.Errorf("invalid matcher %q", part)
	}
	matcher := SilenceMatcher{
		Name:    name,
		Value:   value,
		IsRegex: isRegex,
		IsEqual: true,
	}
	if isRegex {
		if _, err := regexp.Compile(value); err != nil {
			return SilenceMatcher{}, fmt.Errorf("invalid regex matcher %q: %w", part, err)
		}
	}
	return matcher, nil
}

func sameSilenceMatcher(left, right SilenceMatcher) bool {
	return left.Name == right.Name &&
		left.Value == right.Value &&
		left.IsRegex == right.IsRegex &&
		left.IsEqual == right.IsEqual
}

func regexMatchesEmpty(pattern string) bool {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return true
	}
	return re.MatchString("")
}

func trimMatcherValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return strings.TrimSpace(value[1 : len(value)-1])
		}
	}
	return value
}

func formatMatcherExpression(matchers []SilenceMatcher) string {
	parts := make([]string, 0, len(matchers))
	for _, matcher := range normalizeMatchers(matchers) {
		if !matcher.IsEqual {
			continue
		}
		operator := "="
		if matcher.IsRegex {
			operator = "=~"
		}
		parts = append(parts, matcher.Name+operator+matcher.Value)
	}
	return strings.Join(parts, ",")
}

func alertByID(alerts []Alert, id string) (Alert, bool) {
	for _, alert := range alerts {
		if alert.Fingerprint == id {
			return alert, true
		}
	}
	return Alert{}, false
}

var checkWindowPattern = regexp.MustCompile(`^([1-9][0-9]*)(m|h|d)$`)

func parseCheckWindow(value string) (string, error) {
	match := checkWindowPattern.FindStringSubmatch(value)
	if match == nil {
		return "", fmt.Errorf("invalid check window %q", value)
	}
	amount, err := strconv.Atoi(match[1])
	if err != nil {
		return "", fmt.Errorf("parse check window %q: %w", value, err)
	}
	var duration time.Duration
	switch match[2] {
	case "m":
		duration = time.Duration(amount) * time.Minute
	case "h":
		duration = time.Duration(amount) * time.Hour
	case "d":
		duration = time.Duration(amount) * 24 * time.Hour
	default:
		return "", fmt.Errorf("unsupported check window %q", value)
	}
	if duration < time.Minute || duration > 24*time.Hour {
		return "", fmt.Errorf("check window %q outside 1m..24h", value)
	}
	return value, nil
}

func isDeployJokeIntent(text string) bool {
	for _, token := range strings.FieldsFunc(text, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
	}) {
		if strings.EqualFold(token, "deploy") || strings.EqualFold(token, "деплой") {
			return true
		}
	}
	return false
}

func (s *Service) shouldReplyDeployJoke() bool {
	if s.DeployJokeChance != nil {
		return s.DeployJokeChance()
	}
	n, err := rand.Int(rand.Reader, big.NewInt(4))
	if err != nil {
		return false
	}
	return n.Int64() == 0
}

func randomDeployJoke() string {
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(deployJokes))))
	if err != nil {
		return html.EscapeString(deployJokes[0])
	}
	return html.EscapeString(deployJokes[index.Int64()])
}

var deployJokes = []string{
	"Ура, ура, деплойчик",
	"Сегодня пятница?)",
	"Опять в пятницу?)",
	"Клик не падает, при деплое алерты не потеряем))",
	"Деплой пошел, дежурный морально пристегнулся",
	"Сначала деплой, потом чай, потом объяснительная",
	"Если алерты молчат, значит они тоже на ревью",
	"Катим тихо, как будто так и было задумано",
	"Прод не трогаем, он сам все видел",
	"Деплой без отката - это стендап с элементами риска",
	"Синий график вниз, зеленый график вверх, настроение стабильно",
	"AntiDDoS держится, SRE улыбается глазами",
	"Пятничный деплой считается пятничным только после алерта",
	"Кнопка нажата, совесть в read-only",
	"Если что, это был canary на весь кластер",
	"Релиз малый, вера большая",
	"Пока метрики грузятся, деплой уже в истории",
	"Дежурный сказал можно, но без свидетелей",
	"Откат рядом, значит можно смелее",
	"Никакой паники, только контролируемая турбулентность",
	"Деплой прошел, теперь проверяем, был ли он нужен",
	"Чем тише changelog, тем громче сердце SRE",
	"Сервис перезапущен, легенда обновлена",
	"Бинарь свежий, журнал внимательный",
	"План был надежный, особенно пункт про удачу",
	"У нас не баг, у нас постдеплойная диагностика",
	"Графики приняли деплой без лишних вопросов",
	"Сейчас все стабильно, но это не повод расслабляться",
	"Деплой на одну минуту, воспоминаний на весь спринт",
	"Проверка прошла, можно снова делать вид, что это рутина",
}

const silenceUsage = "Usage: <code>/silence alert-id duration</code>\nOr: <code>/silence instance=host,job=node_exporter duration</code>\nOr: <code>/silence instance=~^node-.* duration</code>\nDurations: 10s, 10m, 10h, 10d, 1month."

const helpMessage = "Commands:\n/? - show active non-zero tenant alerts\n/id - show active non-zero tenant alerts with Alertmanager ids\n/status - show bot and Alertmanager status\n/silences - show active non-zero tenant silences\n/check instance range - compact node_exporter metrics for one instance\n/coverage instance - alert rule coverage for one instance\n/silence alert-id duration - silence one active alert selected by id\n/silence label=value|label=~regex,... duration - silence non-zero tenant alerts by labels\n/ack alert-id - silence one active alert for 30m\n/unsilence silence-id - expire one active silence by id\ndeploy - probabilistic non-mutating deploy joke\n/help - show this command list\n\nSilence example: /silence instance=node-01,job=node_exporter 2h\nRegex silence example: /silence instance=~^node-.* 2h\nSilence durations: 10s, 10m, 10h, 10d, 1month.\nCheck ranges: 15m, 1h, 1d."

func telegramOperator(message *Message) string {
	if message == nil || message.From == nil {
		return "alert-list-bot"
	}
	user := message.From
	name := strings.TrimSpace(user.Username)
	if name != "" {
		name = "@" + name
	} else {
		name = strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	}
	if name == "" {
		name = "telegram-user"
	}
	if user.ID != 0 {
		return fmt.Sprintf("telegram %s (id %d)", name, user.ID)
	}
	return "telegram " + name
}

func (s *Service) messageLimit() int {
	if s.MessageLimit > 0 {
		return s.MessageLimit
	}
	return DefaultTelegramMessageLimit
}

func (s *Service) logger() *log.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return log.Default()
}

func sleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
