package session

import (
	"errors"
	"testing"
	"time"

	"agentremote/internal/platform/device"
)

func newTestManager(now *time.Time) *Manager {
	return newManager(4, 2, 10*time.Minute, 2*time.Minute, func() time.Time { return *now })
}

func TestDirectAndApprovalSessionLifecycle(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newTestManager(&now)

	direct, err := m.Create("direct", CreateRequest{
		DeviceID: "d1", OperatorConnectionID: "op1", DeviceConnectionID: "dev1",
		Purpose: "help", Permissions: []string{PermissionExecuteCommand}, Mode: device.ModeDirect,
	})
	if err != nil {
		t.Fatal(err)
	}
	if direct.Status != StatusApproved {
		t.Fatalf("direct status = %s, want approved", direct.Status)
	}
	if _, err := m.Authorize("direct", "op1", PermissionExecuteCommand); err != nil {
		t.Fatalf("direct session should authorize: %v", err)
	}

	pending, err := m.Create("pending", CreateRequest{
		DeviceID: "d1", OperatorConnectionID: "op2", DeviceConnectionID: "dev1",
		Purpose: "help", Permissions: []string{PermissionExecuteCommand}, Mode: device.ModeApproval,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != StatusPendingApproval {
		t.Fatalf("pending status = %s, want pending_approval", pending.Status)
	}
	if _, err := m.Authorize("pending", "op2", PermissionExecuteCommand); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("pending authorize got %v, want ErrInvalidState", err)
	}
	if _, err := m.Approve("pending", "wrong-device"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("wrong approver got %v, want ErrNotOwner", err)
	}
	approved, err := m.Approve("pending", "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != StatusApproved {
		t.Fatalf("approved status = %s", approved.Status)
	}
	if _, err := m.Approve("pending", "dev1"); err != nil {
		t.Fatalf("approve should be idempotent: %v", err)
	}
	active, err := m.Activate("pending", "op2")
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != StatusActive {
		t.Fatalf("active status = %s", active.Status)
	}
	closed, err := m.Close("pending", "done")
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != StatusClosed {
		t.Fatalf("closed status = %s", closed.Status)
	}
	if _, err := m.Close("pending", "again"); err != nil {
		t.Fatalf("close should be idempotent: %v", err)
	}
}

func TestAuthorizeBindsOperatorPermissionAndExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newTestManager(&now)
	if _, err := m.Create("s", CreateRequest{
		DeviceID: "d1", OperatorConnectionID: "op1", DeviceConnectionID: "dev1",
		Purpose: "help", Permissions: []string{PermissionExecuteCommand}, Mode: device.ModeDirect,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Authorize("s", "op2", PermissionExecuteCommand); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("wrong operator got %v, want ErrNotOwner", err)
	}
	if _, err := m.Authorize("s", "op1", "admin"); !errors.Is(err, ErrNoPermission) {
		t.Fatalf("wrong permission got %v, want ErrNoPermission", err)
	}
	now = now.Add(11 * time.Minute)
	if _, err := m.Authorize("s", "op1", PermissionExecuteCommand); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired got %v, want ErrExpired", err)
	}
	expired := m.Expire()
	if len(expired) != 1 || expired[0].Status != StatusExpired {
		t.Fatalf("unexpected expired sessions: %#v", expired)
	}
}

func TestLimitsInterruptAndRevoke(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newTestManager(&now)
	for i, id := range []string{"a", "b"} {
		if _, err := m.Create(id, CreateRequest{
			DeviceID: "d1", OperatorConnectionID: "op1", DeviceConnectionID: "dev1",
			Purpose: "help", Permissions: []string{PermissionExecuteCommand}, Mode: device.ModeDirect,
		}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := m.Create("c", CreateRequest{
		DeviceID: "d1", OperatorConnectionID: "op1", DeviceConnectionID: "dev1",
		Purpose: "help", Permissions: []string{PermissionExecuteCommand}, Mode: device.ModeDirect,
	}); !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("got %v, want ErrSessionLimit", err)
	}
	interrupted := m.InterruptByOperator("op1", "operator_disconnected")
	if len(interrupted) != 2 {
		t.Fatalf("interrupted %d sessions, want 2", len(interrupted))
	}
	for _, s := range interrupted {
		if s.Status != StatusInterrupted {
			t.Fatalf("status = %s, want interrupted", s.Status)
		}
	}
	if len(m.InterruptByOperator("op1", "again")) != 0 {
		t.Fatal("terminal sessions must not be interrupted twice")
	}
}

func TestRejectAndRevoke(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newTestManager(&now)
	if _, err := m.Create("p", CreateRequest{
		DeviceID: "d1", OperatorConnectionID: "op1", DeviceConnectionID: "dev1",
		Purpose: "help", Permissions: []string{PermissionExecuteCommand}, Mode: device.ModeApproval,
	}); err != nil {
		t.Fatal(err)
	}
	rejected, err := m.Reject("p", "dev1", "no")
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != StatusRejected {
		t.Fatalf("status = %s", rejected.Status)
	}
	if _, err := m.Authorize("p", "op1", PermissionExecuteCommand); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("rejected authorize got %v", err)
	}
}

func TestApproveAndRejectRejectExpiredPendingSession(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newTestManager(&now)
	if _, err := m.Create("p1", CreateRequest{
		DeviceID: "d1", OperatorConnectionID: "op1", DeviceConnectionID: "dev1",
		Purpose: "help", Permissions: []string{PermissionExecuteCommand}, Mode: device.ModeApproval,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create("p2", CreateRequest{
		DeviceID: "d1", OperatorConnectionID: "op1", DeviceConnectionID: "dev1",
		Purpose: "help", Permissions: []string{PermissionExecuteCommand}, Mode: device.ModeApproval,
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Minute)
	if _, err := m.Approve("p1", "dev1"); !errors.Is(err, ErrExpired) {
		t.Fatalf("approve expired got %v, want ErrExpired", err)
	}
	if s, ok := m.Get("p1"); !ok || s.Status != StatusExpired {
		t.Fatalf("expired session state: %#v", s)
	}
	if _, err := m.Reject("p2", "dev1", "no"); !errors.Is(err, ErrExpired) {
		t.Fatalf("reject expired got %v, want ErrExpired", err)
	}
}
