package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type agentCredential struct {
	NodeID    string `json:"node_id"`
	Token     string `json:"token"`
	CreatedAt string `json:"created_at,omitempty"`
}

func (p *pipeline) ensureIdentity(ctx context.Context) error {
	path := p.config.CredentialPath
	if path == "" {
		path = filepath.Join(p.config.SpoolDir, "agent-credential.json")
	}
	if !p.config.Reenroll {
		credential, err := readAgentCredential(path)
		if err == nil {
			if credential.NodeID != p.config.NodeID {
				return fmt.Errorf("agent credential belongs to node %q, not %q", credential.NodeID, p.config.NodeID)
			}
			p.agentToken = credential.Token
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read agent credential: %w", err)
		}
	}

	payload, err := json.Marshal(map[string]any{"node_id": p.config.NodeID, "rotate": p.config.Reenroll})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(p.config.CenterURL, "/") + "/api/v1/agents/enroll"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if p.config.Token != "" {
		request.Header.Set("Authorization", "Bearer "+p.config.Token)
	}
	response, err := p.config.Client.Do(request)
	if err != nil {
		return fmt.Errorf("enroll agent: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("enroll agent: center returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var credential agentCredential
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credential); err != nil {
		return fmt.Errorf("decode agent credential: %w", err)
	}
	if credential.NodeID != p.config.NodeID || !strings.HasPrefix(credential.Token, "fleet_agent_") {
		return errors.New("center returned an invalid agent credential")
	}
	if err := writeAgentCredential(path, credential); err != nil {
		return fmt.Errorf("save agent credential: %w", err)
	}
	p.agentToken = credential.Token
	return nil
}

func readAgentCredential(path string) (agentCredential, error) {
	var credential agentCredential
	data, err := os.ReadFile(path)
	if err != nil {
		return credential, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credential); err != nil {
		return credential, err
	}
	if credential.NodeID == "" || credential.Token == "" {
		return credential, errors.New("credential is missing node_id or token")
	}
	return credential, nil
}

func writeAgentCredential(path string, credential agentCredential) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(credential, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".agent-credential-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(append(data, '\n'))
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	backup := ""
	if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
		backup = path + ".previous"
		_ = os.Remove(backup)
		if err := os.Rename(path, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(name, path); err != nil {
		if backup != "" {
			_ = os.Rename(backup, path)
		}
		return err
	}
	if backup != "" {
		_ = os.Remove(backup)
	}
	return nil
}
