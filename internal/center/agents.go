package center

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type AgentCredential struct {
	NodeID     string `json:"node_id"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
}

type storedAgentCredential struct {
	AgentCredential
	TokenHash string `json:"token_hash"`
}

type enrollmentRequest struct {
	NodeID string `json:"node_id"`
	Rotate bool   `json:"rotate,omitempty"`
}

type enrollmentResponse struct {
	AgentCredential
	Token string `json:"token"`
}

type agentAuth struct {
	Admin  bool
	NodeID string
}

type agentAuthKey struct{}

func (s *Server) enrollAgent(w http.ResponseWriter, r *http.Request) {
	var request enrollmentRequest
	if decodeBody(w, r, &request) != nil {
		return
	}
	request.NodeID = strings.TrimSpace(request.NodeID)
	if !validID.MatchString(request.NodeID) {
		writeError(w, http.StatusBadRequest, "invalid node id")
		return
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		writeError(w, http.StatusInternalServerError, "generate agent credential: "+err.Error())
		return
	}
	token := "fleet_agent_" + base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	now := time.Now().UTC().Format(time.RFC3339Nano)

	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.readAgentCredentials()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, record := range records {
		if record.NodeID == request.NodeID && record.RevokedAt == "" && !request.Rotate {
			writeError(w, http.StatusConflict, "node already enrolled; explicitly rotate its credential")
			return
		}
	}
	record := storedAgentCredential{AgentCredential: AgentCredential{NodeID: request.NodeID, CreatedAt: now}, TokenHash: hex.EncodeToString(hash[:])}
	updated := make([]storedAgentCredential, 0, len(records)+1)
	for _, current := range records {
		if current.NodeID != request.NodeID {
			updated = append(updated, current)
		}
	}
	updated = append(updated, record)
	if err := s.writeAgentCredentials(updated); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, enrollmentResponse{AgentCredential: record.AgentCredential, Token: token})
}

func (s *Server) listAgents(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	records, err := s.readAgentCredentials()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	agents := make([]AgentCredential, 0, len(records))
	for _, record := range records {
		agents = append(agents, record.AgentCredential)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].NodeID < agents[j].NodeID })
	writeJSON(w, agents)
}

func (s *Server) revokeAgent(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("id")
	if !validID.MatchString(nodeID) {
		writeError(w, http.StatusBadRequest, "invalid node id")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.readAgentCredentials()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	found := false
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for index := range records {
		if records[index].NodeID == nodeID && records[index].RevokedAt == "" {
			records[index].RevokedAt = now
			found = true
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "active agent credential not found")
		return
	}
	if err := s.writeAgentCredentials(records); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) authorizeAgent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			next(w, r.WithContext(context.WithValue(r.Context(), agentAuthKey{}, agentAuth{Admin: true})))
			return
		}
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		token := strings.TrimPrefix(header, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) == 1 {
			next(w, r.WithContext(context.WithValue(r.Context(), agentAuthKey{}, agentAuth{Admin: true})))
			return
		}
		nodeID, ok, err := s.authenticateAgentToken(token)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		s.touchAgent(nodeID)
		next(w, r.WithContext(context.WithValue(r.Context(), agentAuthKey{}, agentAuth{NodeID: nodeID})))
	}
}

func (s *Server) requireNodeIdentity(w http.ResponseWriter, r *http.Request, nodeID string) bool {
	auth, _ := r.Context().Value(agentAuthKey{}).(agentAuth)
	if auth.Admin || auth.NodeID == nodeID {
		return true
	}
	writeError(w, http.StatusForbidden, "agent credential is not valid for this node")
	return false
}

func (s *Server) authenticateAgentToken(token string) (string, bool, error) {
	hash := sha256.Sum256([]byte(token))
	want := hex.EncodeToString(hash[:])
	s.mu.RLock()
	defer s.mu.RUnlock()
	records, err := s.readAgentCredentials()
	if err != nil {
		return "", false, err
	}
	for _, record := range records {
		if record.RevokedAt == "" && subtle.ConstantTimeCompare([]byte(record.TokenHash), []byte(want)) == 1 {
			return record.NodeID, true, nil
		}
	}
	return "", false, nil
}

func (s *Server) touchAgent(nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.readAgentCredentials()
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for index := range records {
		if records[index].NodeID != nodeID || records[index].RevokedAt != "" {
			continue
		}
		lastSeen, _ := time.Parse(time.RFC3339Nano, records[index].LastSeenAt)
		if !lastSeen.IsZero() && now.Sub(lastSeen) < time.Minute {
			return
		}
		records[index].LastSeenAt = now.Format(time.RFC3339Nano)
		_ = s.writeAgentCredentials(records)
		return
	}
}

func (s *Server) readAgentCredentials() ([]storedAgentCredential, error) {
	var records []storedAgentCredential
	err := readJSONFile(filepath.Join(s.dir, "agents.json"), &records)
	if errors.Is(err, os.ErrNotExist) {
		return []storedAgentCredential{}, nil
	}
	return records, err
}

func (s *Server) writeAgentCredentials(records []storedAgentCredential) error {
	return writeJSONFile(filepath.Join(s.dir, "agents.json"), records)
}
