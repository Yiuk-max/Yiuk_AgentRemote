package transfer

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func encoded(data string) string { return base64.StdEncoding.EncodeToString([]byte(data)) }

func TestTransferEnforcesContiguousSequencesAndSize(t *testing.T) {
	m := NewManager(1)
	tr, created, err := m.Start("s1", "r1", "corr-1", 5, 0)
	if err != nil || !created || tr.RequestID != "r1" {
		t.Fatalf("start: %#v %v %v", tr, created, err)
	}
	if _, err := m.Append("s1", "r1", 2, encoded("a")); err != ErrSequence {
		t.Fatalf("out-of-order append = %v, want ErrSequence", err)
	}
	if _, err := m.Append("s1", "r1", 1, "not base64!"); err != ErrInvalidBase64 {
		t.Fatalf("invalid base64 = %v, want ErrInvalidBase64", err)
	}
	if _, err := m.Append("s1", "r1", 1, ""); err != ErrEmptyChunk {
		t.Fatalf("empty chunk = %v, want ErrEmptyChunk", err)
	}
	tr, err = m.Append("s1", "r1", 1, encoded("abc"))
	if err != nil || tr.Bytes != 3 || tr.NextSeq != 2 {
		t.Fatalf("append: %#v %v", tr, err)
	}
	tr, err = m.Append("s1", "r1", 2, encoded("def"))
	if err != ErrSizeLimit || tr.Status != StatusFailed {
		t.Fatalf("size limit append = %#v %v", tr, err)
	}
}

func TestTransferRejectsOversizedChunk(t *testing.T) {
	m := NewManager(1)
	if _, _, err := m.Start("s1", "r1", "", MaxChunkBytes*2, 0); err != nil {
		t.Fatal(err)
	}
	_, err := m.Append("s1", "r1", 1, encoded(strings.Repeat("x", MaxChunkBytes+1)))
	if err != ErrChunkTooLarge {
		t.Fatalf("got %v, want ErrChunkTooLarge", err)
	}
}

func TestTransferExpiresUnknown(t *testing.T) {
	now := time.Unix(100, 0)
	m := newManager(1, func() time.Time { return now })
	if _, _, err := m.Start("s1", "r1", "corr", 100, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if expired := m.Expire(); len(expired) != 0 {
		t.Fatalf("expired before deadline: %#v", expired)
	}
	now = now.Add(11 * time.Second)
	expired := m.Expire()
	if len(expired) != 1 || expired[0].Status != StatusResultUnknown || expired[0].ResultKnown {
		t.Fatalf("unexpected expired transfer: %#v", expired)
	}
}

func TestTransferResultSizeMismatchIsUnknown(t *testing.T) {
	m := NewManager(1)
	if _, _, err := m.Start("s1", "r1", "corr", 100, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Append("s1", "r1", 1, encoded("abc")); err != nil {
		t.Fatal(err)
	}
	tr, err := m.Finish("s1", "r1", StatusSucceeded, "a.txt", 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != StatusResultUnknown || tr.ResultKnown {
		t.Fatalf("mismatched size should be unknown: %#v", tr)
	}
}
