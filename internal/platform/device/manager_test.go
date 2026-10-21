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
