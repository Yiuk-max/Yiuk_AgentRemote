package device

import (
	"errors"
	"testing"
	"time"
)

func TestRegisterVerifyExpireAndRevoke(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(2, time.Minute, func() time.Time { return now })
	password := "0123456789abcdef0123456789abcdef"

	info, generated, err := m.Register(Registration{
		ID: "device-1", Name: "desk", Platform: "windows", TemporaryPass: password, Mode: ModeApproval,
	})
	if err != nil {
		t.Fatal(err)
	}
	if generated != "" {
		t.Fatal("provided password should not be echoed")
	}
	if info.Mode != ModeApproval || info.PasswordVersion != 1 {
		t.Fatalf("unexpected info: %#v", info)
	}

	if _, err := m.VerifyPassword("device-1", password); err != nil {
		t.Fatalf("correct password rejected: %v", err)
	}
	if _, err := m.VerifyPassword("device-1", "wrong"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("got %v, want ErrBadPassword", err)
	}

	now = now.Add(2 * time.Minute)
	if _, err := m.VerifyPassword("device-1", password); !errors.Is(err, ErrPasswordExpiry) {
		t.Fatalf("got %v, want ErrPasswordExpiry", err)
	}

	now = now.Add(-2 * time.Minute)
	if _, err := m.RevokePassword("device-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.VerifyPassword("device-1", password); !errors.Is(err, ErrPasswordRevoke) {
		t.Fatalf("got %v, want ErrPasswordRevoke", err)
	}
}

func TestExpiredPasswordCanReRegisterAndRotate(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(2, time.Minute, func() time.Time { return now })
	oldPassword := "0123456789abcdef0123456789abcdef"
	info, _, err := m.Register(Registration{ID: "d", TemporaryPass: oldPassword, PasswordVersion: 4})
	if err != nil {
		t.Fatal(err)
	}
	now = info.PasswordExpiresAt.Add(time.Second)

	refreshed, newPassword, err := m.Register(Registration{ID: "d", TemporaryPass: oldPassword, PasswordVersion: 4})
	if err != nil {
		t.Fatalf("expired credential holder should recover registration: %v", err)
	}
	if len(newPassword) != 32 {
		t.Fatalf("server should issue a replacement password, got length %d", len(newPassword))
	}
	if refreshed.PasswordVersion <= info.PasswordVersion {
		t.Fatalf("password version did not advance: old=%d new=%d", info.PasswordVersion, refreshed.PasswordVersion)
	}
	if _, err := m.VerifyPassword("d", newPassword); err != nil {
		t.Fatalf("replacement password rejected: %v", err)
	}
	if _, err := m.VerifyPassword("d", oldPassword); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("old password should be invalid after recovery, got %v", err)
	}
}

func TestGeneratedPasswordIsLongAndUnique(t *testing.T) {
	a, err := GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 32 || len(b) != 32 {
		t.Fatalf("generated passwords must be 32 characters: %q %q", a, b)
	}
	if a == b {
		t.Fatal("generated passwords must not repeat")
	}
}

func TestUpdatePasswordRejectsStaleVersion(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(2, time.Minute, func() time.Time { return now })
	if _, _, err := m.Register(Registration{ID: "d", TemporaryPass: "0123456789abcdef0123456789abcdef", PasswordVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.UpdatePassword("d", "fedcba9876543210fedcba9876543210", 2, ModeDirect, time.Minute); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("got %v, want ErrStaleVersion", err)
	}
	info, _, err := m.UpdatePassword("d", "fedcba9876543210fedcba9876543210", 3, ModeDirect, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if info.PasswordVersion != 3 || info.Mode != ModeDirect {
		t.Fatalf("unexpected updated info: %#v", info)
	}
}

func TestMismatchedPasswordGetsNewDeviceID(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(4, time.Minute, func() time.Time { return now })
	password := "0123456789abcdef0123456789abcdef"
	other := "fedcba9876543210fedcba9876543210"
	if _, _, err := m.Register(Registration{ID: "d", TemporaryPass: password}); err != nil {
		t.Fatal(err)
	}

	// 设备码是公开标识，密码不匹配时不能抢走它，而是拿一个新码。
	assigned, _, err := m.Register(Registration{ID: "d", TemporaryPass: other})
	if err != nil {
		t.Fatalf("mismatched password should be reassigned, got %v", err)
	}
	if assigned.ID == "d" {
		t.Fatal("mismatched password must not keep the requested device id")
	}
	if len(assigned.ID) != 9 {
		t.Fatalf("assigned device id = %q, want 9 digits", assigned.ID)
	}

	original, ok := m.Get("d")
	if !ok || original.ID != "d" {
		t.Fatal("original device entry must stay untouched")
	}
	if _, err := m.VerifyPassword("d", password); err != nil {
		t.Fatalf("original password rejected after reassignment: %v", err)
	}

	// 新码可以凭自己的密码重连。
	again, _, err := m.Register(Registration{ID: assigned.ID, TemporaryPass: other})
	if err != nil || again.ID != assigned.ID {
		t.Fatalf("reassigned device should reconnect: id=%q err=%v", again.ID, err)
	}
}

func TestTombstoneBlocksReuseButAllowsOwnerReclaim(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(4, time.Minute, func() time.Time { return now })
	password := "0123456789abcdef0123456789abcdef"
	other := "fedcba9876543210fedcba9876543210"
	if _, _, err := m.Register(Registration{ID: "123456789", TemporaryPass: password}); err != nil {
		t.Fatal(err)
	}

	now = now.Add(10 * time.Minute)
	if removed := m.RemoveExpiredOfflineDevices(time.Minute); removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if m.TombstoneCount() != 1 {
		t.Fatalf("tombstone count = %d, want 1", m.TombstoneCount())
	}
	if _, ok := m.Get("123456789"); ok {
		t.Fatal("released device should leave the device map")
	}

	// 别人拿不到墓碑里的码。
	intruder, _, err := m.Register(Registration{ID: "123456789", TemporaryPass: other})
	if err != nil {
		t.Fatal(err)
	}
	if intruder.ID == "123456789" {
		t.Fatal("tombstoned device id must not be reassigned to another device")
	}

	// 原持有者凭旧密码回来，沿用原码，版本递增。
	reclaimed, _, err := m.Register(Registration{ID: "123456789", TemporaryPass: password})
	if err != nil {
		t.Fatalf("owner reclaim failed: %v", err)
	}
	if reclaimed.ID != "123456789" {
		t.Fatalf("reclaim returned %q, want the original id", reclaimed.ID)
	}
	if reclaimed.PasswordVersion <= 1 {
		t.Fatalf("reclaim should bump the password version, got %d", reclaimed.PasswordVersion)
	}
	if m.TombstoneCount() != 0 {
		t.Fatalf("reclaim should consume the tombstone, got %d", m.TombstoneCount())
	}
	if _, err := m.VerifyPassword("123456789", password); err != nil {
		t.Fatalf("reclaimed password rejected: %v", err)
	}
}

func TestRevokedDeviceNeverReclaimedOrReassigned(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(4, time.Minute, func() time.Time { return now })
	password := "0123456789abcdef0123456789abcdef"
	if _, _, err := m.Register(Registration{ID: "123456789", TemporaryPass: password}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RevokePassword("123456789"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Register(Registration{ID: "123456789", TemporaryPass: password}); !errors.Is(err, ErrPasswordRevoke) {
		t.Fatalf("revoked device must not re-register, got %v", err)
	}

	// 释放后墓碑要继承 revoked，否则 revoke 会被过期或重启自动解除。
	now = now.Add(10 * time.Minute)
	m.RemoveExpiredOfflineDevices(time.Minute)
	if _, _, err := m.Register(Registration{ID: "123456789", TemporaryPass: password}); !errors.Is(err, ErrPasswordRevoke) {
		t.Fatalf("revoked tombstone must not be reclaimed, got %v", err)
	}
	if _, _, err := m.Register(Registration{ID: "123456789", TemporaryPass: "fedcba9876543210fedcba9876543210"}); !errors.Is(err, ErrPasswordRevoke) {
		t.Fatalf("revoked tombstone must not be reassigned, got %v", err)
	}
}

// 从未建立过会话的设备只是占槽，空闲超时后释放成墓碑；打过交道的设备不受影响。
func TestUnusedDevicesAreReleasedAndCanReclaim(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(4, time.Hour, func() time.Time { return now })
	unusedPassword := "0123456789abcdef0123456789abcdef"
	usedPassword := "fedcba9876543210fedcba9876543210"
	if _, _, err := m.Register(Registration{ID: "111111111", TemporaryPass: unusedPassword}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Register(Registration{ID: "222222222", TemporaryPass: usedPassword}); err != nil {
		t.Fatal(err)
	}
	m.AddSession("222222222", "session-1")
	m.RemoveSession("222222222", "session-1")

	now = now.Add(11 * time.Minute)
	if released := m.ReleaseUnusedDevices(10 * time.Minute); released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
	if _, ok := m.Get("111111111"); ok {
		t.Fatal("unused device should be released")
	}
	if _, ok := m.Get("222222222"); !ok {
		t.Fatal("device that hosted a session must be kept")
	}

	// 原持有者回来后仍然拿回自己的码。
	reclaimed, _, err := m.Register(Registration{ID: "111111111", TemporaryPass: unusedPassword})
	if err != nil {
		t.Fatalf("reclaim after unused release failed: %v", err)
	}
	if reclaimed.ID != "111111111" {
		t.Fatalf("reclaim returned %q, want the original id", reclaimed.ID)
	}
}

func TestReleaseUnusedDevicesIgnoresOnlineDevices(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(4, time.Hour, func() time.Time { return now })
	password := "0123456789abcdef0123456789abcdef"
	if _, _, err := m.Register(Registration{ID: "333333333", TemporaryPass: password}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetOnline("333333333", "conn-1"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if released := m.ReleaseUnusedDevices(time.Minute); released != 0 {
		t.Fatalf("online device must not be released, released = %d", released)
	}
	if _, ok := m.Get("333333333"); !ok {
		t.Fatal("online device disappeared")
	}
}

func TestOnlineOwnershipAndLimits(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(1, time.Minute, func() time.Time { return now })
	if _, _, err := m.Register(Registration{ID: "d1", TemporaryPass: "0123456789abcdef0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetOnline("d1", "conn-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Register(Registration{ID: "d1", TemporaryPass: "0123456789abcdef0123456789abcdef"}); !errors.Is(err, ErrDeviceOnline) {
		t.Fatalf("got %v, want ErrDeviceOnline", err)
	}
	if _, _, err := m.Register(Registration{ID: "d2", TemporaryPass: "0123456789abcdef0123456789abcdef"}); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("got %v, want ErrDeviceLimit", err)
	}
	if _, owned := m.SetOffline("d1", "conn-2"); owned {
		t.Fatal("non-owning connection must not take the device offline")
	}
	if _, owned := m.SetOffline("d1", "conn-1"); !owned {
		t.Fatal("owning connection should take the device offline")
	}
	if _, ok := m.OnlineConnection("d1"); ok {
		t.Fatal("device should be offline")
	}
}
