// Package jobs 是命令任务与文件传输共用的生命周期词汇。
//
// 两者是两种不同的异步操作（一个搬 stdout、一个搬文件分块），但状态机、终态判定
// 和“终态记录保留多久”的规则是同一套。分开各写一份的后果不是重复几十行，而是
// 两者的 isTerminal 迟早会不一致——例如传输多出 timed_out 而任务没有。
package jobs

import "time"

// Status 是一次异步操作的生命周期状态。取值即协议里的字符串，改值等于改协议。
type Status string

const (
	StatusQueued        Status = "queued"
	StatusRunning       Status = "running"
	StatusSucceeded     Status = "succeeded"
	StatusFailed        Status = "failed"
	StatusTimedOut      Status = "timed_out"
	StatusCancelled     Status = "cancelled"
	StatusResultUnknown Status = "result_unknown"
)

// Terminal 报告该状态是否已终结。
//
// result_unknown 也是终态：它表示“可能已经生效，不要盲目重试”，
// 与 failed（确定没生效）必须区分——这是本项目最核心的语义之一。
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusTimedOut, StatusCancelled, StatusResultUnknown:
		return true
	default:
		return false
	}
}

// Expired 报告一条终态记录是否已经超过保留时长。
// finishedAt 为零值表示这不是一条已终结的记录，永远不算过期。
func Expired(status Status, finishedAt, now time.Time, retain time.Duration) bool {
	if !status.Terminal() || finishedAt.IsZero() {
		return false
	}
	return now.Sub(finishedAt) >= retain
}
