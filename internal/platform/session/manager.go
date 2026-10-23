// Package session owns the in-memory session authorization state machine.
// A session binds exactly one device, one operator connection, a permission
// set and an expiry. No session survives a platform restart.
package session

// Session lifecycle: requested -> pending_approval -> approved -> active -> closed
// 授权状态机、权限集合、设备/操作者绑定、过期
import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"yiuk-switchboard/internal/platform/device"
)

// Status is the session lifecycle state.
type Status string

const (
	StatusRequested       Status = "requested"
	StatusPendingApproval Status = "pending_approval"
	StatusApproved        Status = "approved"
	StatusActive          Status = "active"
	StatusClosed          Status = "closed"
	StatusRejected        Status = "rejected"
	StatusExpired         Status = "expired"
	StatusInterrupted     Status = "interrupted"
)

// Session permissions are explicitly requested and approved by the device
// owner. A file download never inherits command execution permission.
const (
	PermissionExecuteCommand = "execute_command"
	PermissionReadFile       = "read_file"
)

var (
	ErrNotFound       = errors.New("session not found")
	ErrSessionLimit   = errors.New("session limit reached")
	ErrInvalidState   = errors.New("invalid session state transition")
	ErrInvalidPurpose = errors.New("purpose is required")
	ErrNoPermission   = errors.New("session does not grant the requested permission")
	ErrExpired        = errors.New("session has expired")
	ErrNotOwner       = errors.New("connection does not own the session")
)

// Session is the persisted in-memory session record.
type Session struct {
	ID                   string
	DeviceID             string
	OperatorConnectionID string
	DeviceConnectionID   string
	OperatorLabel        string
	OperatorSource       string
	Purpose              string
	Permissions          map[string]struct{}
	Mode                 device.Mode
	Status               Status
	CreatedAt            time.Time
	ExpiresAt            time.Time
	ApprovedAt           time.Time
	ClosedAt             time.Time
	CloseReason          string
	LastActivity         time.Time
}

// Clone returns a copy that callers may read without holding the manager lock.
func (s *Session) Clone() *Session {
	if s == nil {
		return nil
	}
	out := *s
	out.Permissions = make(map[string]struct{}, len(s.Permissions))
	for k := range s.Permissions {
		out.Permissions[k] = struct{}{}
	}
	return &out
}

// HasPermission reports whether the session grants a permission.
func (s *Session) HasPermission(permission string) bool {
	if s == nil {
		return false
	}
	_, ok := s.Permissions[permission]
	return ok
}

// PermissionList returns the permissions in deterministic order.
func (s *Session) PermissionList() []string {
	out := make([]string, 0, len(s.Permissions))
	for p := range s.Permissions {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// CreateRequest describes a session creation request.
type CreateRequest struct {
	DeviceID             string
	OperatorConnectionID string
	DeviceConnectionID   string
	OperatorLabel        string
	OperatorSource       string
	Purpose              string
	Permissions          []string
	Mode                 device.Mode
}

// Manager owns all session state.
type Manager struct {
	mu           sync.Mutex
	sessions     map[string]*Session
	max          int
	maxPerDevice int
	sessionTTL   time.Duration
	approvalTTL  time.Duration
	now          func() time.Time
}

// NewManager creates a session manager.
func NewManager(max, maxPerDevice int, sessionTTL, approvalTTL time.Duration) *Manager {
	return newManager(max, maxPerDevice, sessionTTL, approvalTTL, time.Now)
}

func newManager(max, maxPerDevice int, sessionTTL, approvalTTL time.Duration, now func() time.Time) *Manager {
	if max < 1 {
		max = 1
	}
	if maxPerDevice < 1 {
		maxPerDevice = 1
	}
	if sessionTTL <= 0 {
		sessionTTL = 10 * time.Minute
	}
	if approvalTTL <= 0 {
		approvalTTL = 2 * time.Minute
	}
	return &Manager{
		sessions:     make(map[string]*Session),
		max:          max,
		maxPerDevice: maxPerDevice,
		sessionTTL:   sessionTTL,
		approvalTTL:  approvalTTL,
		now:          now,
	}
}

// Create creates a direct (approved) or approval-mode (pending) session.
func (m *Manager) Create(id string, req CreateRequest) (*Session, error) {
	if req.Purpose == "" {
		return nil, ErrInvalidPurpose
	}
	perms, err := normalizePermissions(req.Permissions)
	if err != nil {
		return nil, err
	}
	mode := req.Mode
	if mode != device.ModeDirect && mode != device.ModeApproval {
		mode = device.ModeApproval
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sessions) >= m.max {
		return nil, ErrSessionLimit
	}
	activeForDevice := 0
	for _, s := range m.sessions {
		if s.DeviceID == req.DeviceID && !isTerminal(s.Status) {
			activeForDevice++
		}
	}
	if activeForDevice >= m.maxPerDevice {
		return nil, ErrSessionLimit
	}

	now := m.now()
	s := &Session{
		ID:                   id,
		DeviceID:             req.DeviceID,
		OperatorConnectionID: req.OperatorConnectionID,
		DeviceConnectionID:   req.DeviceConnectionID,
		OperatorLabel:        req.OperatorLabel,
		OperatorSource:       req.OperatorSource,
		Purpose:              req.Purpose,
		Permissions:          perms,
		Mode:                 mode,
		Status:               StatusRequested,
		CreatedAt:            now,
		LastActivity:         now,
	}
	if mode == device.ModeDirect {
		s.Status = StatusApproved
		s.ApprovedAt = now
		s.ExpiresAt = now.Add(m.sessionTTL)
	} else {
		s.Status = StatusPendingApproval
		s.ExpiresAt = now.Add(m.approvalTTL)
	}
	m.sessions[id] = s
	return s.Clone(), nil
}

// Approve transitions a pending session to approved. Only the target device
// connection may approve, and the operation is idempotent for an already
// approved session.
func (m *Manager) Approve(id, deviceConnectionID string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.locked(id)
	if err != nil {
		return nil, err
	}
	if s.DeviceConnectionID != deviceConnectionID {
		return nil, ErrNotOwner
	}
	switch s.Status {
	case StatusApproved, StatusActive:
		return s.Clone(), nil
	case StatusPendingApproval:
		now := m.now()
		if !now.Before(s.ExpiresAt) {
			s.Status = StatusExpired
			s.ClosedAt = now
			s.CloseReason = "expired"
			return nil, ErrExpired
		}
		s.Status = StatusApproved
		s.ApprovedAt = now
		s.ExpiresAt = now.Add(m.sessionTTL)
		return s.Clone(), nil
	default:
		return nil, ErrInvalidState
	}
}

// Reject transitions a pending session to rejected.
func (m *Manager) Reject(id, deviceConnectionID, reason string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.locked(id)
	if err != nil {
		return nil, err
	}
	if s.DeviceConnectionID != deviceConnectionID {
		return nil, ErrNotOwner
	}
	switch s.Status {
	case StatusRejected:
		return s.Clone(), nil
	case StatusPendingApproval:
		now := m.now()
		if !now.Before(s.ExpiresAt) {
			s.Status = StatusExpired
			s.ClosedAt = now
			s.CloseReason = "expired"
			return nil, ErrExpired
		}
		s.Status = StatusRejected
		s.ClosedAt = now
		s.CloseReason = reason
		return s.Clone(), nil
	default:
		return nil, ErrInvalidState
	}
}

// Authorize verifies that the operator connection owns an approved/active,
// unexpired session with the requested permission.
func (m *Manager) Authorize(id, operatorConnectionID, permission string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.locked(id)
	if err != nil {
		return nil, err
	}
	if s.OperatorConnectionID != operatorConnectionID {
		return nil, ErrNotOwner
	}
	if s.Status != StatusApproved && s.Status != StatusActive {
		return nil, ErrInvalidState
	}
	if !m.now().Before(s.ExpiresAt) {
		return nil, ErrExpired
	}
	if !s.HasPermission(permission) {
		return nil, ErrNoPermission
	}
	return s.Clone(), nil
}

// Activate marks an approved session active. It is idempotent.
func (m *Manager) Activate(id, operatorConnectionID string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.locked(id)
	if err != nil {
		return nil, err
	}
	if s.OperatorConnectionID != operatorConnectionID {
		return nil, ErrNotOwner
	}
	switch s.Status {
	case StatusActive:
		s.LastActivity = m.now()
		return s.Clone(), nil
	case StatusApproved:
		if !m.now().Before(s.ExpiresAt) {
			return nil, ErrExpired
		}
		s.Status = StatusActive
		s.LastActivity = m.now()
		return s.Clone(), nil
	default:
		return nil, ErrInvalidState
	}
}

// Close terminates a session. Already terminal sessions are returned
// unchanged so callers can safely retry a close.
func (m *Manager) Close(id, reason string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.locked(id)
	if err != nil {
		return nil, err
	}
	if isTerminal(s.Status) {
		return s.Clone(), nil
	}
	s.Status = StatusClosed
	s.ClosedAt = m.now()
	s.CloseReason = reason
	return s.Clone(), nil
}

// Get returns a session clone.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	return s.Clone(), true
}

// List returns clones of all sessions.
func (m *Manager) List() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.Clone())
	}
	return out
}

// CountActiveByDevice counts non-terminal sessions for a device.
func (m *Manager) CountActiveByDevice(deviceID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, s := range m.sessions {
		if s.DeviceID == deviceID && !isTerminal(s.Status) {
			count++
		}
	}
	return count
}

// InterruptByOperator interrupts every live session owned by a disconnected
// operator connection.
func (m *Manager) InterruptByOperator(connectionID, reason string) []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.interruptLocked(func(s *Session) bool {
		return s.OperatorConnectionID == connectionID && !isTerminal(s.Status)
	}, reason)
}

// InterruptByDevice interrupts every live session for a disconnected device.
func (m *Manager) InterruptByDevice(deviceID, reason string) []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.interruptLocked(func(s *Session) bool {
		return s.DeviceID == deviceID && !isTerminal(s.Status)
	}, reason)
}

// RevokeByDevice closes every live session after the device rotates or revokes
// its temporary password.
func (m *Manager) RevokeByDevice(deviceID, reason string) []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.interruptLocked(func(s *Session) bool {
		return s.DeviceID == deviceID && !isTerminal(s.Status)
	}, reason)
}

func (m *Manager) interruptLocked(match func(*Session) bool, reason string) []*Session {
	now := m.now()
	var out []*Session
	for _, s := range m.sessions {
		if !match(s) {
			continue
		}
		s.Status = StatusInterrupted
		s.ClosedAt = now
		s.CloseReason = reason
		out = append(out, s.Clone())
	}
	return out
}

// Expire transitions timed-out sessions to a terminal state and returns them.
func (m *Manager) Expire() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var out []*Session
	for _, s := range m.sessions {
		if isTerminal(s.Status) || s.ExpiresAt.IsZero() || now.Before(s.ExpiresAt) {
			continue
		}
		s.Status = StatusExpired
		s.ClosedAt = now
		s.CloseReason = "expired"
		out = append(out, s.Clone())
	}
	return out
}

func (m *Manager) locked(id string) (*Session, error) {
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

func isTerminal(status Status) bool {
	switch status {
	case StatusClosed, StatusRejected, StatusExpired, StatusInterrupted:
		return true
	default:
		return false
	}
}

func normalizePermissions(in []string) (map[string]struct{}, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("%w: at least one permission is required", ErrNoPermission)
	}
	out := make(map[string]struct{}, len(in))
	for _, p := range in {
		if p != PermissionExecuteCommand && p != PermissionReadFile {
			return nil, fmt.Errorf("%w: %q", ErrNoPermission, p)
		}
		out[p] = struct{}{}
	}
	return out, nil
}
