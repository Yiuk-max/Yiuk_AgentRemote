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
	"fmt"
	"math/big"
	"sort"
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
	// ErrDeviceIDExhausted 只会在设备码空间真的用完时返回；9 位十进制在
	// maxDevices 级别的规模下不可能触发，作为防御性错误保留。
	ErrDeviceIDExhausted = errors.New("no free device id available")
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
	info         Info
	passwordSalt []byte
	passwordHash []byte
	revoked      bool
	// used 表示该设备曾经建立过会话。从未被使用的条目只是占着设备槽，
	// 离线一段时间后可以被释放（码进墓碑，原持有者仍能 reclaim）。
	used               bool
	onlineConnectionID string
	sessions           map[string]struct{}
}

// tombstone 记录一个已释放的设备码。它保留原持有者的密码凭据，因为：
//   - 墓碑里的码永远不会再分配给别的设备，避免 operator 手里的旧设备码在释放后
//     指向另一台机器（远程会话串台）；
//   - 原持有者凭正确密码可以把码收回来，所以“放假一周回来”不会换码。
//
// 墓碑与 devices 一样只存在于内存，进程重启后清空。
type tombstone struct {
	id         string
	name       string
	platform   string
	mode       Mode
	salt       []byte
	hash       []byte
	version    int
	revoked    bool
	used       bool
	createdAt  time.Time
	releasedAt time.Time
}

// Manager owns all device state.
type Manager struct {
	mu                 sync.Mutex
	devices            map[string]*device
	tombstones         map[string]*tombstone
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
	return &Manager{
		devices:            make(map[string]*device),
		tombstones:         make(map[string]*tombstone),
		maxDevices:         maxDevices,
		defaultPasswordTTL: defaultPasswordTTL,
		now:                now,
	}
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
	if existing != nil && existing.revoked {
		return Info{}, "", ErrPasswordRevoke
	}
	buried := m.tombstones[registration.ID]
	if existing == nil && buried != nil && buried.revoked {
		return Info{}, "", ErrPasswordRevoke
	}

	// 设备码是公开标识，只有匹配的临时密码才算拥有它。所以密码校验**先于**在线冲突
	// 判定：密码不匹配时对方根本不属于这个码，直接分配新码；只有密码匹配才是同一个
	// 身份的两个连接（僵尸 socket 由 handler 先 ForceOffline 清除）。
	password := registration.TemporaryPass
	switch {
	case existing != nil:
		candidateHash := hashPassword(existing.passwordSalt, password)
		if subtle.ConstantTimeCompare(candidateHash, existing.passwordHash) != 1 {
			return m.createDevice(registration, "", time.Time{}, now, passwordTTL)
		}
		if existing.onlineConnectionID != "" {
			return Info{}, "", ErrDeviceOnline
		}
	case buried != nil:
		candidateHash := hashPassword(buried.salt, password)
		if subtle.ConstantTimeCompare(candidateHash, buried.hash) != 1 {
			return m.createDevice(registration, "", time.Time{}, now, passwordTTL)
		}
		// 原持有者回来了：沿用原设备码，版本号递增，创建和 used 状态保留。
		registration.PasswordVersion = buried.version + 1
		info, generatedPassword, err := m.createDevice(registration, buried.id, buried.createdAt, now, passwordTTL)
		if err == nil {
			if record := m.devices[buried.id]; record != nil {
				record.used = buried.used
			}
		}
		return info, generatedPassword, err
	default:
		return m.createDevice(registration, registration.ID, time.Time{}, now, passwordTTL)
	}

	// existing != nil 且密码匹配：刷新已有条目。
	generatedPassword := ""
	passwordExpired := !existing.info.PasswordExpiresAt.IsZero() &&
		!now.Before(existing.info.PasswordExpiresAt)
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
	if version <= existing.info.PasswordVersion {
		version = existing.info.PasswordVersion + 1
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

// createDevice 创建一个新的 device 条目。requestedID 为空时由服务端分配设备码，
// 这是“密码不匹配 → 换一个新码”的唯一出口。调用方必须持有 m.mu。
func (m *Manager) createDevice(registration Registration, requestedID string, createdAt time.Time, now time.Time, passwordTTL time.Duration) (Info, string, error) {
	if len(m.devices) >= m.maxDevices {
		return Info{}, "", ErrDeviceLimit
	}
	id := requestedID
	if id == "" {
		allocated, err := m.allocDeviceID()
		if err != nil {
			return Info{}, "", err
		}
		id = allocated
	}
	password := registration.TemporaryPass
	generatedPassword := ""
	if password == "" {
		generated, err := GeneratePassword()
		if err != nil {
			return Info{}, "", err
		}
		password, generatedPassword = generated, generated
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return Info{}, "", err
	}
	version := registration.PasswordVersion
	if version <= 0 {
		version = 1
	}
	if createdAt.IsZero() {
		createdAt = now
	}
	record := &device{sessions: make(map[string]struct{})}
	record.info = Info{
		ID:                id,
		Name:              registration.Name,
		Platform:          registration.Platform,
		Mode:              registration.Mode,
		PasswordVersion:   version,
		PasswordExpiresAt: now.Add(passwordTTL),
		LastHeartbeat:     now,
		CreatedAt:         createdAt,
	}
	record.passwordSalt = salt
	record.passwordHash = hashPassword(salt, password)
	m.devices[id] = record
	// 原持有者 reclaim 自己的墓碑时，码从墓碑搬回 devices。
	delete(m.tombstones, id)
	return record.snapshot(now), generatedPassword, nil
}

// allocDeviceID 返回一个既不在 devices、也不在 tombstones 里的 9 位十进制设备码。
// 墓碑里的码对其他人永久不可分配。调用方必须持有 m.mu。
func (m *Manager) allocDeviceID() (string, error) {
	for attempt := 0; attempt < 128; attempt++ {
		id, err := randomDeviceID()
		if err != nil {
			return "", err
		}
		if _, taken := m.devices[id]; taken {
			continue
		}
		if _, buried := m.tombstones[id]; buried {
			continue
		}
		return id, nil
	}
	return "", ErrDeviceIDExhausted
}

var deviceIDSpace = big.NewInt(int64(1000000000))

// randomDeviceID 返回 9 位十进制设备码（允许前导零）。
func randomDeviceID() (string, error) {
	value, err := rand.Int(rand.Reader, deviceIDSpace)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%09d", value.Int64()), nil
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
		deviceRecord.used = true
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

// RemoveExpiredOfflineDevices moves offline devices whose password has been expired for
// longer than the retention window into the tombstone set. It bounds memory without
// persisting business state.
func (m *Manager) RemoveExpiredOfflineDevices(retention time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	removedCount := 0
	for _, deviceRecord := range m.devices {
		if deviceRecord.onlineConnectionID != "" || len(deviceRecord.sessions) > 0 {
			continue
		}
		if now.Sub(deviceRecord.info.PasswordExpiresAt) > retention {
			m.release(deviceRecord)
			removedCount++
		}
	}
	m.pruneTombstones()
	return removedCount
}

// ReleaseUnusedDevices releases devices that never hosted a session and have been
// idle for longer than idle. It keeps a registration flood from filling the device
// table without penalising real machines: the code moves to the tombstone set and
// the original holder can still reclaim it with the same password.
func (m *Manager) ReleaseUnusedDevices(idle time.Duration) int {
	if idle <= 0 {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	releasedCount := 0
	for _, deviceRecord := range m.devices {
		if deviceRecord.used || deviceRecord.onlineConnectionID != "" || len(deviceRecord.sessions) > 0 {
			continue
		}
		if now.Sub(deviceRecord.info.LastHeartbeat) <= idle {
			continue
		}
		m.release(deviceRecord)
		releasedCount++
	}
	m.pruneTombstones()
	return releasedCount
}

// release 把设备条目搬进墓碑集合，保留密码凭据以便原持有者 reclaim。
// 墓碑里的码对其他人永久不可分配。调用方必须持有 m.mu。
func (m *Manager) release(deviceRecord *device) {
	m.tombstones[deviceRecord.info.ID] = &tombstone{
		id:         deviceRecord.info.ID,
		name:       deviceRecord.info.Name,
		platform:   deviceRecord.info.Platform,
		mode:       deviceRecord.info.Mode,
		salt:       deviceRecord.passwordSalt,
		hash:       deviceRecord.passwordHash,
		version:    deviceRecord.info.PasswordVersion,
		revoked:    deviceRecord.revoked,
		used:       deviceRecord.used,
		createdAt:  deviceRecord.info.CreatedAt,
		releasedAt: m.now(),
	}
	delete(m.devices, deviceRecord.info.ID)
}

// tombstoneLimit 是墓碑集合的上限。超出后按释放时间淘汰最旧的墓碑，
// 被淘汰的码重新可分配。调用方必须持有 m.mu。
func (m *Manager) pruneTombstones() {
	limit := 10 * m.maxDevices
	if limit < 1024 {
		limit = 1024
	}
	if len(m.tombstones) <= limit {
		return
	}
	released := make([]*tombstone, 0, len(m.tombstones))
	for _, record := range m.tombstones {
		released = append(released, record)
	}
	sort.Slice(released, func(i, j int) bool { return released[i].releasedAt.Before(released[j].releasedAt) })
	for index := 0; index < len(released)-limit; index++ {
		delete(m.tombstones, released[index].id)
	}
}

// TombstoneCount returns the number of retained (released, non-reusable) device codes.
func (m *Manager) TombstoneCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tombstones)
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
