package storage

import (
	"sync"

	"github.com/lehigh-university-libraries/scribe/internal/models"
)

type SessionStore struct {
	sessions map[string]*models.CorrectionSession
	mu       sync.RWMutex
}

func New() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*models.CorrectionSession),
	}
}

func (s *SessionStore) Get(sessionID string) (*models.CorrectionSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, exists := s.sessions[sessionID]
	return session, exists
}

func (s *SessionStore) Set(sessionID string, session *models.CorrectionSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = session
}

func (s *SessionStore) GetAll() map[string]*models.CorrectionSession {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]*models.CorrectionSession, len(s.sessions))
	for k, v := range s.sessions {
		result[k] = v
	}
	return result
}

func (s *SessionStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}
