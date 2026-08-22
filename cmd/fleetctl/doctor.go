package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type doctorResults struct {
	passed, warnings, failed int
}

func (r *doctorResults) report(level, name, detail string) {
	switch level {
	case "OK":
		r.passed++
	case "WARN":
		r.warnings++
	case "FAIL":
		r.failed++
	}
	fmt.Printf("%-4s %-18s %s\n", level, name, detail)
}

func doctor(args []string) error {
	defaultData, err := defaultUpDataDir()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	baseURL := fs.String("url", "http://127.0.0.1:8770", "FleetScope center URL")
	data := fs.String("data", defaultData, "persistent data directory")
	tokenFile := fs.String("token-file", "", "management token file (defaults to DATA/admin-token)")
	offline := fs.Bool("offline", false, "skip center HTTP checks")
	requireNode := fs.Bool("require-node", false, "fail unless at least one node has reported")
	if err = fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	dataPath, err := filepath.Abs(*data)
	if err != nil {
		return err
	}
	if *tokenFile == "" {
		*tokenFile = filepath.Join(dataPath, "admin-token")
	}
	results := &doctorResults{}
	fmt.Printf("FleetScope doctor %s (%s)\n", version, dataPath)
	checkDoctorData(results, dataPath)
	token := checkDoctorToken(results, *tokenFile)
	if *offline {
		results.report("WARN", "center", "HTTP checks skipped by --offline")
	} else {
		checkDoctorCenter(results, strings.TrimRight(*baseURL, "/"), token, *requireNode)
	}
	if path, lookupErr := resolveInfraScout("auto"); lookupErr != nil {
		results.report("WARN", "InfraScout", lookupErr.Error())
	} else if path == "" {
		results.report("OK", "native discovery", "enabled; InfraScout is optional")
	} else {
		results.report("OK", "InfraScout", path)
	}
	fmt.Printf("Summary: %d passed, %d warning, %d failed\n", results.passed, results.warnings, results.failed)
	if results.failed > 0 {
		return fmt.Errorf("doctor found %d blocking check(s)", results.failed)
	}
	return nil
}

func checkDoctorData(results *doctorResults, path string) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		results.report("WARN", "data directory", "not created yet; fleetctl up will create it")
		return
	}
	if err != nil {
		results.report("FAIL", "data directory", err.Error())
		return
	}
	if !info.IsDir() {
		results.report("FAIL", "data directory", "path is not a directory")
		return
	}
	testFile, err := os.CreateTemp(path, ".doctor-write-*.tmp")
	if err != nil {
		results.report("FAIL", "data directory", "not writable: "+err.Error())
		return
	}
	name := testFile.Name()
	if closeErr := testFile.Close(); closeErr != nil {
		results.report("FAIL", "data directory", "write check failed: "+closeErr.Error())
		_ = os.Remove(name)
		return
	}
	_ = os.Remove(name)
	results.report("OK", "data directory", "exists and is writable")
}

func checkDoctorToken(results *doctorResults, path string) string {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		results.report("WARN", "admin token", "not found; fleetctl up will generate it")
		return ""
	}
	if err != nil {
		results.report("FAIL", "admin token", "cannot read token file")
		return ""
	}
	token := strings.TrimSpace(string(data))
	if strings.Contains(token, "\n") || strings.HasPrefix(token, "FLEET_TOKEN=") {
		token = ""
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "FLEET_TOKEN=") {
				token = strings.TrimSpace(strings.TrimPrefix(line, "FLEET_TOKEN="))
				break
			}
		}
	}
	if !strings.HasPrefix(token, "fleet_admin_") || len(token) < 32 {
		results.report("FAIL", "admin token", "token file is invalid")
		return ""
	}
	results.report("OK", "admin token", "valid token file found")
	return token
}

func checkDoctorCenter(results *doctorResults, baseURL, token string, requireNode bool) {
	client := &http.Client{Timeout: 4 * time.Second}
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/health", nil)
	if err != nil {
		results.report("FAIL", "center health", err.Error())
		return
	}
	response, err := client.Do(request)
	if err != nil {
		results.report("FAIL", "center health", err.Error())
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		results.report("FAIL", "center health", response.Status)
		return
	}
	results.report("OK", "center health", baseURL+" is reachable")
	request, _ = http.NewRequest(http.MethodGet, baseURL+"/api/v1/overview", nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err = client.Do(request)
	if err != nil {
		results.report("FAIL", "center API", err.Error())
		return
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		results.report("FAIL", "center API", "management token is missing or does not match the center")
		return
	}
	if response.StatusCode != http.StatusOK {
		results.report("FAIL", "center API", response.Status)
		return
	}
	var overview struct {
		TotalNodes int `json:"total_nodes"`
	}
	if err = json.Unmarshal(body, &overview); err != nil {
		results.report("FAIL", "center API", "overview response is invalid JSON")
		return
	}
	if requireNode && overview.TotalNodes < 1 {
		results.report("FAIL", "agent report", "no node has reported to the center")
		return
	}
	results.report("OK", "center API", fmt.Sprintf("overview is readable; %d node(s) reported", overview.TotalNodes))
}
