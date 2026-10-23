// Package device owns in-memory device registration, temporary passwords and
// online state. Nothing is persisted: restarting the platform invalidates every
// device password, connection and session.
package device

//临时密码哈希（盐 + SHA-256）、版本、TTL、撤销、在线连接、心跳、并发会话计数
import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"strings"
	"sync"
	"time"
)

// Mode controls how a verified temporary password may create a session.
type Mode string

const (
	ModeDirect   Mode = "direct"
	ModeApproval Mode = "approval"
)

var (
	ErrDeviceLimit    = errors.New("device limit reached")
	ErrDeviceNotFound = errors.New("device not found")
	ErrDeviceOnline   = errors.New("device is already online")
	ErrBadPassword    = errors.New("temporary password is incorrect")
	ErrPasswordExpiry = errors.New("temporary password has expired")
	ErrPasswordRevoke = errors.New("temporary password has been revoked")
	ErrStaleVersion   = errors.New("temporary password version is stale")
	ErrInvalidMode    = errors.New("invalid authorization mode")
)

// Info is the non-secret device view.
type Info struct {
	ID                string
	Name              string
	Platform          string
	Mode              Mode
	Online            bool
	PasswordVersion   int
	PasswordExpiresAt time.Time
	LastHeartbeat     time.Time
	ActiveSessions    int
	CreatedAt         time.Time
}

// Registration describes a device registration request.
type Registration struct {
	ID              string
	Name            string
	Platform        string
	TemporaryPass   string
	PasswordVersion int
	Mode            Mode
	TTL             time.Duration
}

type device struct {
	info         Info
	passwordSalt []byte
	passwordHash []byte
	revoked      bool
	onlineConnID string
	sessions     map[string]struct{}
}

// Manager owns all device state.
type Manager struct {
	mu      sync.Mutex
	devices map[string]*device
	max     int
	ttl     time.Duration
	now     func() time.Time
}

// NewManager creates a device manager.
func NewManager(max int, ttl time.Duration) *Manager {
	return newManager(max, ttl, time.Now)
}

func newManager(max int, ttl time.Duration, now func() time.Time) *Manager {
	if max < 1 {
		max = 1
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &Manager{devices: make(map[string]*device), max: max, ttl: ttl, now: now}
}

// GeneratePassword returns a 32-character high-entropy temporary password.
// The alphabet is base32 so it is easy to read over a voice channel while
// retaining 160 bits of entropy.
func GeneratePassword() (string, error) {
	var raw [20]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]), nil
}

// Register creates or refreshes a device. If TemporaryPass is empty the
// manager generates one and returns it in the second result so the caller can
// hand it to the device over TLS. A device that is already online cannot be
// registered by another connection.
func (m *Manager) Register(reg Registration) (Info, string, error) {
	reg.ID = strings.TrimSpace(reg.ID)
	if reg.ID == "" {
		return Info{}, "", errors.New("device id is required")
	}
	if reg.Mode == "" {
		reg.Mode = ModeApproval
	}
	if reg.Mode != ModeDirect && reg.Mode != ModeApproval {
		return Info{}, "", ErrInvalidMode
	}
	now := m.now()
	ttl := reg.TTL
	if ttl <= 0 {
		ttl = m.ttl
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	existing := m.devices[reg.ID]
	expiredPassword := false
	if existing == nil && len(m.devices) >= m.max {
		return Info{}, "", ErrDeviceLimit
	}
	if existing != nil && existing.onlineConnID != "" {
		return Info{}, "", ErrDeviceOnline
	}

	generated := ""
	password := reg.TemporaryPass
	if existing != nil {
		// Re-registration is a reconnect operation, not a way to claim an
		// offline device by knowing only its public ID. The current temporary
		// password is the device's proof of identity.
		if existing.revoked {
			return Info{}, "", ErrPasswordRevoke
		}
		candidate := hashPassword(existing.passwordSalt, password)
		if subtle.ConstantTimeCompare(candidate, existing.passwordHash) != 1 {
			return Info{}, "", ErrBadPassword
		}
		expiredPassword = !existing.info.PasswordExpiresAt.IsZero() &&
			!now.Before(existing.info.PasswordExpiresAt)
	}
	if password == "" || expiredPassword {
		var err error
		password, err = GeneratePassword()
		if err != nil {
			return Info{}, "", err
		}
		generated = password
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return Info{}, "", err
	}
	hash := hashPassword(salt, password)
	version := reg.PasswordVersion
	if version <= 0 {
		version = 1
	}
	if existing != nil && version <= existing.info.PasswordVersion {
		version = existing.info.PasswordVersion + 1
	}

	if existing == nil {
		existing = &device{sessions: make(map[string]struct{})}
		m.devices[reg.ID] = existing
	}
	existing.info.ID = reg.ID
	existing.info.Name = reg.Name
	existing.info.Platform = reg.Platform
	existing.info.Mode = reg.Mode
	existing.info.PasswordVersion = version
	existing.info.PasswordExpiresAt = now.Add(ttl)
	existing.info.LastHeartbeat = now
	existing.info.Online = false
	existing.info.CreatedAt = now
	existing.passwordSalt = salt
	existing.passwordHash = hash
	existing.revoked = false
	existing.onlineConnID = ""

	return existing.snapshot(now), generated, nil
}

// UpdatePassword replaces the temporary password and mode. A stale version is
// rejected so an old client cannot roll back a newer password.
func (m *Manager) UpdatePassword(deviceID, password string, version int, mode Mode, ttl time.Duration) (Info, string, error) {
	deviceID = strings.TrimSpace(deviceID)
	if mode == "" {
		mode = ModeApproval
	}
	if mode != ModeDirect && mode != ModeApproval {
		return Info{}, "", ErrInvalidMode
	}
	now := m.now()
	if ttl <= 0 {
		ttl = m.ttl
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.devices[deviceID]
	if d == nil {
		return Info{}, "", ErrDeviceNotFound
	}
	if version > 0 && version <= d.info.PasswordVersion {
		return Info{}, "", ErrStaleVersion
	}
	generated := ""
	if password == "" {
		var err error
		password, err = GeneratePassword()
		if err != nil {
			return Info{}, "", err
		}
		generated = password
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return Info{}, "", err
	}
	d.passwordSalt = salt
	d.passwordHash = hashPassword(salt, password)
	d.revoked = false
	d.info.Mode = mode
	if version > 0 {
		d.info.PasswordVersion = version
	} else {
		d.info.PasswordVersion = d.info.PasswordVersion + 1
	}
	d.info.PasswordExpiresAt = now.Add(ttl)
	return d.snapshot(now), generated, nil
}

// VerifyPassword checks the temporary password for a device. It never returns
// the password or hash to callers.
func (m *Manager) VerifyPassword(deviceID, password string) (Info, error) {
	deviceID = strings.TrimSpace(deviceID)
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.devices[deviceID]
	if d == nil {
		return Info{}, ErrDeviceNotFound
	}
	now := m.now()
	if d.revoked {
		return Info{}, ErrPasswordRevoke
	}
	if !d.info.PasswordExpiresAt.IsZero() && !now.Before(d.info.PasswordExpiresAt) {
		return Info{}, ErrPasswordExpiry
	}
	candidate := hashPassword(d.passwordSalt, password)
	if subtle.ConstantTimeCompare(candidate, d.passwordHash) != 1 {
		return Info{}, ErrBadPassword
	}
	return d.snapshot(now), nil
}

// RevokePassword invalidates the current temporary password.
func (m *Manager) RevokePassword(deviceID string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.devices[deviceID]
	if d == nil {
		return Info{}, ErrDeviceNotFound
	}
	d.revoked = true
	return d.snapshot(m.now()), nil
}

// SetOnline marks a device as online for the given connection.
func (m *Manager) SetOnline(deviceID, connectionID string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.devices[deviceID]
	if d == nil {
		return Info{}, ErrDeviceNotFound
	}
	d.onlineConnID = connectionID
	d.info.Online = true
	d.info.LastHeartbeat = m.now()
	return d.snapshot(m.now()), nil
}

// SetOffline clears online state only when connectionID still owns the device.
func (m *Manager) SetOffline(deviceID, connectionID string) (Info, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.devices[deviceID]
	if d == nil {
		return Info{}, false
	}
	if connectionID != "" && d.onlineConnID != connectionID {
		return d.snapshot(m.now()), false
	}
	d.onlineConnID = ""
	d.info.Online = false
	return d.snapshot(m.now()), true
}

// ForceOffline clears online state regardless of the owning connection. It is
// used when a reconnect observes a stale connection that no longer exists.
func (m *Manager) ForceOffline(deviceID string) (Info, bool) {
	return m.SetOffline(deviceID, "")
}

// Heartbeat refreshes liveness for the owning connection.
func (m *Manager) Heartbeat(deviceID, connectionID string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.devices[deviceID]
	if d == nil {
		return Info{}, ErrDeviceNotFound
	}
	if d.onlineConnID != connectionID {
		return Info{}, ErrDeviceOnline
	}
	d.info.LastHeartbeat = m.now()
	return d.snapshot(m.now()), nil
}

// OnlineConnection returns the connection ID currently owning the device.
func (m *Manager) OnlineConnection(deviceID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.devices[deviceID]
	if d == nil || d.onlineConnID == "" {
		return "", false
	}
	return d.onlineConnID, true
}

// Get returns a device view.
func (m *Manager) Get(deviceID string) (Info, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.devices[deviceID]
	if d == nil {
		return Info{}, false
	}
	return d.snapshot(m.now()), true
}

// List returns all device views.
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := make([]Info, 0, len(m.devices))
	for _, d := range m.devices {
		out = append(out, d.snapshot(now))
	}
	return out
}

// AddSession records that a session belongs to the device.
func (m *Manager) AddSession(deviceID, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.devices[deviceID]; d != nil {
		d.sessions[sessionID] = struct{}{}
	}
}

// RemoveSession removes a session association.
func (m *Manager) RemoveSession(deviceID, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.devices[deviceID]; d != nil {
		delete(d.sessions, sessionID)
	}
}

// ActiveSessions returns the number of sessions currently associated with a
// device.
func (m *Manager) ActiveSessions(deviceID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.devices[deviceID]; d != nil {
		return len(d.sessions)
	}
	return 0
}

// Cleanup removes offline devices whose password has been expired for longer
// than the retention window. It bounds memory without persisting business
// state.
func (m *Manager) Cleanup(retention time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	removed := 0
	for id, d := range m.devices {
		if d.onlineConnID != "" || len(d.sessions) > 0 {
			continue
		}
		if now.Sub(d.info.PasswordExpiresAt) > retention {
			delete(m.devices, id)
			removed++
		}
	}
	return removed
}

func (d *device) snapshot(now time.Time) Info {
	info := d.info
	info.ActiveSessions = len(d.sessions)
	if !d.info.PasswordExpiresAt.IsZero() && !now.Before(d.info.PasswordExpiresAt) {
		info.Online = d.onlineConnID != ""
	}
	return info
}

func hashPassword(salt []byte, password string) []byte {
	h := sha256.New()
	_, _ = h.Write(salt)
	_, _ = h.Write([]byte(password))
	return h.Sum(nil)
}
