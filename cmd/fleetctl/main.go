package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/agent"
	"github.com/GeneJie199/fleet-observability/internal/center"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "push":
		err = push(os.Args[2:])
	case "agent":
		err = runAgent(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "fleetctl serve [--addr 127.0.0.1:8770 --data ./fleet-data]\nfleetctl agent --center URL --node ID [--interval 30s --inventory FILE --drift FILE]\nfleetctl push --center URL --node ID --inventory FILE [--drift FILE]")
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8770", "listen address")
	data := fs.String("data", "fleet-data", "data directory")
	tokenEnv := fs.String("token-env", "FLEET_TOKEN", "bearer token environment variable")
	tlsCert := fs.String("tls-cert", "", "TLS server certificate")
	tlsKey := fs.String("tls-key", "", "TLS server private key")
	clientCA := fs.String("client-ca", "", "CA file used to require and verify client certificates")
	webhook := fs.String("webhook-url", "", "optional alert webhook URL")
	telemetryRetention := fs.Duration("telemetry-retention", 30*24*time.Hour, "native metric retention")
	telemetryMaxSeries := fs.Int("telemetry-max-series", 50000, "maximum retained metric series")
	telemetryMaxBatch := fs.Int("telemetry-max-batch", 10000, "maximum points per telemetry batch")
	telemetryMaxSamples := fs.Int("telemetry-max-samples", 5000000, "maximum retained metric samples")
	eventRetention := fs.Duration("event-retention", 30*24*time.Hour, "native event and log retention")
	eventMaxEntries := fs.Int("event-max-entries", 500000, "maximum retained events")
	eventMaxBatch := fs.Int("event-max-batch", 5000, "maximum events per batch")
	historyMaxEntries := fs.Int("history-max-entries", 2000, "maximum retained node reports per node")
	historyMaxBytes := fs.Int64("history-max-bytes", 64<<20, "maximum retained node report history bytes per node")
	protectReads := fs.Bool("protect-reads", false, "require the bearer token for read APIs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	host, _, splitErr := net.SplitHostPort(*addr)
	if splitErr != nil {
		return splitErr
	}
	ip := net.ParseIP(host)
	isLoopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	if !isLoopback && os.Getenv(*tokenEnv) == "" {
		return fmt.Errorf("FLEET_TOKEN is required for a non-loopback listener")
	}
	if !isLoopback && (*tlsCert == "" || *tlsKey == "") {
		return fmt.Errorf("--tls-cert and --tls-key are required for a non-loopback listener")
	}
	if !isLoopback {
		*protectReads = true
	}
	s, err := center.NewWithOptions(*data, os.Getenv(*tokenEnv), *webhook, center.Options{TelemetryRetention: *telemetryRetention, TelemetryMaxSeries: *telemetryMaxSeries, TelemetryMaxBatch: *telemetryMaxBatch, TelemetryMaxSamples: *telemetryMaxSamples, EventRetention: *eventRetention, EventMaxEntries: *eventMaxEntries, EventMaxBatch: *eventMaxBatch, HistoryMaxEntries: *historyMaxEntries, HistoryMaxBytes: *historyMaxBytes, ProtectReads: *protectReads})
	if err != nil {
		return err
	}
	scheme := "http"
	if *tlsCert != "" && *tlsKey != "" {
		scheme = "https"
	}
	fmt.Printf("Fleet center: %s://%s/\n", scheme, *addr)
	server := &http.Server{Addr: *addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() {
		if *clientCA != "" {
			pool, err := loadCertPool(*clientCA)
			if err != nil {
				done <- err
				return
			}
			server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
		}
		if *tlsCert != "" || *tlsKey != "" {
			if *tlsCert == "" || *tlsKey == "" {
				done <- fmt.Errorf("--tls-cert and --tls-key must be used together")
				return
			}
			done <- server.ListenAndServeTLS(*tlsCert, *tlsKey)
			return
		}
		done <- server.ListenAndServe()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	base := fs.String("center", "", "center URL")
	node := fs.String("node", "", "stable node id")
	interval := fs.Duration("interval", 30*time.Second, "report interval")
	systemInterval := fs.Duration("system-interval", 0, "system collector interval (defaults to --interval)")
	probeInterval := fs.Duration("probe-interval", 0, "probe collector interval (defaults to --interval)")
	logInterval := fs.Duration("log-interval", 0, "file log collector interval (defaults to --interval)")
	applicationInterval := fs.Duration("application-interval", 0, "application collector interval (defaults to --interval)")
	reportInterval := fs.Duration("report-interval", 5*time.Minute, "inventory and drift report interval")
	collectorTimeout := fs.Duration("collector-timeout", 15*time.Second, "timeout for each collector run")
	jitter := fs.Duration("collector-jitter", 5*time.Second, "maximum collector scheduling jitter")
	maxConcurrentCollectors := fs.Int("collector-concurrency", 4, "maximum collectors running at once")
	once := fs.Bool("once", false, "collect and report once")
	inv := fs.String("inventory", "", "InfraScout inventory JSON")
	drift := fs.String("drift", "", "InfraScout drift JSON")
	infrascout := fs.String("infrascout", "", "optional InfraScout binary to execute before each report")
	state := fs.String("state-dir", "", "InfraScout state directory")
	probes := fs.String("probes", "", "JSON file containing HTTP, TCP, TLS, and database probes")
	logs := fs.String("logs", "", "JSON file containing native file log sources")
	applications := fs.String("applications", "", "JSON file containing native Nginx, Redis, database, process, and Docker collectors")
	spoolDir := fs.String("spool-dir", "fleet-spool", "durable native telemetry queue directory")
	maxSpool := fs.Int64("max-spool-bytes", 64<<20, "maximum durable telemetry queue size")
	credentialPath := fs.String("credential-file", "", "node credential file (defaults inside spool directory)")
	reenroll := fs.Bool("reenroll", false, "rotate this node's credential using the bootstrap token")
	tokenEnv := fs.String("token-env", "FLEET_TOKEN", "bearer token environment variable")
	ca := fs.String("ca", "", "server CA certificate")
	cert := fs.String("cert", "", "mTLS client certificate")
	key := fs.String("key", "", "mTLS client private key")
	var labelFlags stringList
	fs.Var(&labelFlags, "label", "node label key=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	labels, err := agent.ParseLabels(labelFlags)
	if err != nil {
		return err
	}
	client, err := tlsClient(*ca, *cert, *key)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return agent.Run(ctx, agent.Config{CenterURL: *base, NodeID: *node, Token: os.Getenv(*tokenEnv), InventoryPath: *inv, DriftPath: *drift, InfraScoutPath: *infrascout, StateDir: *state, ProbeConfigPath: *probes, LogConfigPath: *logs, ApplicationConfigPath: *applications, AgentVersion: version, CredentialPath: *credentialPath, Labels: labels, Interval: *interval, SystemInterval: *systemInterval, ProbeInterval: *probeInterval, LogInterval: *logInterval, ApplicationInterval: *applicationInterval, ReportInterval: *reportInterval, CollectorTimeout: *collectorTimeout, Jitter: *jitter, SpoolDir: *spoolDir, MaxSpoolBytes: *maxSpool, MaxConcurrentCollectors: *maxConcurrentCollectors, Reenroll: *reenroll, Client: client}, *once)
}

func loadCertPool(path string) (*x509.CertPool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("no certificates found in %s", path)
	}
	return p, nil
}
func tlsClient(ca, cert, key string) (*http.Client, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if ca != "" {
		p, err := loadCertPool(ca)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = p
	}
	if cert != "" || key != "" {
		if cert == "" || key == "" {
			return nil, fmt.Errorf("--cert and --key must be used together")
		}
		pair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return nil, err
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg}}, nil
}

func push(args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	base := fs.String("center", "", "center URL")
	node := fs.String("node", "", "stable node id")
	invPath := fs.String("inventory", "", "InfraScout inventory.json")
	driftPath := fs.String("drift", "", "InfraScout drift.json")
	tokenEnv := fs.String("token-env", "FLEET_TOKEN", "bearer token environment variable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *base == "" || *node == "" || *invPath == "" {
		return fmt.Errorf("--center, --node and --inventory are required")
	}
	inv, err := os.ReadFile(*invPath)
	if err != nil {
		return err
	}
	report := center.Report{NodeID: *node, ObservedAt: time.Now().Format(time.RFC3339), Inventory: inv}
	if *driftPath != "" {
		report.Drift, err = os.ReadFile(*driftPath)
		if err != nil {
			return err
		}
	}
	b, _ := json.Marshal(report)
	endpoint := strings.TrimRight(*base, "/") + "/api/v1/nodes/" + url.PathEscape(*node) + "/report"
	req, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token := os.Getenv(*tokenEnv); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("center returned %s", resp.Status)
	}
	fmt.Printf("accepted node %s\n", *node)
	return nil
}
