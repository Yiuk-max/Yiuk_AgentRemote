package session

import (
	"io"
	"net"
	"time"
)

// Pipe copies both directions and preserves TCP half-closes. Each direction may
// be a buffered reader to preserve protocol prefetch.
func Pipe(a net.Conn, aReader io.Reader, b net.Conn, bReader io.Reader, idleTimeout time.Duration) error {
	if aReader == nil {
		aReader = a
	}
	if bReader == nil {
		bReader = b
	}
	aReader = idleReader{reader: aReader, conn: a, timeout: idleTimeout}
	bReader = idleReader{reader: bReader, conn: b, timeout: idleTimeout}
	type copyResult struct {
		destination net.Conn
		err         error
	}
	done := make(chan copyResult, 2)
	go func() { _, err := io.Copy(a, bReader); done <- copyResult{destination: a, err: err} }()
	go func() { _, err := io.Copy(b, aReader); done <- copyResult{destination: b, err: err} }()

	var firstErr error
	for range 2 {
		result := <-done
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			// An I/O error (including an idle timeout) makes the session unusable.
			_ = a.Close()
			_ = b.Close()
			continue
		}
		if firstErr == nil {
			if err := closeWrite(result.destination); err != nil {
				firstErr = err
				_ = a.Close()
				_ = b.Close()
			}
		}
	}
	_ = a.Close()
	_ = b.Close()
	return firstErr
}

func closeWrite(conn net.Conn) error {
	if halfCloser, ok := conn.(interface{ CloseWrite() error }); ok {
		return halfCloser.CloseWrite()
	}
	return conn.Close()
}

type idleReader struct {
	reader  io.Reader
	conn    net.Conn
	timeout time.Duration //if there are no data for a while, close the connection,to clean the conn that has lost connection but not closed by the TCP layer
}

func (r idleReader) Read(p []byte) (int, error) {
	if r.timeout > 0 {
		_ = r.conn.SetReadDeadline(time.Now().Add(r.timeout))
	}
	return r.reader.Read(p)
}
