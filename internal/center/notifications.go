package center

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type NotificationEngine struct {
	dir       string
	config    NotificationConfig
	legacyURL string
	mu        sync.Mutex
}

func LoadNotificationConfig(path string) (NotificationConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return NotificationConfig{}, fmt.Errorf("read notification configuration: %w", err)
	}
	var config NotificationConfig
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return NotificationConfig{}, fmt.Errorf("decode notification configuration: %w", err)
	}
	if err := validateNotificationConfig(&config); err != nil {
		return NotificationConfig{}, err
	}
	return config, nil
}

func NewNotificationEngine(dataDir string, config *NotificationConfig, legacyURL string) (*NotificationEngine, error) {
	value := NotificationConfig{}
	if config != nil {
		value = *config
	}
	if legacyURL != "" {
		if _, err := url.ParseRequestURI(legacyURL); err != nil {
			return nil, fmt.Errorf("invalid legacy webhook URL: %w", err)
		}
		value.Channels = append(value.Channels, NotificationChannel{ID: "legacy-webhook", Type: "webhook", Enabled: true})
	}
	if err := validateNotificationConfig(&value); err != nil {
		return nil, err
	}
	return &NotificationEngine{dir: filepath.Join(dataDir, "notifications"), config: value, legacyURL: legacyURL}, nil
}

func validateNotificationConfig(config *NotificationConfig) error {
	if config.Policy.DedupeWindowSeconds <= 0 {
		config.Policy.DedupeWindowSeconds = 900
	}
	if config.Policy.MaxQueue <= 0 {
		config.Policy.MaxQueue = 5000
	}
	if config.Policy.MaxAttempts <= 0 {
		config.Policy.MaxAttempts = 8
	}
	if config.Policy.InitialRetrySeconds <= 0 {
		config.Policy.InitialRetrySeconds = 5
	}
	if config.Policy.MaxQueue > 100000 || config.Policy.MaxAttempts > 20 || config.Policy.DedupeWindowSeconds > 7*24*3600 || config.Policy.InitialRetrySeconds > 3600 {
		return errors.New("notification policy exceeds safe limits")
	}
	seen := map[string]bool{}
	allowedTypes := map[string]bool{"webhook": true, "feishu": true, "wecom": true, "dingtalk": true, "slack": true, "telegram": true, "smtp": true}
	for _, channel := range config.Channels {
		if !validID.MatchString(channel.ID) || seen[channel.ID] {
			return fmt.Errorf("notification channel id is invalid or duplicated: %s", channel.ID)
		}
		seen[channel.ID] = true
		if !allowedTypes[channel.Type] {
			return fmt.Errorf("unsupported notification channel type: %s", channel.Type)
		}
		if channel.Type == "smtp" {
			if channel.SMTP == nil || channel.SMTP.Host == "" || channel.SMTP.Port < 1 || channel.SMTP.Port > 65535 || len(channel.SMTP.To) == 0 {
				return fmt.Errorf("SMTP channel %s is incomplete", channel.ID)
			}
			if _, err := mail.ParseAddress(channel.SMTP.From); err != nil {
				return fmt.Errorf("SMTP channel %s has invalid from address", channel.ID)
			}
			for _, recipient := range channel.SMTP.To {
				if _, err := mail.ParseAddress(recipient); err != nil {
					return fmt.Errorf("SMTP channel %s has invalid recipient", channel.ID)
				}
			}
		} else if channel.Type == "telegram" {
			if channel.Telegram == nil || channel.Telegram.TokenEnv == "" || channel.Telegram.ChatID == "" {
				return fmt.Errorf("telegram channel %s is incomplete", channel.ID)
			}
		} else if channel.ID != "legacy-webhook" && channel.EndpointEnv == "" {
			return fmt.Errorf("notification channel %s must use endpoint_env", channel.ID)
		}
		for header, envName := range channel.HeadersEnv {
			if validateOutboundHeaderName(header) != nil || strings.TrimSpace(envName) == "" {
				return fmt.Errorf("notification channel %s contains an invalid header environment mapping", channel.ID)
			}
		}
	}
	for _, suppression := range config.Policy.Suppressions {
		if observedTime(suppression.Until).IsZero() {
			return errors.New("notification suppression until must use RFC3339")
		}
	}
	for _, window := range config.Policy.DeploymentWindows {
		if observedTime(window.Start).IsZero() || observedTime(window.End).IsZero() || !observedTime(window.Start).Before(observedTime(window.End)) {
			return errors.New("deployment window start/end must be a valid RFC3339 range")
		}
	}
	return nil
}

func (e *NotificationEngine) deliveriesPath() string { return filepath.Join(e.dir, "deliveries.json") }

func (e *NotificationEngine) readDeliveries() ([]NotificationDelivery, error) {
	var deliveries []NotificationDelivery
	err := readJSONFile(e.deliveriesPath(), &deliveries)
	if errors.Is(err, os.ErrNotExist) {
		return []NotificationDelivery{}, nil
	}
	return deliveries, err
}

func (e *NotificationEngine) writeDeliveries(deliveries []NotificationDelivery) error {
	if err := os.MkdirAll(e.dir, 0o750); err != nil {
		return err
	}
	return writeJSONFile(e.deliveriesPath(), deliveries)
}

func (e *NotificationEngine) Enqueue(alerts []Alert) error {
	if len(alerts) == 0 || len(e.config.Channels) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	deliveries, err := e.readDeliveries()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, channel := range e.config.Channels {
		if !channel.Enabled {
			continue
		}
		ready := make([]Alert, 0, len(alerts))
		for _, alert := range alerts {
			if !e.isDuplicate(deliveries, channel.ID, alert.ID, now) {
				ready = append(ready, alert)
			}
		}
		if len(ready) == 0 {
			continue
		}
		nowText := now.Format(time.RFC3339Nano)
		active, suppressed := []Alert{}, []Alert{}
		reasons := []string{}
		for _, alert := range ready {
			if reason := e.alertSuppressionReason(alert, now); reason != "" {
				suppressed = append(suppressed, alert)
				reasons = append(reasons, reason)
			} else {
				active = append(active, alert)
			}
		}
		if len(active) > 0 {
			deliveries = append(deliveries, newNotificationDelivery(channel, active, "pending", "", nowText))
		}
		if len(suppressed) > 0 {
			deliveries = append(deliveries, newNotificationDelivery(channel, suppressed, "suppressed", strings.Join(uniqueStrings(reasons), "; "), nowText))
		}
	}
	deliveries, err = e.enforceQueueLimit(deliveries)
	if err != nil {
		return err
	}
	return e.writeDeliveries(deliveries)
}

func (e *NotificationEngine) isDuplicate(deliveries []NotificationDelivery, channelID, alertID string, now time.Time) bool {
	window := time.Duration(e.config.Policy.DedupeWindowSeconds) * time.Second
	for i := len(deliveries) - 1; i >= 0; i-- {
		delivery := deliveries[i]
		created := observedTime(delivery.CreatedAt)
		if !created.IsZero() && now.Sub(created) > window {
			break
		}
		if delivery.ChannelID == channelID && delivery.Status != "failed" && containsString(delivery.AlertIDs, alertID) {
			return true
		}
	}
	return false
}

func newNotificationDelivery(channel NotificationChannel, alerts []Alert, status, reason, now string) NotificationDelivery {
	ids := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		ids = append(ids, alert.ID)
	}
	sort.Strings(ids)
	return NotificationDelivery{ID: stableID("delivery", channel.ID, strings.Join(ids, ","), now), ChannelID: channel.ID, ChannelType: channel.Type, AlertIDs: ids, Status: status, CreatedAt: now, UpdatedAt: now, NextAttemptAt: now, SuppressedBy: reason, Alerts: alerts}
}

func (e *NotificationEngine) alertSuppressionReason(alert Alert, now time.Time) string {
	for _, suppression := range e.config.Policy.Suppressions {
		if now.Before(observedTime(suppression.Until)) && (suppression.NodeID == "" || suppression.NodeID == alert.NodeID) && (suppression.Kind == "" || suppression.Kind == alert.Kind) {
			return "suppression: " + suppression.Reason
		}
	}
	for _, window := range e.config.Policy.DeploymentWindows {
		if !now.Before(observedTime(window.Start)) && now.Before(observedTime(window.End)) && (window.NodeID == "" || window.NodeID == alert.NodeID) && !(window.AllowCritical && alert.Severity == "critical") {
			return "deployment window: " + window.Reason
		}
	}
	return ""
}

func (e *NotificationEngine) enforceQueueLimit(deliveries []NotificationDelivery) ([]NotificationDelivery, error) {
	if len(deliveries) <= e.config.Policy.MaxQueue {
		return deliveries, nil
	}
	remove := len(deliveries) - e.config.Policy.MaxQueue
	out := make([]NotificationDelivery, 0, e.config.Policy.MaxQueue)
	for _, delivery := range deliveries {
		if remove > 0 && (delivery.Status == "delivered" || delivery.Status == "suppressed" || delivery.Status == "failed") {
			remove--
			continue
		}
		out = append(out, delivery)
	}
	if remove > 0 {
		return nil, errors.New("notification queue is full of pending deliveries")
	}
	return out, nil
}

func (e *NotificationEngine) DrainDue(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	deliveries, err := e.readDeliveries()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	changed := false
	for index := range deliveries {
		delivery := &deliveries[index]
		if delivery.Status != "pending" || (!observedTime(delivery.NextAttemptAt).IsZero() && observedTime(delivery.NextAttemptAt).After(now)) {
			continue
		}
		channel, ok := e.channel(delivery.ChannelID)
		if !ok {
			delivery.Status, delivery.LastError = "failed", "notification channel no longer exists"
			delivery.UpdatedAt = now.Format(time.RFC3339Nano)
			changed = true
			continue
		}
		delivery.Attempts++
		err := e.deliver(ctx, channel, delivery.Alerts)
		delivery.UpdatedAt = now.Format(time.RFC3339Nano)
		if err == nil {
			delivery.Status, delivery.DeliveredAt, delivery.NextAttemptAt, delivery.LastError = "delivered", delivery.UpdatedAt, "", ""
		} else {
			delivery.LastError = redactNotificationError(err.Error())
			if delivery.Attempts >= e.config.Policy.MaxAttempts {
				delivery.Status, delivery.NextAttemptAt = "failed", ""
			} else {
				delay := retryDelay(e.config.Policy.InitialRetrySeconds, delivery.Attempts)
				delivery.NextAttemptAt = now.Add(delay).Format(time.RFC3339Nano)
			}
		}
		changed = true
	}
	if changed {
		return e.writeDeliveries(deliveries)
	}
	return nil
}

func retryDelay(initial, attempts int) time.Duration {
	delay := time.Duration(initial) * time.Second
	for i := 1; i < attempts && delay < time.Hour; i++ {
		delay *= 2
	}
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}

func (e *NotificationEngine) channel(id string) (NotificationChannel, bool) {
	for _, channel := range e.config.Channels {
		if channel.ID == id {
			return channel, true
		}
	}
	return NotificationChannel{}, false
}

func (e *NotificationEngine) deliver(ctx context.Context, channel NotificationChannel, alerts []Alert) error {
	if channel.Type == "smtp" {
		return e.deliverSMTP(ctx, *channel.SMTP, channel.AllowPrivateNetwork, alerts)
	}
	endpoint := ""
	if channel.ID == "legacy-webhook" {
		endpoint = e.legacyURL
	} else if channel.Type == "telegram" {
		token := os.Getenv(channel.Telegram.TokenEnv)
		if token == "" {
			return fmt.Errorf("required environment variable %s is empty", channel.Telegram.TokenEnv)
		}
		endpoint = "https://api.telegram.org/bot" + url.PathEscape(token) + "/sendMessage"
	} else {
		endpoint = os.Getenv(channel.EndpointEnv)
	}
	if endpoint == "" {
		return fmt.Errorf("required endpoint environment variable %s is empty", channel.EndpointEnv)
	}
	if _, err := validateOutboundURL(endpoint); err != nil {
		return fmt.Errorf("invalid notification endpoint: %w", err)
	}
	payload := notificationPayload(channel, alerts)
	b, _ := json.Marshal(payload)
	// #nosec G704 -- outboundHTTPClient validates DNS results at dial time and blocks redirects.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for header, envName := range channel.HeadersEnv {
		value := os.Getenv(envName)
		if value == "" {
			return fmt.Errorf("required header environment variable %s is empty", envName)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("header environment variable %s contains a line break", envName)
		}
		req.Header.Set(header, value)
	}
	// #nosec G704 -- the hardened client rejects special/private destinations unless explicitly trusted.
	resp, err := newOutboundHTTPClient(10*time.Second, channel.AllowPrivateNetwork).Do(req)
	if err != nil {
		return errors.New("notification request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notification endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func notificationPayload(channel NotificationChannel, alerts []Alert) any {
	lines := make([]string, 0, len(alerts)+1)
	lines = append(lines, fmt.Sprintf("FleetScope: %d new alert(s)", len(alerts)))
	for _, alert := range alerts {
		lines = append(lines, fmt.Sprintf("[%s] %s / %s: %s", strings.ToUpper(alert.Severity), alert.NodeID, alert.Title, alert.Detail))
	}
	text := strings.Join(lines, "\n")
	switch channel.Type {
	case "feishu":
		return map[string]any{"msg_type": "text", "content": map[string]string{"text": text}}
	case "wecom":
		return map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": text}}
	case "dingtalk":
		return map[string]any{"msgtype": "markdown", "markdown": map[string]string{"title": "FleetScope alert", "text": text}}
	case "slack":
		return map[string]string{"text": text}
	case "telegram":
		return map[string]any{"chat_id": channel.Telegram.ChatID, "text": text, "disable_web_page_preview": true}
	default:
		return map[string]any{"schema": "fleetscope.notification/v1", "event": "fleet.alert.opened", "alerts": alerts}
	}
}

func (e *NotificationEngine) deliverSMTP(ctx context.Context, config SMTPChannel, allowPrivate bool, alerts []Alert) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	subject := fmt.Sprintf("[FleetScope] %d new alert(s)", len(alerts))
	body := notificationPayload(NotificationChannel{Type: "slack"}, alerts).(map[string]string)["text"]
	message := []byte("From: " + config.From + "\r\nTo: " + strings.Join(config.To, ", ") + "\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body + "\r\n")
	address := net.JoinHostPort(config.Host, fmt.Sprint(config.Port))
	var auth smtp.Auth
	if config.UsernameEnv != "" {
		username, password := os.Getenv(config.UsernameEnv), os.Getenv(config.PasswordEnv)
		if username == "" || password == "" {
			return errors.New("SMTP credential environment variables are empty")
		}
		auth = smtp.PlainAuth("", username, password, config.Host)
	}
	connection, err := dialOutboundContext(ctx, &net.Dialer{Timeout: 10 * time.Second}, "tcp", address, allowPrivate)
	if err != nil {
		return err
	}
	if config.TLS {
		tlsConnection := tls.Client(connection, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.Host})
		if err = tlsConnection.HandshakeContext(ctx); err != nil {
			_ = connection.Close()
			return err
		}
		connection = tlsConnection
	}
	defer connection.Close()
	client, err := smtp.NewClient(connection, config.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(config.From); err != nil {
		return err
	}
	for _, recipient := range config.To {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(message); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func redactNotificationError(value string) string {
	if len(value) > 500 {
		value = value[:500]
	}
	if parsed, err := url.Parse(value); err == nil && parsed.User != nil {
		parsed.User = nil
		value = parsed.String()
	}
	return value
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (s *Server) notifyAlerts(alerts []Alert) {
	if s.notifications == nil || len(alerts) == 0 {
		return
	}
	if err := s.notifications.Enqueue(alerts); err != nil {
		fmt.Fprintf(os.Stderr, "fleetscope notification enqueue: %v\n", err)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.notifications.DrainDue(ctx)
	}()
}

func (s *Server) RunNotificationWorker(ctx context.Context) {
	if s.notifications == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		_ = s.notifications.DrainDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) notificationStatus(w http.ResponseWriter, _ *http.Request) {
	if s.notifications == nil {
		writeJSON(w, map[string]any{"enabled": false, "channels": []any{}})
		return
	}
	channels := make([]map[string]any, 0, len(s.notifications.config.Channels))
	for _, channel := range s.notifications.config.Channels {
		channels = append(channels, map[string]any{"id": channel.ID, "type": channel.Type, "enabled": channel.Enabled, "configured": channel.ID == "legacy-webhook" || channel.Type == "smtp" || channel.Type == "telegram" || os.Getenv(channel.EndpointEnv) != ""})
	}
	writeJSON(w, map[string]any{"enabled": len(channels) > 0, "channels": channels, "policy": s.notifications.config.Policy})
}

func (s *Server) listNotificationDeliveries(w http.ResponseWriter, r *http.Request) {
	s.notifications.mu.Lock()
	defer s.notifications.mu.Unlock()
	deliveries, err := s.notifications.readDeliveries()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := r.URL.Query().Get("status")
	out := []NotificationDelivery{}
	for i := len(deliveries) - 1; i >= 0; i-- {
		if status == "" || deliveries[i].Status == status {
			out = append(out, deliveries[i])
		}
	}
	writeJSON(w, out)
}

func (s *Server) retryNotificationDelivery(w http.ResponseWriter, r *http.Request) {
	s.notifications.mu.Lock()
	deliveries, err := s.notifications.readDeliveries()
	if err != nil {
		s.notifications.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for index := range deliveries {
		if deliveries[index].ID != r.PathValue("id") {
			continue
		}
		if deliveries[index].Status == "delivered" {
			s.notifications.mu.Unlock()
			writeError(w, http.StatusConflict, "delivered notification cannot be retried")
			return
		}
		deliveries[index].Status, deliveries[index].Attempts, deliveries[index].LastError = "pending", 0, ""
		deliveries[index].NextAttemptAt, deliveries[index].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)
		err = s.notifications.writeDeliveries(deliveries)
		delivery := deliveries[index]
		s.notifications.mu.Unlock()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = s.notifications.DrainDue(ctx)
		}()
		writeJSON(w, delivery)
		return
	}
	s.notifications.mu.Unlock()
	writeError(w, http.StatusNotFound, "notification delivery not found")
}
