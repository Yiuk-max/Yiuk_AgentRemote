package core

import "errors"

// ErrNoRoute 表示目标连接当前不在任何接入层节点的连接表里。
var ErrNoRoute = errors.New("core: connection is not attached")

// Link 是 core 唯一的出口：把一条已经编码好的协议帧送到指定连接。
//
// 单进程时由 gateway 直接实现，两个进程时由 gRPC 客户端实现；
// core 里所有出站消息都经过它，没有第二条写 socket 的路径。
type Link interface {
	// Deliver 投递一帧。目标连接不存在时返回 ErrNoRoute。
	Deliver(connectionID string, frame []byte) error
	// Alive 报告这条连接是否还挂着。core 不自己维护连接表，需要时直接问接入层。
	Alive(connectionID string) bool
}

// FatalError 表示处理这一帧之后必须关闭连接。
//
// Frame 非空时表示还有一条协议错误帧要先写出去；它是 core 编码好的字节，
// 接入层不需要理解内容，写完再关连接即可。
type FatalError struct {
	Frame []byte
	Err   error
}

func (e *FatalError) Error() string {
	if e.Err == nil {
		return "core: fatal error"
	}
	return "core: fatal error: " + e.Err.Error()
}

func (e *FatalError) Unwrap() error { return e.Err }
