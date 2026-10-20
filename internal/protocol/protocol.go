// Package protocol implements the length-delimited Protocol Buffers control
// protocol. The field numbers are defined in proto/switchboard.proto.
package protocol

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const MaxMessageBytes = 64 << 10

var ErrMessageTooLong = errors.New("protocol message exceeds 64 KiB")

type Mod struct {
	ID  string
	Ver string
}
type RoomSummary struct {
	ID       string
	Name     string
	Host     string
	Port     int
	Players  int
	Max      int
	MC       string
	Loader   string
	ModCount int
}
type Message struct {
	T         string
	PW        string
	Name      string
	Ver       int
	Max       int
	MC        string
	Loader    string
	Mods      []Mod
	ID        string
	SID       string
	Token     string
	Port      int
	LocalPort int
	Code      string
	Msg       string
	List      []RoomSummary
}

var types = []string{"", "auth", "data", "ok", "err", "ping", "pong", "list", "rooms", "create", "created", "room_mods", "mods", "close", "resume", "new_session"}

func typeNumber(s string) (uint64, error) {
	for i, v := range types {
		if v == s && i > 0 {
			return uint64(i), nil
		}
	}
	return 0, fmt.Errorf("unknown message type %q", s)
}
func typeName(n uint64) (string, error) {
	if n == 0 || n >= uint64(len(types)) {
		return "", fmt.Errorf("unknown message type %d", n)
	}
	return types[n], nil
}

func putVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}
func putBytes(dst []byte, field uint64, b []byte) []byte {
	dst = putVarint(dst, field<<3|2)
	dst = putVarint(dst, uint64(len(b)))
	return append(dst, b...)
}
func putString(dst []byte, field uint64, s string) []byte {
	if s == "" {
		return dst
	}
	return putBytes(dst, field, []byte(s))
}
func putInt(dst []byte, field uint64, n int) []byte {
	if n == 0 {
		return dst
	}
	dst = putVarint(dst, field<<3)
	return putVarint(dst, uint64(n))
}
func encodeMod(m Mod) []byte { var b []byte; b = putString(b, 1, m.ID); return putString(b, 2, m.Ver) }
func encodeRoom(r RoomSummary) []byte {
	var b []byte
	b = putString(b, 1, r.ID)
	b = putString(b, 2, r.Name)
	b = putString(b, 3, r.Host)
	b = putInt(b, 4, r.Port)
	b = putInt(b, 5, r.Players)
	b = putInt(b, 6, r.Max)
	b = putString(b, 7, r.MC)
	b = putString(b, 8, r.Loader)
	b = putInt(b, 9, r.ModCount)
	return b
}
func marshal(m Message) ([]byte, error) { //to set the message type and marshal the message to protobuf format
	t, err := typeNumber(m.T)
	if err != nil {
		return nil, err
	}
	b := putVarint(nil, 8)
	b = putVarint(b, t)
	b = putString(b, 2, m.PW)
	b = putString(b, 3, m.Name)
	b = putInt(b, 4, m.Ver)
	b = putInt(b, 5, m.Max)
	b = putString(b, 6, m.MC)
	b = putString(b, 7, m.Loader)
	for _, v := range m.Mods {
		b = putBytes(b, 8, encodeMod(v))
	}
	b = putString(b, 9, m.ID)
	b = putString(b, 10, m.SID)
	b = putString(b, 11, m.Token)
	b = putInt(b, 12, m.Port)
	b = putInt(b, 13, m.LocalPort)
	b = putString(b, 14, m.Code)
	b = putString(b, 15, m.Msg)
	for _, v := range m.List {
		b = putBytes(b, 16, encodeRoom(v))
	}
	return b, nil
}

type field struct {
	n    uint64
	wire uint64
	v    uint64
	b    []byte
}

func fields(data []byte, fn func(field) error) error {
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 {
			return errors.New("invalid protobuf tag")
		}
		data = data[n:]
		f := field{n: tag >> 3, wire: tag & 7}
		if f.n == 0 {
			return errors.New("invalid protobuf field number")
		}
		switch f.wire {
		case 0:
			v, k := binary.Uvarint(data)
			if k <= 0 {
				return errors.New("invalid protobuf varint")
			}
			f.v = v
			data = data[k:]
		case 2:
			l, k := binary.Uvarint(data)
			if k <= 0 || l > uint64(len(data)-k) {
				return errors.New("invalid protobuf length")
			}
			data = data[k:]
			f.b = data[:l]
			data = data[l:]
		case 1:
			if len(data) < 8 {
				return errors.New("truncated protobuf fixed64")
			}
			data = data[8:]
		case 5:
			if len(data) < 4 {
				return errors.New("truncated protobuf fixed32")
			}
			data = data[4:]
		default:
			return fmt.Errorf("unsupported protobuf wire type %d", f.wire)
		}
		if err := fn(f); err != nil {
			return err
		}
	}
	return nil
}
func str(f field) string { return string(f.b) }
func parseMod(data []byte) (m Mod, err error) {
	err = fields(data, func(f field) error {
		if f.wire != 2 {
			return errors.New("invalid mod field")
		}
		switch f.n {
		case 1:
			m.ID = str(f)
		case 2:
			m.Ver = str(f)
		}
		return nil
	})
	return
}
func parseRoom(data []byte) (r RoomSummary, err error) {
	err = fields(data, func(f field) error {
		switch f.n {
		case 1:
			r.ID = str(f)
		case 2:
			r.Name = str(f)
		case 3:
			r.Host = str(f)
		case 4:
			r.Port = int(f.v)
		case 5:
			r.Players = int(f.v)
		case 6:
			r.Max = int(f.v)
		case 7:
			r.MC = str(f)
		case 8:
			r.Loader = str(f)
		case 9:
			r.ModCount = int(f.v)
		}
		return nil
	})
	return
}
func unmarshal(data []byte) (m Message, err error) {
	err = fields(data, func(f field) error {
		switch f.n {
		case 1:
			if f.wire != 0 {
				return errors.New("invalid message type")
			}
			m.T, err = typeName(f.v)
			return err
		case 2:
			m.PW = str(f)
		case 3:
			m.Name = str(f)
		case 4:
			m.Ver = int(f.v)
		case 5:
			m.Max = int(f.v)
		case 6:
			m.MC = str(f)
		case 7:
			m.Loader = str(f)
		case 8:
			v, e := parseMod(f.b)
			if e != nil {
				return e
			}
			m.Mods = append(m.Mods, v)
		case 9:
			m.ID = str(f)
		case 10:
			m.SID = str(f)
		case 11:
			m.Token = str(f)
		case 12:
			m.Port = int(f.v)
		case 13:
			m.LocalPort = int(f.v)
		case 14:
			m.Code = str(f)
		case 15:
			m.Msg = str(f)
		case 16:
			v, e := parseRoom(f.b)
			if e != nil {
				return e
			}
			m.List = append(m.List, v)
		}
		return nil
	})
	if err == nil && m.T == "" {
		err = errors.New("missing message type")
	}
	return
}

// ReadFrame consumes a standard protobuf varint-length-delimited message.
func ReadFrame(r *bufio.Reader) (Message, error) {
	var m Message
	length, err := binary.ReadUvarint(r)
	if err != nil {
		return m, err
	}
	if length > MaxMessageBytes {
		return m, ErrMessageTooLong
	}
	data := make([]byte, int(length))
	if _, err = io.ReadFull(r, data); err != nil {
		return m, err
	}
	m, err = unmarshal(data)
	if err != nil {
		return Message{}, fmt.Errorf("invalid protobuf message: %w", err)
	}
	return m, nil
}
func Write(w io.Writer, m Message) error {
	data, err := marshal(m)
	if err != nil {
		return err
	}
	if len(data) > MaxMessageBytes {
		return ErrMessageTooLong
	}
	// Write the length prefix and the message data in a single write to avoid interleaving with other writes.
	frame := putVarint(nil, uint64(len(data)))
	// Append the message data to the frame.
	frame = append(frame, data...)
	//make sure the entire frame is written, even if the writer only accepts a partial write.
	for len(frame) > 0 {
		n, e := w.Write(frame)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}
