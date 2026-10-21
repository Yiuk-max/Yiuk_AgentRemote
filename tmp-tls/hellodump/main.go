package main

import (
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"time"
)

// helloDump listens once, reads the first TLS record (ClientHello) and prints it.
func main() {
	addr := "127.0.0.1:29600"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	defer ln.Close()
	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Second))
	conn, err := ln.Accept()
	if err != nil {
		fmt.Println("accept:", err)
		return
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		fmt.Println("read:", err)
		return
	}
	fmt.Printf("bytes=%d\n", n)
	fmt.Println(hex.EncodeToString(buf[:n]))
	// Cheap parse: record version, handshake type, SNI presence, versions.
	if n >= 11 {
		fmt.Printf("record_version=%02x%02x handshake_type=%02x\n", buf[1], buf[2], buf[5])
	}
	hasSNI := false
	for i := 0; i+9 < n; i++ {
		if buf[i] == 0x00 && buf[i+1] == 0x00 { // server_name extension id
			hasSNI = true
			break
		}
	}
	fmt.Printf("has_server_name_extension=%v\n", hasSNI)
}
