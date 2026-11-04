// device 包负责内存中的设备注册、临时密码和在线状态。
// 重启后数据失效
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

// 协助连接模式，approval or direct
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
	info               Info
	passwordSalt       []byte
	passwordHash       []byte
	revoked            bool
	onlineConnectionID string
	sessions           map[string]struct{}
}

// Manager owns all device state.
type Manager struct {
	mu                 sync.Mutex
	devices            map[string]*device
	maxDevices         int
	defaultPasswordTTL time.Duration
	now                func() time.Time
}

// NewManager creates a device manager.
func NewManager(maxDevices int, defaultPasswordTTL time.Duration) *Manager {
	return newManager(maxDevices, defaultPasswordTTL, time.Now)
}

func newManager(maxDevices int, defaultPasswordTTL time.Duration, now func() time.Time) *Manager {
	if maxDevices < 1 {
		maxDevices = 1
	}
	if defaultPasswordTTL <= 0 {
		defaultPasswordTTL = 10 * time.Minute
	}
	return &Manager{devices: make(map[string]*device), maxDevices: maxDevices, defaultPasswordTTL: defaultPasswordTTL, now: now}
}

// GeneratePassword returns a 32-character high-entropy temporary password.
// The alphabet is base32 so it is easy to read over a voice channel while
// retaining 160 bits of entropy.
func GeneratePassword() (string, error) {
	var randomBytes [20]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(randomBytes[:]), nil
}

// Register creates or refreshes a device. If TemporaryPass is empty the
// manager generates one and returns it in the second result so the caller can
// hand it to the device over TLS. A device that is already online cannot be
// registered by another connection.
func (m *Manager) Register(registration Registration) (Info, string, error) {
	registration.ID = strings.TrimSpace(registration.ID)
	if registration.ID == "" {
		return Info{}, "", errors.New("device id is required")
	}
	if registration.Mode == "" {
		registration.Mode = ModeApproval
	}
	if registration.Mode != ModeDirect && registration.Mode != ModeApproval {
		return Info{}, "", ErrInvalidMode
	}
	now := m.now()
	passwordTTL := registration.TTL
	if passwordTTL <= 0 {
		passwordTTL = m.defaultPasswordTTL
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	existing := m.devices[registration.ID]
	passwordExpired := false
	if existing == nil && len(m.devices) >= m.maxDevices {
		return Info{}, "", ErrDeviceLimit
	}
	if existing != nil && existing.onlineConnectionID != "" {
		return Info{}, "", ErrDeviceOnline
	}

	generatedPassword := ""
	password := registration.TemporaryPass
	if existing != nil {
		// Re-registration is a reconnect operation, not a way to claim an
		// offline device by knowing only its public ID. The current temporary
		// password is the device's proof of identity.
		if existing.revoked {
			return Info{}, "", ErrPasswordRevoke
		}
		candidateHash := hashPassword(existing.passwordSalt, password)
		if subtle.ConstantTimeCompare(candidateHash, existing.passwordHash) != 1 {
			return Info{}, "", ErrBadPassword
		}
		passwordExpired = !existing.info.PasswordExpiresAt.IsZero() &&
			!now.Before(existing.info.PasswordExpiresAt)
	}
	if password == "" || passwordExpired {
		var err error
		password, err = GeneratePassword()
		if err != nil {
			return Info{}, "", err
		}
		generatedPassword = password
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return Info{}, "", err
	}
	passwordHash := hashPassword(salt, password)
	version := registration.PasswordVersion
	if version <= 0 {
		version = 1
	}
	if existing != nil && version <= existing.info.PasswordVersion {
		version = existing.info.PasswordVersion + 1
	}

	if existing == nil {
		existing = &device{sessions: make(map[string]struct{})}
		m.devices[registration.ID] = existing
	}
	existing.info.ID = registration.ID
	existing.info.Name = registration.Name
	existing.info.Platform = registration.Platform
	existing.info.Mode = registration.Mode
	existing.info.PasswordVersion = version
	existing.info.PasswordExpiresAt = now.Add(passwordTTL)
	existing.info.LastHeartbeat = now
	existing.info.Online = false
	existing.info.CreatedAt = now
	existing.passwordSalt = salt
	existing.passwordHash = passwordHash
	existing.revoked = false
	existing.onlineConnectionID = ""

	return existing.snapshot(now), generatedPassword, nil
}

// UpdatePassword replaces the temporary password and mode. A stale version is
// rejected so an old client cannot roll back a newer password.
func (m *Manager) UpdatePassword(deviceID, password string, version int, mode Mode, passwordTTL time.Duration) (Info, string, error) {
	deviceID = strings.TrimSpace(deviceID)
	if mode == "" {
		mode = ModeApproval
	}
	if mode != ModeDirect && mode != ModeApproval {
		return Info{}, "", ErrInvalidMode
	}
	now := m.now()
	if passwordTTL <= 0 {
		passwordTTL = m.defaultPasswordTTL
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	deviceRecord := m.devices[deviceID]
	if deviceRecord == nil {
		return Info{}, "", ErrDeviceNotFound
	}
	if version > 0 && version <= deviceRecord.info.PasswordVersion {
		return Info{}, "", ErrStaleVersion
	}
	generatedPassword := ""
	if password == "" {
		var err error
		password, err = GeneratePassword()
		if err != nil {
			return Info{}, "", err
		}
		generatedPassword = password
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return Info{}, "", err
	}
	deviceRecord.passwordSalt = salt
	deviceRecord.passwordHash = hashPassword(salt, password)
	deviceRecord.revoked = false
	deviceRecord.info.Mode = mode
	if version > 0 {
		deviceRecord.info.PasswordVersion = version
	} else {
		deviceRecord.info.PasswordVersion = deviceRecord.info.PasswordVersion + 1
	}
	deviceRecord.info.PasswordExpiresAt = now.Add(passwordTTL)
	return deviceRecord.snapshot(now), generatedPassword, nil
}

// VerifyPassword checks the temporary password for a device. It never returns
// the password or hash to callers.
func (m *Manager) VerifyPassword(deviceID, password string) (Info, error) {
	deviceID = strings.TrimSpace(deviceID)
	m.mu.Lock()
	defer m.mu.Unlock()
	deviceRecord := m.devices[deviceID]
	if deviceRecord == nil {
		return Info{}, ErrDeviceNotFound
	}
	now := m.now()
	if deviceRecord.revoked {
		return Info{}, ErrPasswordRevoke
	}
	if !deviceRecord.info.PasswordExpiresAt.IsZero() && !now.Before(deviceRecord.info.PasswordExpiresAt) {
		return Info{}, ErrPasswordExpiry
	}
	candidateHash := hashPassword(deviceRecord.passwordSalt, password)
	if subtle.ConstantTimeCompare(candidateHash, deviceRecord.passwordHash) != 1 {
		return Info{}, ErrBadPassword
	}
	return deviceRecord.snapshot(now), nil
}

// RevokePassword invalidates the current temporary password.
func (m *Manager) RevokePassword(deviceID string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	deviceRecord := m.devices[deviceID]
	if deviceRecord == nil {
		return Info{}, ErrDeviceNotFound
	}
	deviceRecord.revoked = true
	return deviceRecord.snapshot(m.now()), nil
}

// SetOnline marks a device as online for the given connection.
func (m *Manager) SetOnline(deviceID, connectionID string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	deviceRecord := m.devices[deviceID]
	if deviceRecord == nil {
		return Info{}, ErrDeviceNotFound
	}
	deviceRecord.onlineConnectionID = connectionID
	deviceRecord.info.Online = true
	deviceRecord.info.LastHeartbeat = m.now()
	return deviceRecord.snapshot(m.now()), nil
}

// SetOffline clears online state only when connectionID still owns the device.
func (m *Manager) SetOffline(deviceID, connectionID string) (Info, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	deviceRecord := m.devices[deviceID]
	if deviceRecord == nil {
		return Info{}, false
	}
	if connectionID != "" && deviceRecord.onlineConnectionID != connectionID {
		return deviceRecord.snapshot(m.now()), false
	}
	deviceRecord.onlineConnectionID = ""
	deviceRecord.info.Online = false
	return deviceRecord.snapshot(m.now()), true
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
	deviceRecord := m.devices[deviceID]
	if deviceRecord == nil {
		return Info{}, ErrDeviceNotFound
	}
	if deviceRecord.onlineConnectionID != connectionID {
		return Info{}, ErrDeviceOnline
	}
	deviceRecord.info.LastHeartbeat = m.now()
	return deviceRecord.snapshot(m.now()), nil
}

// OnlineConnection returns the connection ID currently owning the device.
func (m *Manager) OnlineConnection(deviceID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	deviceRecord := m.devices[deviceID]
	if deviceRecord == nil || deviceRecord.onlineConnectionID == "" {
		return "", false
	}
	return deviceRecord.onlineConnectionID, true
}

// Get returns a device view.
func (m *Manager) Get(deviceID string) (Info, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	deviceRecord := m.devices[deviceID]
	if deviceRecord == nil {
		return Info{}, false
	}
	return deviceRecord.snapshot(m.now()), true
}

// List returns all device views.
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	deviceSnapshots := make([]Info, 0, len(m.devices))
	for _, deviceRecord := range m.devices {
		deviceSnapshots = append(deviceSnapshots, deviceRecord.snapshot(now))
	}
	return deviceSnapshots
}

// AddSession records that a session belongs to the device.
func (m *Manager) AddSession(deviceID, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if deviceRecord := m.devices[deviceID]; deviceRecord != nil {
		deviceRecord.sessions[sessionID] = struct{}{}
	}
}

// RemoveSession removes a session association.
func (m *Manager) RemoveSession(deviceID, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if deviceRecord := m.devices[deviceID]; deviceRecord != nil {
		delete(deviceRecord.sessions, sessionID)
	}
}

// SessionCount returns the number of sessions currently associated with a
// device.
func (m *Manager) SessionCount(deviceID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if deviceRecord := m.devices[deviceID]; deviceRecord != nil {
		return len(deviceRecord.sessions)
	}
	return 0
}

// RemoveExpiredOfflineDevices removes offline devices whose password has been expired for longer
// than the retention window. It bounds memory without persisting business
// state.
func (m *Manager) RemoveExpiredOfflineDevices(retention time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	removedCount := 0
	for deviceID, deviceRecord := range m.devices {
		if deviceRecord.onlineConnectionID != "" || len(deviceRecord.sessions) > 0 {
			continue
		}
		if now.Sub(deviceRecord.info.PasswordExpiresAt) > retention {
			delete(m.devices, deviceID)
			removedCount++
		}
	}
	return removedCount
}

func (deviceRecord *device) snapshot(now time.Time) Info {
	info := deviceRecord.info
	info.ActiveSessions = len(deviceRecord.sessions)
	if !deviceRecord.info.PasswordExpiresAt.IsZero() && !now.Before(deviceRecord.info.PasswordExpiresAt) {
		info.Online = deviceRecord.onlineConnectionID != ""
	}
	return info
}

func hashPassword(salt []byte, password string) []byte {
	hasher := sha256.New()
	_, _ = hasher.Write(salt)
	_, _ = hasher.Write([]byte(password))
	return hasher.Sum(nil)
}
