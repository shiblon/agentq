package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/entroq"
)

const nsSessions = "sessions"

// SessionURI returns the doc: URI for a session.
func SessionURI(sessionID string) string {
	return "doc:sessions/" + sessionID
}

// Store wraps an entroq client with typed doc operations for sessions and configs.
type Store struct {
	eq *entroq.EntroQ
}

// New creates a Store backed by the given entroq client.
func New(eq *entroq.EntroQ) *Store {
	return &Store{eq: eq}
}

// getDocByKey fetches the first doc in ns whose key equals key exactly.
// Returns nil, nil when no such doc exists.
func (s *Store) getDocByKey(ctx context.Context, ns, key string) (*entroq.Doc, error) {
	docs, err := s.eq.Docs(ctx, &entroq.DocQuery{
		Namespace: ns,
		KeyStart:  key,
		KeyEnd:    key + "\x00", // half-open [key, key+NUL) matches only key
		Limit:     1,
	})
	if err != nil {
		return nil, fmt.Errorf("getDocByKey %s/%s: %w", ns, key, err)
	}
	if len(docs) == 0 {
		return nil, nil
	}
	return docs[0], nil
}

// PutSession creates or updates the session doc identified by session.ID.
// For concurrent-safe updates, prefer UpdateSession.
func (s *Store) PutSession(ctx context.Context, session *models.Session) error {
	existing, err := s.getDocByKey(ctx, nsSessions, session.ID)
	if err != nil {
		return fmt.Errorf("PutSession: %w", err)
	}
	var mod entroq.ModifyArg
	if existing != nil {
		mod = existing.Change(entroq.WithContent(session))
	} else {
		mod = entroq.CreatingIn(nsSessions,
			entroq.WithKeys(session.ID, ""),
			entroq.WithContent(session),
		)
	}
	if _, err := s.eq.Modify(ctx, mod); err != nil {
		return fmt.Errorf("PutSession %s: %w", session.ID, err)
	}
	return nil
}

const maxUpdateRetries = 10

// UpdateSession fetches the current session, applies fn to it, and saves it.
// On a version conflict it re-fetches and retries up to maxUpdateRetries times,
// so fn must be idempotent (pure mutations of the session value are fine).
func (s *Store) UpdateSession(ctx context.Context, sessionID string, fn func(*models.Session) error) error {
	for attempt := range maxUpdateRetries {
		session, err := s.GetSession(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("UpdateSession: %w", err)
		}
		if err := fn(session); err != nil {
			return fmt.Errorf("UpdateSession fn: %w", err)
		}
		if err := s.PutSession(ctx, session); err != nil {
			if entroq.IsDependency(err) {
				time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
				continue
			}
			return fmt.Errorf("UpdateSession: %w", err)
		}
		return nil
	}
	return fmt.Errorf("UpdateSession %s: too many version conflicts (%d attempts)", sessionID, maxUpdateRetries)
}

// GetSession retrieves a session by ID. Returns an error if the session is not found.
func (s *Store) GetSession(ctx context.Context, sessionID string) (*models.Session, error) {
	doc, err := s.getDocByKey(ctx, nsSessions, sessionID)
	if err != nil {
		return nil, fmt.Errorf("GetSession: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("GetSession: session %q not found", sessionID)
	}
	session, err := entroq.GetContent[models.Session](doc)
	if err != nil {
		return nil, fmt.Errorf("GetSession unmarshal %s: %w", sessionID, err)
	}
	return &session, nil
}

// sessionIDFromURI extracts the session ID from a "doc:sessions/{id}" URI.
func sessionIDFromURI(uri string) (string, error) {
	const prefix = "doc:sessions/"
	if !strings.HasPrefix(uri, prefix) {
		return "", fmt.Errorf("sessionIDFromURI: unexpected URI %q", uri)
	}
	return strings.TrimPrefix(uri, prefix), nil
}

// ListSessions returns up to limit sessions from the store, in key order.
// The caller can filter or sort the result as needed.
func (s *Store) ListSessions(ctx context.Context, limit int) ([]*models.Session, error) {
	docs, err := s.eq.Docs(ctx, &entroq.DocQuery{
		Namespace: nsSessions,
		Limit:     limit,
	})
	if err != nil {
		return nil, fmt.Errorf("ListSessions: %w", err)
	}
	sessions := make([]*models.Session, 0, len(docs))
	for _, doc := range docs {
		s, err := entroq.GetContent[models.Session](doc)
		if err != nil {
			continue // skip malformed docs
		}
		sessions = append(sessions, &s)
	}
	return sessions, nil
}

// GetSessionByURI retrieves a session using a doc: URI from a Task.
func (s *Store) GetSessionByURI(ctx context.Context, uri string) (*models.Session, error) {
	id, err := sessionIDFromURI(uri)
	if err != nil {
		return nil, err
	}
	return s.GetSession(ctx, id)
}
