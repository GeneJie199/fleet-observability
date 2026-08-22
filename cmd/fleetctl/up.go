package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/agent"
	"github.com/GeneJie199/fleet-observability/internal/center"
)

var unsafeNodeID = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func up(args []string) error {
	defaultData, err := defaultUpDataDir()
	if err != nil {
		return err
	}
	defaultNode, err := defaultNodeID()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8770", "loopback listen address")
	data := fs.String("data", defaultData, "persistent data directory")
	node := fs.String("node", defaultNode, "stable local node id")
	open := fs.Bool("open", true, "open the console in the default browser")
	infrascout := fs.String("infrascout", "auto", "InfraScout binary path, auto, or off")
	probes := fs.String("probes", "", "JSON file containing HTTP, TCP, TLS, and database probes")
	logs := fs.String("logs", "", "JSON file containing native file log sources")
	applications := fs.String("applications", "", "JSON file containing application collectors")
	notificationsPath := fs.String("notifications", "", "notification channel and delivery policy JSON")
	aiProviderPath := fs.String("ai-provider", "", "optional evidence explanation provider JSON")
	var labelFlags stringList
	fs.Var(&labelFlags, "label", "node label key=value (repeatable)")
	if err = fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err = validateLoopbackAddress(*addr); err != nil {
		return err
	}
	if !validLocalNodeID(*node) {
		return fmt.Errorf("--node must start with a letter or number and contain only letters, numbers, dot, underscore, or dash")
	}
	labels, err := agent.ParseLabels(labelFlags)
	if err != nil {
		return err
	}
	dataPath, err := filepath.Abs(*data)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dataPath, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	adminToken, err := loadOrCreateAdminToken(filepath.Join(dataPath, "admin-token"))
	if err != nil {
		return err
	}
	var notificationConfig *center.NotificationConfig
	if *notificationsPath != "" {
		loaded, loadErr := center.LoadNotificationConfig(*notificationsPath)
		if loadErr != nil {
			return loadErr
		}
		notificationConfig = &loaded
	}
	var aiConfig *center.AIProviderConfig
	if *aiProviderPath != "" {
		loaded, loadErr := center.LoadAIProviderConfig(*aiProviderPath)
		if loadErr != nil {
			return loadErr
		}
		aiConfig = &loaded
	}
	service, err := center.NewWithOptions(dataPath, adminToken, "", center.Options{NotificationConfig: notificationConfig, AIConfig: aiConfig})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *addr, err)
	}
	defer listener.Close()
	actualAddress := listener.Addr().String()
	pageURL := "http://" + actualAddress + "/"
	server := &http.Server{Handler: service.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErrors := make(chan error, 1)
	go func() {
		serveErr := server.Serve(listener)
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		serverErrors <- serveErr
	}()
	go service.RunNotificationWorker(ctx)

	resolvedInfraScout, err := resolveInfraScout(*infrascout)
	if err != nil {
		return shutdownUpServer(server, err)
	}
	spoolDir := filepath.Join(dataPath, "agent")
	credentialPath := filepath.Join(spoolDir, "agent-credential.json")
	_, credentialErr := os.Stat(credentialPath)
	config := agent.Config{
		CenterURL:               pageURL,
		NodeID:                  *node,
		Token:                   adminToken,
		InfraScoutPath:          resolvedInfraScout,
		StateDir:                filepath.Join(dataPath, "infrascout"),
		ProbeConfigPath:         *probes,
		LogConfigPath:           *logs,
		ApplicationConfigPath:   *applications,
		AgentVersion:            version,
		CredentialPath:          credentialPath,
		Labels:                  labels,
		Interval:                10 * time.Second,
		SystemInterval:          10 * time.Second,
		ProbeInterval:           30 * time.Second,
		LogInterval:             5 * time.Second,
		ApplicationInterval:     30 * time.Second,
		ReportInterval:          30 * time.Second,
		CollectorTimeout:        15 * time.Second,
		Jitter:                  2 * time.Second,
		SpoolDir:                spoolDir,
		MaxSpoolBytes:           64 << 20,
		MaxConcurrentCollectors: 4,
		Reenroll:                errors.Is(credentialErr, os.ErrNotExist),
		Client:                  &http.Client{Timeout: 20 * time.Second},
	}
	agentErrors := make(chan error, 1)
	go func() { agentErrors <- agent.Run(ctx, config, false) }()

	launchURL := pageURL + "#" + url.Values{"token": []string{adminToken}}.Encode()
	fmt.Printf("FleetScope is ready: %s\n", pageURL)
	fmt.Printf("Data directory: %s\n", dataPath)
	if resolvedInfraScout == "" {
		fmt.Println("Discovery: FleetScope native collector")
	} else {
		fmt.Printf("Discovery: FleetScope native collector + InfraScout (%s)\n", resolvedInfraScout)
	}
	if *open {
		if err = openBrowser(launchURL); err != nil {
			fmt.Fprintf(os.Stderr, "open browser: %v\n", err)
			fmt.Println("Open the ready URL and use the admin token stored in the data directory.")
		}
	}

	select {
	case <-ctx.Done():
		err = nil
	case err = <-serverErrors:
		if err != nil {
			err = fmt.Errorf("center stopped: %w", err)
		}
	case agentErr := <-agentErrors:
		if agentErr != nil {
			err = fmt.Errorf("local agent stopped: %w", agentErr)
		}
	}
	stop()
	return shutdownUpServer(server, err)
}

func defaultUpDataDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(dir, "FleetScope"), nil
}

func defaultNodeID() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("read hostname: %w", err)
	}
	value := strings.Trim(unsafeNodeID.ReplaceAllString(hostname, "-"), "-._")
	if value == "" {
		value = "local-node"
	}
	if len(value) > 128 {
		value = value[:128]
	}
	return value, nil
}

func validLocalNodeID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	first := value[0]
	if !((first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return false
	}
	return unsafeNodeID.FindString(value) == ""
}

func validateLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid --addr: %w", err)
	}
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return errors.New("fleetctl up only listens on a loopback address; use fleetctl serve with TLS for remote access")
	}
	return nil
}

func loadOrCreateAdminToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		token := strings.TrimSpace(string(data))
		if !strings.HasPrefix(token, "fleet_admin_") || len(token) < 32 {
			return "", fmt.Errorf("admin token file is invalid: %s", path)
		}
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read admin token: %w", err)
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate admin token: %w", err)
	}
	token := "fleet_admin_" + base64.RawURLEncoding.EncodeToString(secret)
	if err = os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write admin token: %w", err)
	}
	return token, nil
}

func resolveInfraScout(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "off", "none":
		return "", nil
	case "auto":
		path, err := exec.LookPath("infrascout")
		if err != nil {
			return "", nil
		}
		return path, nil
	default:
		info, err := os.Stat(value)
		if err != nil {
			return "", fmt.Errorf("locate InfraScout: %w", err)
		}
		if info.IsDir() {
			return "", errors.New("InfraScout path points to a directory")
		}
		return value, nil
	}
}

func openBrowser(pageURL string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", pageURL)
	case "darwin":
		command = exec.Command("open", pageURL)
	default:
		command = exec.Command("xdg-open", pageURL)
	}
	return command.Start()
}

func shutdownUpServer(server *http.Server, result error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); result == nil && err != nil {
		return err
	}
	return result
}
