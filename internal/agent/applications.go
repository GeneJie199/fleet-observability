package agent

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/events"
	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

const maxApplicationConfigBytes = 1 << 20

var validEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

type ApplicationConfig struct {
	MaxConcurrency int              `json:"max_concurrency,omitempty"`
	Nginx          []NginxTarget    `json:"nginx,omitempty"`
	Redis          []RedisTarget    `json:"redis,omitempty"`
	Databases      []DatabaseTarget `json:"databases,omitempty"`
	Processes      []ProcessTarget  `json:"processes,omitempty"`
	Docker         []DockerTarget   `json:"docker,omitempty"`
}

type NginxTarget struct {
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	HeaderEnv      map[string]string `json:"header_env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Required       bool              `json:"required,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
}

type RedisTarget struct {
	Name           string            `json:"name"`
	Address        string            `json:"address"`
	UsernameEnv    string            `json:"username_env,omitempty"`
	PasswordEnv    string            `json:"password_env,omitempty"`
	TLS            bool              `json:"tls,omitempty"`
	ServerName     string            `json:"server_name,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Required       bool              `json:"required,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
}

type DatabaseTarget struct {
	Name           string            `json:"name"`
	Engine         string            `json:"engine"`
	DSNEnv         string            `json:"dsn_env"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Required       bool              `json:"required,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
}

type ProcessTarget struct {
	Name     string            `json:"name"`
	Match    string            `json:"match"`
	Required bool              `json:"required,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

type DockerTarget struct {
	Name           string            `json:"name"`
	Socket         string            `json:"socket,omitempty"`
	MaxContainers  int               `json:"max_containers,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Required       bool              `json:"required,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
}

type ApplicationCollector struct {
	Every      time.Duration
	ConfigPath string
	Labels     map[string]string
}

func (collector ApplicationCollector) ID() string { return "applications" }

func (collector ApplicationCollector) Interval() time.Duration {
	if collector.Every < time.Second {
		return 30 * time.Second
	}
	return collector.Every
}

type applicationResult struct {
	name, kind string
	required   bool
	points     []telemetry.Point
	summary    map[string]any
	err        error
}

func (collector ApplicationCollector) Collect(ctx context.Context, observedAt time.Time) (Collection, error) {
	config, err := readApplicationConfig(collector.ConfigPath)
	if err != nil {
		return Collection{}, err
	}
	runners := applicationRunners(config, observedAt, collector.Labels)
	if len(runners) == 0 {
		return Collection{}, errors.New("application config contains no targets")
	}
	concurrency := config.MaxConcurrency
	if concurrency <= 0 {
		concurrency = 4
	}
	results := make(chan applicationResult, len(runners))
	semaphore := make(chan struct{}, concurrency)
	var wait sync.WaitGroup
	for _, runner := range runners {
		wait.Add(1)
		go func(run func(context.Context) applicationResult) {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			results <- run(ctx)
		}(runner)
	}
	wait.Wait()
	close(results)
	collection := Collection{Points: []telemetry.Point{}, Events: []events.Entry{}, ReportMetrics: map[string]any{}}
	summaries := []map[string]any{}
	for result := range results {
		labels := applicationLabels(collector.Labels, nil, result.name, result.kind, result.required)
		up := 1.0
		if result.err != nil {
			up = 0
			severity := "warning"
			if result.required {
				severity = "error"
			}
			collection.Events = append(collection.Events, events.Entry{TimestampMS: observedAt.UnixMilli(), Kind: "collector", Severity: severity, Service: result.name, Message: safeApplicationError(result.err), Attributes: map[string]string{"collector": "applications", "application_kind": result.kind}})
		}
		collection.Points = append(collection.Points, telemetry.Point{Metric: "application_target_up", Labels: labels, TimestampMS: observedAt.UnixMilli(), Value: up, Kind: telemetry.Gauge})
		collection.Points = append(collection.Points, result.points...)
		summary := result.summary
		if summary == nil {
			summary = map[string]any{}
		}
		summary["name"], summary["kind"], summary["up"], summary["required"] = result.name, result.kind, result.err == nil, result.required
		if result.err != nil {
			summary["error"] = safeApplicationError(result.err)
		}
		summaries = append(summaries, summary)
	}
	collection.ReportMetrics["applications"] = summaries
	return collection, nil
}

func readApplicationConfig(path string) (ApplicationConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return ApplicationConfig{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxApplicationConfigBytes+1))
	decoder.DisallowUnknownFields()
	var config ApplicationConfig
	if err := decoder.Decode(&config); err != nil {
		return ApplicationConfig{}, fmt.Errorf("decode application config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return ApplicationConfig{}, errors.New("application config must contain one JSON object")
		}
		return ApplicationConfig{}, fmt.Errorf("invalid trailing application config: %w", err)
	}
	if config.MaxConcurrency < 0 || config.MaxConcurrency > 32 {
		return ApplicationConfig{}, errors.New("max_concurrency must be between 1 and 32")
	}
	seen := map[string]bool{}
	validateName := func(name string) error {
		if !validLogIdentity.MatchString(name) {
			return fmt.Errorf("invalid application target name %q", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate application target name %q", name)
		}
		seen[name] = true
		return nil
	}
	for _, target := range config.Nginx {
		if err := validateName(target.Name); err != nil {
			return ApplicationConfig{}, err
		}
		parsed, err := url.Parse(target.URL)
		if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return ApplicationConfig{}, fmt.Errorf("nginx target %s has an invalid HTTP URL", target.Name)
		}
		for _, environment := range target.HeaderEnv {
			if !validEnvironmentName.MatchString(environment) {
				return ApplicationConfig{}, fmt.Errorf("nginx target %s has an invalid header environment name", target.Name)
			}
		}
	}
	for _, target := range config.Redis {
		if err := validateName(target.Name); err != nil {
			return ApplicationConfig{}, err
		}
		if _, _, err := net.SplitHostPort(target.Address); err != nil {
			return ApplicationConfig{}, fmt.Errorf("redis target %s address: %w", target.Name, err)
		}
		for _, environment := range []string{target.UsernameEnv, target.PasswordEnv} {
			if environment != "" && !validEnvironmentName.MatchString(environment) {
				return ApplicationConfig{}, fmt.Errorf("redis target %s has an invalid credential environment name", target.Name)
			}
		}
	}
	for _, target := range config.Databases {
		if err := validateName(target.Name); err != nil {
			return ApplicationConfig{}, err
		}
		engine := strings.ToLower(target.Engine)
		if engine != "postgres" && engine != "postgresql" && engine != "mysql" {
			return ApplicationConfig{}, fmt.Errorf("database target %s engine must be postgres or mysql", target.Name)
		}
		if !validEnvironmentName.MatchString(target.DSNEnv) {
			return ApplicationConfig{}, fmt.Errorf("database target %s has an invalid dsn_env", target.Name)
		}
	}
	for _, target := range config.Processes {
		if err := validateName(target.Name); err != nil {
			return ApplicationConfig{}, err
		}
		if strings.TrimSpace(target.Match) == "" || len(target.Match) > 256 {
			return ApplicationConfig{}, fmt.Errorf("process target %s match must contain 1 to 256 characters", target.Name)
		}
	}
	for _, target := range config.Docker {
		if err := validateName(target.Name); err != nil {
			return ApplicationConfig{}, err
		}
		if target.MaxContainers < 0 || target.MaxContainers > 500 {
			return ApplicationConfig{}, fmt.Errorf("docker target %s max_containers must be between 1 and 500", target.Name)
		}
	}
	return config, nil
}

func applicationRunners(config ApplicationConfig, observedAt time.Time, baseLabels map[string]string) []func(context.Context) applicationResult {
	runners := []func(context.Context) applicationResult{}
	for _, target := range config.Nginx {
		target := target
		runners = append(runners, func(ctx context.Context) applicationResult { return collectNginx(ctx, target, observedAt, baseLabels) })
	}
	for _, target := range config.Redis {
		target := target
		runners = append(runners, func(ctx context.Context) applicationResult { return collectRedis(ctx, target, observedAt, baseLabels) })
	}
	for _, target := range config.Databases {
		target := target
		runners = append(runners, func(ctx context.Context) applicationResult {
			return collectApplicationDatabase(ctx, target, observedAt, baseLabels)
		})
	}
	for _, target := range config.Processes {
		target := target
		runners = append(runners, func(ctx context.Context) applicationResult {
			return collectProcessTarget(ctx, target, observedAt, baseLabels)
		})
	}
	for _, target := range config.Docker {
		target := target
		runners = append(runners, func(ctx context.Context) applicationResult {
			return collectDockerTarget(ctx, target, observedAt, baseLabels)
		})
	}
	return runners
}

func collectNginx(parent context.Context, target NginxTarget, observedAt time.Time, baseLabels map[string]string) applicationResult {
	result := applicationResult{name: target.Name, kind: "nginx", required: target.Required}
	ctx, cancel := timeout(parent, target.TimeoutSeconds)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
	if err != nil {
		result.err = err
		return result
	}
	for header, environment := range target.HeaderEnv {
		value := os.Getenv(environment)
		if value == "" {
			result.err = fmt.Errorf("required environment variable %s is empty", environment)
			return result
		}
		request.Header.Set(header, value)
	}
	client := &http.Client{Timeout: timeoutDuration(target.TimeoutSeconds)}
	response, err := client.Do(request)
	if err != nil {
		result.err = err
		return result
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 256<<10))
	if err != nil {
		result.err = err
		return result
	}
	if response.StatusCode != http.StatusOK {
		result.err = fmt.Errorf("status endpoint returned HTTP %d", response.StatusCode)
		return result
	}
	metrics, err := parseNginxStatus(string(body))
	if err != nil {
		result.err = err
		return result
	}
	labels := applicationLabels(baseLabels, target.Labels, target.Name, "nginx", target.Required)
	result.points = pointsFromMetrics("nginx_", metrics, observedAt, labels, map[string]bool{"accepted_connections_total": true, "handled_connections_total": true, "http_requests_total": true})
	result.summary = map[string]any{"connections_active": metrics["connections_active"], "http_requests_total": metrics["http_requests_total"]}
	return result
}

func parseNginxStatus(body string) (map[string]float64, error) {
	lines := []string{}
	for _, line := range strings.Split(body, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) < 4 {
		return nil, errors.New("response is not Nginx stub_status format")
	}
	values := map[string]float64{}
	parse := func(name, raw string) error {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("invalid Nginx %s value", name)
		}
		values[name] = value
		return nil
	}
	active := strings.Fields(lines[0])
	counters := strings.Fields(lines[2])
	states := strings.Fields(lines[3])
	if len(active) != 3 || !strings.EqualFold(active[0], "active") || len(counters) != 3 || len(states) != 6 {
		return nil, errors.New("response is not Nginx stub_status format")
	}
	if err := parse("connections_active", active[2]); err != nil {
		return nil, err
	}
	if err := parse("accepted_connections_total", counters[0]); err != nil {
		return nil, err
	}
	if err := parse("handled_connections_total", counters[1]); err != nil {
		return nil, err
	}
	if err := parse("http_requests_total", counters[2]); err != nil {
		return nil, err
	}
	for index, name := range []string{"connections_reading", "connections_writing", "connections_waiting"} {
		if err := parse(name, states[index*2+1]); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func collectRedis(parent context.Context, target RedisTarget, observedAt time.Time, baseLabels map[string]string) applicationResult {
	result := applicationResult{name: target.Name, kind: "redis", required: target.Required}
	ctx, cancel := timeout(parent, target.TimeoutSeconds)
	defer cancel()
	connection, err := dialRedis(ctx, target)
	if err != nil {
		result.err = err
		return result
	}
	defer connection.Close()
	deadline, _ := ctx.Deadline()
	_ = connection.SetDeadline(deadline)
	reader := bufio.NewReader(io.LimitReader(connection, 2<<20))
	writer := bufio.NewWriter(connection)
	username, password := environmentValue(target.UsernameEnv), environmentValue(target.PasswordEnv)
	if target.PasswordEnv != "" && password == "" {
		result.err = fmt.Errorf("required environment variable %s is empty", target.PasswordEnv)
		return result
	}
	if password != "" {
		command := []string{"AUTH", password}
		if username != "" {
			command = []string{"AUTH", username, password}
		}
		if err := writeRedisCommand(writer, command...); err != nil {
			result.err = err
			return result
		}
		if _, err := readRedisReply(reader); err != nil {
			result.err = err
			return result
		}
	}
	if err := writeRedisCommand(writer, "INFO", "ALL"); err != nil {
		result.err = err
		return result
	}
	info, err := readRedisReply(reader)
	if err != nil {
		result.err = err
		return result
	}
	metrics := parseRedisInfo(info)
	labels := applicationLabels(baseLabels, target.Labels, target.Name, "redis", target.Required)
	counters := map[string]bool{"total_commands_processed": true, "expired_keys": true, "evicted_keys": true, "keyspace_hits": true, "keyspace_misses": true, "total_net_input_bytes": true, "total_net_output_bytes": true}
	result.points = pointsFromMetrics("redis_", metrics, observedAt, labels, counters)
	result.summary = map[string]any{"connected_clients": metrics["connected_clients"], "used_memory_bytes": metrics["used_memory_bytes"], "keys": metrics["keys"]}
	return result
}

func dialRedis(ctx context.Context, target RedisTarget) (net.Conn, error) {
	dialer := &net.Dialer{}
	if !target.TLS {
		return dialer.DialContext(ctx, "tcp", target.Address)
	}
	serverName := target.ServerName
	if serverName == "" {
		serverName, _, _ = net.SplitHostPort(target.Address)
	}
	return (&tlsDialer{dialer: dialer, serverName: serverName}).DialContext(ctx, target.Address)
}

func writeRedisCommand(writer *bufio.Writer, parts ...string) error {
	if _, err := fmt.Fprintf(writer, "*%d\r\n", len(parts)); err != nil {
		return err
	}
	for _, part := range parts {
		if _, err := fmt.Fprintf(writer, "$%d\r\n%s\r\n", len(part), part); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func readRedisReply(reader *bufio.Reader) (string, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	switch prefix {
	case '+', ':':
		return line, nil
	case '-':
		return "", errors.New("redis returned an error")
	case '$':
		length, err := strconv.Atoi(line)
		if err != nil || length < 0 || length > 2<<20 {
			return "", errors.New("invalid Redis bulk response length")
		}
		buffer := make([]byte, length+2)
		if _, err := io.ReadFull(reader, buffer); err != nil {
			return "", err
		}
		return string(buffer[:length]), nil
	default:
		return "", errors.New("unsupported Redis response")
	}
}

func parseRedisInfo(info string) map[string]float64 {
	aliases := map[string]string{"used_memory": "used_memory_bytes", "used_memory_rss": "used_memory_rss_bytes", "connected_slaves": "connected_replicas"}
	allowed := map[string]bool{"connected_clients": true, "blocked_clients": true, "used_memory": true, "used_memory_rss": true, "expired_keys": true, "evicted_keys": true, "keyspace_hits": true, "keyspace_misses": true, "connected_slaves": true, "master_last_io_seconds_ago": true, "uptime_in_seconds": true, "total_commands_processed": true, "instantaneous_ops_per_sec": true, "total_net_input_bytes": true, "total_net_output_bytes": true}
	metrics := map[string]float64{}
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "db") {
			if _, values, ok := strings.Cut(line, ":"); ok {
				for _, part := range strings.Split(values, ",") {
					if raw, ok := strings.CutPrefix(part, "keys="); ok {
						value, _ := strconv.ParseFloat(raw, 64)
						metrics["keys"] += value
					}
				}
			}
			continue
		}
		key, raw, ok := strings.Cut(line, ":")
		if !ok || !allowed[key] {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			continue
		}
		if alias := aliases[key]; alias != "" {
			key = alias
		}
		metrics[key] = value
	}
	return metrics
}

func collectApplicationDatabase(parent context.Context, target DatabaseTarget, observedAt time.Time, baseLabels map[string]string) applicationResult {
	engine := strings.ToLower(target.Engine)
	if engine == "postgresql" {
		engine = "postgres"
	}
	result := applicationResult{name: target.Name, kind: engine, required: target.Required}
	dsn := os.Getenv(target.DSNEnv)
	if dsn == "" {
		result.err = fmt.Errorf("required environment variable %s is empty", target.DSNEnv)
		return result
	}
	ctx, cancel := timeout(parent, target.TimeoutSeconds)
	defer cancel()
	driver := engine
	if driver == "postgres" {
		driver = "pgx"
	}
	database, err := sql.Open(driver, dsn)
	if err != nil {
		result.err = errors.New("open database connection failed")
		return result
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	if err := database.PingContext(ctx); err != nil {
		result.err = errors.New("database health query failed")
		return result
	}
	metrics, err := collectDatabaseMetrics(ctx, database, engine)
	if err != nil {
		result.err = err
		return result
	}
	labels := applicationLabels(baseLabels, target.Labels, target.Name, engine, target.Required)
	counters := map[string]bool{"transactions_committed": true, "transactions_rolled_back": true, "blocks_read": true, "blocks_hit": true, "rows_returned": true, "rows_fetched": true, "rows_inserted": true, "rows_updated": true, "rows_deleted": true, "deadlocks": true, "temp_bytes": true, "questions": true, "bytes_received": true, "bytes_sent": true, "slow_queries": true}
	result.points = pointsFromMetrics(engine+"_", metrics, observedAt, labels, counters)
	result.summary = map[string]any{"connections": metrics["connections"], "max_connections": metrics["max_connections"]}
	return result
}

func collectDatabaseMetrics(ctx context.Context, database *sql.DB, engine string) (map[string]float64, error) {
	metrics := map[string]float64{}
	if engine == "postgres" {
		row := database.QueryRowContext(ctx, `SELECT COALESCE(sum(numbackends),0), COALESCE(sum(xact_commit),0), COALESCE(sum(xact_rollback),0), COALESCE(sum(blks_read),0), COALESCE(sum(blks_hit),0), COALESCE(sum(tup_returned),0), COALESCE(sum(tup_fetched),0), COALESCE(sum(tup_inserted),0), COALESCE(sum(tup_updated),0), COALESCE(sum(tup_deleted),0), COALESCE(sum(deadlocks),0), COALESCE(sum(temp_bytes),0) FROM pg_stat_database`)
		values := make([]int64, 12)
		if err := row.Scan(&values[0], &values[1], &values[2], &values[3], &values[4], &values[5], &values[6], &values[7], &values[8], &values[9], &values[10], &values[11]); err != nil {
			return nil, errors.New("PostgreSQL statistics query failed")
		}
		keys := []string{"connections", "transactions_committed", "transactions_rolled_back", "blocks_read", "blocks_hit", "rows_returned", "rows_fetched", "rows_inserted", "rows_updated", "rows_deleted", "deadlocks", "temp_bytes"}
		for index, key := range keys {
			metrics[key] = float64(values[index])
		}
		var maximum string
		if err := database.QueryRowContext(ctx, "SHOW max_connections").Scan(&maximum); err == nil {
			metrics["max_connections"], _ = strconv.ParseFloat(maximum, 64)
		}
		return metrics, nil
	}
	rows, err := database.QueryContext(ctx, `SHOW GLOBAL STATUS WHERE Variable_name IN ('Threads_connected','Threads_running','Connections','Questions','Bytes_received','Bytes_sent','Slow_queries','Uptime')`)
	if err != nil {
		return nil, errors.New("MySQL statistics query failed")
	}
	defer rows.Close()
	aliases := map[string]string{"Threads_connected": "connections", "Threads_running": "threads_running", "Connections": "connections_total", "Questions": "questions", "Bytes_received": "bytes_received", "Bytes_sent": "bytes_sent", "Slow_queries": "slow_queries", "Uptime": "uptime_seconds"}
	for rows.Next() {
		var name, raw string
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, errors.New("MySQL statistics response was invalid")
		}
		metrics[aliases[name]], _ = strconv.ParseFloat(raw, 64)
	}
	var maximum float64
	if err := database.QueryRowContext(ctx, "SELECT @@max_connections").Scan(&maximum); err == nil {
		metrics["max_connections"] = maximum
	}
	return metrics, rows.Err()
}

func pointsFromMetrics(prefix string, metrics map[string]float64, observedAt time.Time, labels map[string]string, counters map[string]bool) []telemetry.Point {
	points := make([]telemetry.Point, 0, len(metrics))
	for name, value := range metrics {
		kind := telemetry.Gauge
		metricName := prefix + name
		if counters[name] || strings.HasSuffix(name, "_total") {
			kind = telemetry.Counter
			if !strings.HasSuffix(metricName, "_total") {
				metricName += "_total"
			}
		}
		points = append(points, telemetry.Point{Metric: metricName, Labels: labels, TimestampMS: observedAt.UnixMilli(), Value: value, Kind: kind})
	}
	return points
}

func applicationLabels(base, target map[string]string, name, kind string, required bool) map[string]string {
	labels := copyMetricLabels(base)
	for key, value := range target {
		labels[key] = value
	}
	labels["application"] = name
	labels["application_kind"] = kind
	labels["required"] = strconv.FormatBool(required)
	return labels
}

func environmentValue(name string) string {
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}

func timeoutDuration(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = 10
	}
	return time.Duration(seconds) * time.Second
}

func safeApplicationError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}
