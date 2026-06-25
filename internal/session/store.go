package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"sequential-thinking-bridge/internal/thinking"
)

// Session holds one isolated thought state and its activity timestamp.
type Session struct {
	ID       string
	State    *thinking.Server
	LastSeen time.Time
}

// Store keeps session-scoped SequentialThinkingServer instances.
type Store struct {
	mu           sync.Mutex
	ttl          time.Duration
	allowDefault bool
	sessions     map[string]*Session
}

// NewStore creates a session store with idle TTL cleanup.
func NewStore(ttl time.Duration, allowDefault bool) *Store {
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	return &Store{
		ttl:          ttl,
		allowDefault: allowDefault,
		sessions:     make(map[string]*Session),
	}
}

// NewSessionID returns a random MCP session identifier.
func NewSessionID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// Resolve gets a session or returns an error when a required session id is missing.
func (s *Store) Resolve(id string, now time.Time) (*Session, error) {
	if id == "" {
		if !s.allowDefault {
			return nil, fmt.Errorf("missing Mcp-Session-Id header")
		}
		id = "default"
	}
	return s.Get(id, now), nil
}

// Get returns an existing session or creates a new one.
func (s *Store) Get(id string, now time.Time) *Session {
	if id == "" {
		id = "default"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		session = &Session{
			ID:    id,
			State: thinking.NewServerFromEnv(),
		}
		s.sessions[id] = session
	}
	session.LastSeen = now
	return session
}

// Exists reports whether a session currently exists.
func (s *Store) Exists(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[id]
	return ok
}

// Cleanup removes sessions idle longer than the configured TTL.
func (s *Store) Cleanup(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := 0
	for id, session := range s.sessions {
		if now.Sub(session.LastSeen) > s.ttl {
			delete(s.sessions, id)
			removed++
		}
	}
	return removed
}

// Count returns the number of retained sessions.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}
