package agent

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type ProbeConfig struct {
	HTTP      []HTTPProbe     `json:"http"`
	TCP       []TCPProbe      `json:"tcp"`
	TLS       []TLSProbe      `json:"tls"`
	Databases []DatabaseProbe `json:"databases"`
}
type HTTPProbe struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Contains       string `json:"contains,omitempty"`
	WantStatus     int    `json:"want_status,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Required       bool   `json:"required,omitempty"`
}
type TCPProbe struct {
	Name           string `json:"name"`
	Address        string `json:"address"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Required       bool   `json:"required,omitempty"`
}
type TLSProbe struct {
	Name           string `json:"name"`
	Address        string `json:"address"`
	ServerName     string `json:"server_name"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	WarningDays    int    `json:"warning_days,omitempty"`
	CriticalDays   int    `json:"critical_days,omitempty"`
	Required       bool   `json:"required,omitempty"`
}
type DatabaseProbe struct {
	Name           string `json:"name"`
	Engine         string `json:"engine"`
	DSNEnv         string `json:"dsn_env"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Required       bool   `json:"required,omitempty"`
}
type ProbeResult struct {
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Status    string         `json:"status"`
	Required  bool           `json:"required"`
	LatencyMS int64          `json:"latency_ms"`
	Detail    string         `json:"detail,omitempty"`
	Values    map[string]any `json:"values,omitempty"`
}

func RunProbes(parent context.Context, path string) ([]ProbeResult, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c ProbeConfig
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return nil, err
	}
	if err = d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("probe config must contain one JSON object")
		}
		return nil, fmt.Errorf("invalid trailing probe config: %w", err)
	}
	out := []ProbeResult{}
	for _, p := range c.HTTP {
		out = append(out, runHTTP(parent, p))
	}
	for _, p := range c.TCP {
		out = append(out, runTCP(parent, p))
	}
	for _, p := range c.TLS {
		out = append(out, runTLS(parent, p))
	}
	for _, p := range c.Databases {
		out = append(out, runDatabase(parent, p))
	}
	return out, nil
}
func timeout(parent context.Context, seconds int) (context.Context, context.CancelFunc) {
	if seconds <= 0 {
		seconds = 10
	}
	return context.WithTimeout(parent, time.Duration(seconds)*time.Second)
}
func runHTTP(parent context.Context, p HTTPProbe) ProbeResult {
	start := time.Now()
	r := ProbeResult{Name: p.Name, Kind: "http", Status: "error", Required: p.Required}
	ctx, cancel := timeout(parent, p.TimeoutSeconds)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		r.Detail = err.Error()
		return r
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.Detail = err.Error()
		r.LatencyMS = time.Since(start).Milliseconds()
		return r
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	want := p.WantStatus
	if want == 0 {
		want = 200
	}
	r.Values = map[string]any{"status_code": resp.StatusCode, "bytes": len(body)}
	if resp.StatusCode != want {
		r.Detail = fmt.Sprintf("HTTP %d, want %d", resp.StatusCode, want)
	} else if p.Contains != "" && !strings.Contains(string(body), p.Contains) {
		r.Detail = "expected response text not found"
	} else {
		r.Status = "ok"
	}
	r.LatencyMS = time.Since(start).Milliseconds()
	return r
}
func runTCP(parent context.Context, p TCPProbe) ProbeResult {
	start := time.Now()
	r := ProbeResult{Name: p.Name, Kind: "tcp", Status: "error", Required: p.Required}
	ctx, cancel := timeout(parent, p.TimeoutSeconds)
	defer cancel()
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", p.Address)
	if err != nil {
		r.Detail = err.Error()
	} else {
		r.Status = "ok"
		conn.Close()
	}
	r.LatencyMS = time.Since(start).Milliseconds()
	return r
}
func runTLS(parent context.Context, p TLSProbe) ProbeResult {
	start := time.Now()
	r := ProbeResult{Name: p.Name, Kind: "tls", Status: "error", Required: p.Required}
	ctx, cancel := timeout(parent, p.TimeoutSeconds)
	defer cancel()
	d := tls.Dialer{NetDialer: &net.Dialer{}, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.ServerName}}
	conn, err := d.DialContext(ctx, "tcp", p.Address)
	if err != nil {
		r.Detail = err.Error()
		r.LatencyMS = time.Since(start).Milliseconds()
		return r
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		r.Detail = "peer returned no certificate"
		return r
	}
	cert := certs[0]
	days := int(time.Until(cert.NotAfter).Hours() / 24)
	r.Values = map[string]any{"expires_at": cert.NotAfter.UTC().Format(time.RFC3339), "days_remaining": days, "issuer": cert.Issuer.CommonName}
	r.Status = "ok"
	critical := p.CriticalDays
	if critical == 0 {
		critical = 7
	}
	warning := p.WarningDays
	if warning == 0 {
		warning = 30
	}
	if days <= critical {
		r.Status = "critical"
		r.Detail = "certificate expires critically soon"
	} else if days <= warning {
		r.Status = "warning"
		r.Detail = "certificate expires soon"
	}
	r.LatencyMS = time.Since(start).Milliseconds()
	return r
}
func runDatabase(parent context.Context, p DatabaseProbe) ProbeResult {
	start := time.Now()
	r := ProbeResult{Name: p.Name, Kind: "database", Status: "error", Required: p.Required, Values: map[string]any{"engine": p.Engine}}
	driver := p.Engine
	if driver == "postgres" || driver == "postgresql" {
		driver = "pgx"
	}
	if driver != "pgx" && driver != "mysql" {
		r.Detail = "engine must be postgres or mysql"
		return r
	}
	dsn := os.Getenv(p.DSNEnv)
	if dsn == "" {
		r.Detail = fmt.Sprintf("environment variable %s is empty", p.DSNEnv)
		return r
	}
	ctx, cancel := timeout(parent, p.TimeoutSeconds)
	defer cancel()
	db, err := sql.Open(driver, dsn)
	if err != nil {
		r.Detail = err.Error()
		return r
	}
	defer db.Close()
	if err = db.PingContext(ctx); err != nil {
		r.Detail = err.Error()
		r.LatencyMS = time.Since(start).Milliseconds()
		return r
	}
	var version string
	q := "SELECT version()"
	if driver == "mysql" {
		q = "SELECT @@version"
	}
	if err = db.QueryRowContext(ctx, q).Scan(&version); err != nil && !errors.Is(err, sql.ErrNoRows) {
		r.Detail = err.Error()
		return r
	}
	r.Values["version"] = version
	var connections int
	if driver == "pgx" {
		_ = db.QueryRowContext(ctx, "SELECT count(*) FROM pg_stat_activity").Scan(&connections)
	} else {
		var name string
		_ = db.QueryRowContext(ctx, "SHOW STATUS LIKE 'Threads_connected'").Scan(&name, &connections)
	}
	r.Values["connections"] = connections
	r.Status = "ok"
	r.LatencyMS = time.Since(start).Milliseconds()
	return r
}
