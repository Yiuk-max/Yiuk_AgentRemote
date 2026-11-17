// 玩家密码仅用于验证连接是否可以使用平台中继资源；它永远不会授予对设备的访问权限。
// 设备访问始终需要单独的临时密码，并且在审批模式下，需要设备所有者的明确决定。
package auth

//player 密码常量时间比较 + 按 IP 限流
import (
	"crypto/subtle"
	"errors"
	"time"

	baseauth "agentremote/internal/auth"
)

var (
	ErrBadPassword = errors.New("player password is incorrect")
	ErrRateLimited = errors.New("too many authentication attempts")
)

// Authenticator performs constant-time password checks with per-IP rate
// limiting. It stores only the configured secret in process memory
type Authenticator struct {
	password []byte
	limiter  *baseauth.Limiter
}

// 用给定的玩家密码返回一个身份验证器
func New(password string, attemptsPerMinute int) *Authenticator {
	return &Authenticator{
		password: []byte(password),
		limiter:  baseauth.NewLimiter(attemptsPerMinute, time.Minute),
	}
}

// Authenticate 验证候选密码。在进行比较之前会先咨询速率限制器，这样无法通过反复失败
// remote 是接入层看到的对端地址，仅用于按 IP 限流。
func (a *Authenticator) Authenticate(remote string, candidate string) error {
	if a == nil {
		return ErrBadPassword
	}
	if remote != "" && a.limiter != nil && !a.limiter.Allow(remote) {
		return ErrRateLimited
	}
	if subtle.ConstantTimeCompare([]byte(candidate), a.password) != 1 {
		return ErrBadPassword
	}
	return nil
}
