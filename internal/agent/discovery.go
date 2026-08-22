package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

type discoveredListener struct {
	Address string
	Port    int
}

type discoveredProcess struct {
	Command string
}

type discoveryResource struct {
	Type     string         `json:"type"`
	ID       string         `json:"id"`
	Host     map[string]any `json:"host,omitempty"`
	Process  map[string]any `json:"process,omitempty"`
	Endpoint map[string]any `json:"endpoint,omitempty"`
	Service  map[string]any `json:"service,omitempty"`
	Database map[string]any `json:"database,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type discoveryRelationship struct {
	Source     string   `json:"source"`
	Target     string   `json:"target"`
	Type       string   `json:"type"`
	Confidence float64  `json:"confidence"`
	Evidence   []string `json:"evidence"`
}

type discoveryRecommendation struct {
	ID             string         `json:"id"`
	Kind           string         `json:"kind"`
	Name           string         `json:"name"`
	Address        string         `json:"address,omitempty"`
	Status         string         `json:"status"`
	Reason         string         `json:"reason"`
	RequiredFields []string       `json:"required_fields,omitempty"`
	Suggested      map[string]any `json:"suggested,omitempty"`
	Evidence       []string       `json:"evidence,omitempty"`
	ObservedAt     string         `json:"observed_at"`
}

func DiscoverInventory(ctx context.Context, nodeID string) (json.RawMessage, error) {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = nodeID
	}
	listeners, listenerErr := discoverListeners(ctx)
	processes, processErr := discoverProcesses(ctx, 256)
	hostID := "native.host:" + nodeID
	resources := []discoveryResource{{Type: "host", ID: hostID, Host: map[string]any{"hostname": hostname, "os": runtime.GOOS, "arch": runtime.GOARCH}, Metadata: map[string]any{"source": "fleetscope-native"}}}
	relationships := []discoveryRelationship{}
	recommendations := []discoveryRecommendation{}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, listener := range listeners {
		address := fmt.Sprintf("%s:%d", listener.Address, listener.Port)
		endpointID := fmt.Sprintf("native.endpoint:%s:%d", nodeID, listener.Port)
		resources = append(resources, discoveryResource{Type: "endpoint", ID: endpointID, Endpoint: map[string]any{"address": address, "port": listener.Port, "exposure": listenerExposure(listener.Address)}, Metadata: map[string]any{"source": "kernel-listener"}})
		relationships = append(relationships, discoveryRelationship{Source: hostID, Target: endpointID, Type: "listens_on", Confidence: 1, Evidence: []string{"operating system TCP listener table"}})
		if recommendation := listenerRecommendation(nodeID, listener, now); recommendation != nil {
			recommendations = append(recommendations, *recommendation)
			resourceType := "service"
			resource := discoveryResource{Type: resourceType, ID: "native.service:" + nodeID + ":" + recommendation.Kind, Service: map[string]any{"name": recommendation.Name, "kind": recommendation.Kind}, Metadata: map[string]any{"source": "well-known-port"}}
			if recommendation.Kind == "postgres" || recommendation.Kind == "mysql" || recommendation.Kind == "redis" {
				resource.Type, resource.Database, resource.Service = "database", map[string]any{"name": recommendation.Name, "engine": recommendation.Kind, "address": address}, nil
				resource.ID = "native.database:" + nodeID + ":" + recommendation.Kind
			}
			resources = append(resources, resource)
			relationships = append(relationships, discoveryRelationship{Source: resource.ID, Target: endpointID, Type: "listens_on", Confidence: .8, Evidence: []string{"well-known port signature"}})
		}
	}
	seenProcessKinds := map[string]bool{}
	for _, process := range processes {
		resource, recommendation, kind, ok := recognizedProcessResource(nodeID, process.Command, now)
		if !ok || seenProcessKinds[kind] {
			continue
		}
		seenProcessKinds[kind] = true
		resources = append(resources, resource)
		relationships = append(relationships, discoveryRelationship{Source: hostID, Target: resource.ID, Type: "runs", Confidence: .9, Evidence: []string{"allowlisted process-name match"}})
		if !hasRecommendationKind(recommendations, kind) {
			recommendations = append(recommendations, recommendation)
		}
	}
	if _, err := os.Stat(defaultDockerSocket()); err == nil {
		recommendations = append(recommendations, discoveryRecommendation{ID: "native-rec-" + nodeID + "-docker", Kind: "docker", Name: "local-docker", Address: defaultDockerSocket(), Status: "ready", Reason: "发现本机 Docker Engine Socket", Suggested: map[string]any{"name": "local-docker", "socket": defaultDockerSocket(), "max_containers": 100}, Evidence: []string{"Docker Engine socket exists"}, ObservedAt: now})
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
	sort.Slice(relationships, func(i, j int) bool {
		return relationships[i].Source+relationships[i].Target < relationships[j].Source+relationships[j].Target
	})
	summary := map[string]int{"hosts": 1, "processes": 0, "endpoints": len(listeners), "services": 0, "databases": 0}
	for _, resource := range resources {
		switch resource.Type {
		case "process":
			summary["processes"]++
		case "service":
			summary["services"]++
		case "database":
			summary["databases"]++
		}
	}
	warnings := []string{}
	if listenerErr != nil {
		warnings = append(warnings, "listener discovery: "+listenerErr.Error())
	}
	if processErr != nil {
		warnings = append(warnings, "process discovery: "+processErr.Error())
	}
	document := map[string]any{"schema": "fleetscope.native-inventory/v1", "hostname": hostname, "observed_at": now, "summary": summary, "resources": resources, "relationships": relationships, "recommendations": recommendations, "warnings": warnings}
	b, err := json.Marshal(document)
	return b, err
}

func listenerExposure(address string) string {
	if address == "0.0.0.0" || address == "::" || address == "*" {
		return "all-interfaces"
	}
	if address == "127.0.0.1" || address == "::1" {
		return "loopback"
	}
	return "network"
}

func listenerRecommendation(nodeID string, listener discoveredListener, observedAt string) *discoveryRecommendation {
	kind, name, status, fields, suggested := "", "", "ready", []string{}, map[string]any{}
	switch listener.Port {
	case 5432:
		kind, name, status, fields = "postgres", "postgres-5432", "needs-credentials", []string{"dsn_env"}
		suggested = map[string]any{"name": name, "engine": "postgres", "dsn_env": "FLEET_POSTGRES_5432_DSN", "required": true}
	case 3306:
		kind, name, status, fields = "mysql", "mysql-3306", "needs-credentials", []string{"dsn_env"}
		suggested = map[string]any{"name": name, "engine": "mysql", "dsn_env": "FLEET_MYSQL_3306_DSN", "required": true}
	case 6379:
		kind, name, status, fields = "redis", "redis-6379", "needs-credentials", []string{"password_env_if_required"}
		suggested = map[string]any{"name": name, "address": fmt.Sprintf("127.0.0.1:%d", listener.Port), "password_env": "FLEET_REDIS_PASSWORD", "required": true}
	case 80, 8080:
		kind, name, status, fields = "nginx", "nginx-status", "needs-setup", []string{"stub_status_url"}
		suggested = map[string]any{"name": name, "url": fmt.Sprintf("http://127.0.0.1:%d/nginx_status", listener.Port), "required": true}
	case 443, 8443:
		kind, name = "tls", fmt.Sprintf("tls-%d", listener.Port)
		suggested = map[string]any{"name": name, "kind": "tls", "address": fmt.Sprintf("127.0.0.1:%d", listener.Port), "required": true}
	}
	if kind == "" {
		return nil
	}
	return &discoveryRecommendation{ID: fmt.Sprintf("native-rec-%s-%s-%d", nodeID, kind, listener.Port), Kind: kind, Name: name, Address: fmt.Sprintf("%s:%d", listener.Address, listener.Port), Status: status, Reason: fmt.Sprintf("发现 %d 端口正在监听", listener.Port), RequiredFields: fields, Suggested: suggested, Evidence: []string{"well-known TCP port"}, ObservedAt: observedAt}
}

func recognizableProcess(command string) (string, string) {
	lower := strings.ToLower(command)
	for _, item := range []struct{ Needle, Name, Kind string }{{"nginx", "nginx", "nginx"}, {"postgres", "postgres", "postgres"}, {"mysqld", "mysqld", "mysql"}, {"redis-server", "redis-server", "redis"}} {
		if strings.Contains(lower, item.Needle) {
			return item.Name, item.Kind
		}
	}
	return "", ""
}

func recognizedProcessResource(nodeID, command, observedAt string) (discoveryResource, discoveryRecommendation, string, bool) {
	name, kind := recognizableProcess(command)
	if kind == "" {
		return discoveryResource{}, discoveryRecommendation{}, "", false
	}
	resource := discoveryResource{
		Type:     "process",
		ID:       fmt.Sprintf("native.process:%s:%s", nodeID, kind),
		Process:  map[string]any{"name": name, "kind": kind},
		Metadata: map[string]any{"source": "process-table"},
	}
	recommendation := discoveryRecommendation{
		ID:         "native-rec-" + nodeID + "-" + kind,
		Kind:       kind,
		Name:       name,
		Status:     "ready",
		Reason:     "发现正在运行的 " + name + " 进程",
		Suggested:  map[string]any{"name": name, "match": name},
		Evidence:   []string{"process table executable match"},
		ObservedAt: observedAt,
	}
	return resource, recommendation, kind, true
}

func hasRecommendationKind(values []discoveryRecommendation, kind string) bool {
	for _, value := range values {
		if value.Kind == kind {
			return true
		}
	}
	return false
}
