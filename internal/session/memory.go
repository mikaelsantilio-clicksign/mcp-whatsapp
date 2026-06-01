package session

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is an in-memory implementation of Store for development and
// the hackathon MVP. Pending records are expired lazily on read.
type MemoryStore struct {
	mu sync.RWMutex

	sessions      map[string]*Session // key: phone_number
	pendingState  map[string]*Pending // key: state
	pendingLink   map[string]*Pending // key: link_token
	clientReg     *ClientRegistration
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions:     make(map[string]*Session),
		pendingState: make(map[string]*Pending),
		pendingLink:  make(map[string]*Pending),
	}
}

func (m *MemoryStore) GetSession(_ context.Context, phone string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[phone]
	if !ok {
		return nil, ErrNotFound
	}
	cp := copySession(s)
	return cp, nil
}

func (m *MemoryStore) PutSession(_ context.Context, s *Session) error {
	if s == nil || s.PhoneNumber == "" {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := copySession(s)
	if cp.UpdatedAt.IsZero() {
		cp.UpdatedAt = time.Now().UTC()
	}
	m.sessions[s.PhoneNumber] = cp
	return nil
}

// copySession deep-copies a Session including the History slice and the
// nested ToolCalls slices so callers can mutate the returned value without
// affecting the stored record.
func copySession(s *Session) *Session {
	if s == nil {
		return nil
	}
	cp := *s
	if len(s.History) > 0 {
		cp.History = make([]ChatTurn, len(s.History))
		for i, t := range s.History {
			cp.History[i] = copyChatTurn(t)
		}
	} else {
		cp.History = nil
	}
	return &cp
}

func copyChatTurn(t ChatTurn) ChatTurn {
	out := t
	if len(t.ToolCalls) > 0 {
		out.ToolCalls = make([]ChatToolCall, len(t.ToolCalls))
		copy(out.ToolCalls, t.ToolCalls)
	} else {
		out.ToolCalls = nil
	}
	return out
}

func (m *MemoryStore) DeleteSession(_ context.Context, phone string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, phone)
	return nil
}

func (m *MemoryStore) PutPending(_ context.Context, p *Pending) error {
	if p == nil || p.State == "" {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *p
	m.pendingState[p.State] = &cp
	if p.LinkToken != "" {
		m.pendingLink[p.LinkToken] = &cp
	}
	return nil
}

func (m *MemoryStore) GetPendingByState(_ context.Context, state string) (*Pending, error) {
	m.mu.RLock()
	p, ok := m.pendingState[state]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	if time.Now().After(p.ExpiresAt) {
		_ = m.deletePendingLocked(p)
		return nil, ErrExpired
	}
	cp := *p
	return &cp, nil
}

func (m *MemoryStore) GetPendingByLinkToken(_ context.Context, token string) (*Pending, error) {
	m.mu.RLock()
	p, ok := m.pendingLink[token]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	if time.Now().After(p.ExpiresAt) {
		_ = m.deletePendingLocked(p)
		return nil, ErrExpired
	}
	cp := *p
	return &cp, nil
}

func (m *MemoryStore) DeletePending(_ context.Context, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pendingState[state]; ok {
		delete(m.pendingState, state)
		if p.LinkToken != "" {
			delete(m.pendingLink, p.LinkToken)
		}
	}
	return nil
}

func (m *MemoryStore) deletePendingLocked(p *Pending) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pendingState, p.State)
	if p.LinkToken != "" {
		delete(m.pendingLink, p.LinkToken)
	}
	return nil
}

func (m *MemoryStore) GetClientRegistration(_ context.Context) (*ClientRegistration, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.clientReg == nil {
		return nil, ErrNotFound
	}
	cp := *m.clientReg
	return &cp, nil
}

func (m *MemoryStore) PutClientRegistration(_ context.Context, r *ClientRegistration) error {
	if r == nil {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *r
	m.clientReg = &cp
	return nil
}
