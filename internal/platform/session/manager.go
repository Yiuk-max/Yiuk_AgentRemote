// session 包负责内存中的会话授权状态机。
// 一个会话绑定一个设备、一个操作员连接、一个权限集和一个过期时间。
// 后端重启后会话直接丢失
package session

// Session lifecycle: requested -> pending_approval -> approved -> active -> closed
// 授权状态机、权限集合、设备/操作者绑定、过期
import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"agentremote/internal/platform/device"
)

// session lifecycle state.
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

// 会话权限是由设备owner明确请求并批准的
// 文件下载不会继承命令执行权限，远程写入不会继承读取权限

const (
	PermissionExecuteCommand = "execute_command"
	PermissionReadFile       = "read_file"
	PermissionWriteFile      = "write_file"
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

// Session 是服务进程内存中的会话记录；平台重启后记录会丢失，不写入持久化存储。
type Session struct {
	ID                   string              // 会话唯一 ID；用于关联授权、命令任务和文件传输
	DeviceID             string              // DeviceID 目标设备的设备码
	OperatorConnectionID string              //创建会话的操作者连接 ID，用于校验会话归属和回传结果
	DeviceConnectionID   string              //会话创建时目标设备的连接 ID，确保请求只路由到该设备连接
	OperatorLabel        string              //展示给设备所有者的操作者标识
	OperatorSource       string              //记录的操作者IP地址，供所有者查看
	Purpose              string              //操作者提交的会话用途说明，会显示在授权请求中。
	Permissions          map[string]struct{} //本会话获准的权限集合；命令执行、文件读取和文件写入分别授权
	Mode                 device.Mode         //设备配置的授权模式：approval 需要所有者批准，direct 跳过逐次批准
	Status               Status              //会话生命周期状态
	CreatedAt            time.Time           //会话创建时间
	ExpiresAt            time.Time           // 会话过期时间；审批等待阶段和批准后的会话使用各自的时限
	ApprovedAt           time.Time           // 所有者批准会话的时间
	ClosedAt             time.Time           // 会话进入关闭、拒绝、过期或中断等终态的时间
	CloseReason          string              // 记录会话结束原因
	LastActivity         time.Time           // 最近一次会话活动时间
}

// Clone 返回一个副本，调用者可以在不持有管理器锁的情况下读取
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

// 报告会话是否授予某个权限
func (s *Session) HasPermission(permission string) bool {
	if s == nil {
		return false
	}
	_, ok := s.Permissions[permission]
	return ok
}

// 以一种顺序返回权限列表
func (s *Session) PermissionList() []string {
	out := make([]string, 0, len(s.Permissions))
	for p := range s.Permissions {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// 会话创建远程协助请求
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

// Manager 拥有所有会话的状态
type Manager struct {
	mu           sync.Mutex
	sessions     map[string]*Session
	max          int
	maxPerDevice int
	sessionTTL   time.Duration //TTL->存活时间
	approvalTTL  time.Duration
	now          func() time.Time
}

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

// 创建一个直接或被批准的session
func (m *Manager) Create(id string, req CreateRequest) (*Session, error) {
	if req.Purpose == "" {
		return nil, ErrInvalidPurpose
	}
	perms, err := normalizePermissions(req.Permissions) //检查权限格式和内容
	if err != nil {
		return nil, err
	}
	mode := req.Mode
	if mode != device.ModeDirect && mode != device.ModeApproval {
		mode = device.ModeApproval
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// 检查
	if len(m.sessions) >= m.max {
		return nil, ErrSessionLimit
	}
	activeForDevice := 0 //设备已经有的会话数量
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
	m.sessions[id] = s //加入manager管理
	return s.Clone(), nil
}

// 批准会将待处理的会话转为已批准
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
		if !now.Before(s.ExpiresAt) { //同意时间过期了
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
// 授权会验证操作员连接是否拥有一个已批准/有效且未过期的请求权限会话。
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
// Activate 标记已批准的会话为活跃状态
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
// Close 会终止一个会话。已经是终止状态的会话会原样返回，所以可以安全地重试关闭操作
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
// 获取会话信息的副本
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
// 会话列表
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
// 某个设备的已激活会话数
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
// InterruptByOperator 操作员已断开连接，中断每个操作员拥有的实时会话
func (m *Manager) InterruptByOperator(connectionID, reason string) []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.interruptLocked(func(s *Session) bool {
		return s.OperatorConnectionID == connectionID && !isTerminal(s.Status)
	}, reason)
}

// InterruptByDevice interrupts every live session for a disconnected device.
// 设备owner中断会话，自动中断所有会话
func (m *Manager) InterruptByDevice(deviceID, reason string) []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.interruptLocked(func(s *Session) bool {
		return s.DeviceID == deviceID && !isTerminal(s.Status)
	}, reason)
}

// RevokeByDevice closes every live session after the device rotates or revokes
// its temporary password.
// 设备更换或撤销临时密码后关闭所有活跃会话。
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
		if p != PermissionExecuteCommand && p != PermissionReadFile && p != PermissionWriteFile {
			return nil, fmt.Errorf("%w: %q", ErrNoPermission, p)
		}
		out[p] = struct{}{}
	}
	return out, nil
}
