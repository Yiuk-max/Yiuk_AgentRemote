package core

// Caller 是一条连接的身份。
//
// 连接表由接入层（gateway）保管，core 不记账：每处理一帧就拿到一份 Caller，
// 处理过程中可能更新它（认证成功后变成 player、注册设备后带上 DeviceID），
// 处理结束时接入层保存更新后的值。
type Caller struct {
	ConnectionID string // 接入层分配的连接 ID
	RemoteAddr   string // 接入层看到的对端地址，仅用于日志与按 IP 限流
	Role         Role   // 匿名 / 操作者 / 设备
	DeviceID     string // 设备连接注册后才有值
	DisplayName  string // 设备名，用作会话里的操作者/设备标签
	BusinessType string // 认证后确认的业务类型
}

// Role 用字符串表示，方便在两进程之间原样传递，不需要额外映射表。
type Role string

const (
	RoleAnonymous Role = ""
	RolePlayer    Role = "player"
	RoleDevice    Role = "device"
)
