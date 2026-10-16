package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"usbshare/internal/proto"
	"usbshare/internal/tlsutil"
)

var clientName = func() string {
	if n := os.Getenv("COMPUTERNAME"); n != "" {
		return n
	}
	n, _ := os.Hostname()
	return n
}()

type dialResult struct {
	conn   *tls.Conn
	reply  proto.HelloReply
	seenFP string
}

// dialServer łączy się z serwerem USBShare, weryfikuje certyfikat i loguje hasłem.
func dialServer(e ServerEntry, mode string) (*dialResult, error) {
	port := e.Port
	if port == 0 {
		port = proto.DefaultPort
	}
	addr := net.JoinHostPort(e.Host, strconv.Itoa(port))
	res := &dialResult{}
	d := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 15 * time.Second}
	raw, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	if tc, ok := raw.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	conn := tls.Client(raw, tlsutil.ClientConfig(e.Fingerprint, &res.seenFP))
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := conn.Handshake(); err != nil {
		raw.Close()
		var mm *tlsutil.ErrFingerprintMismatch
		if errors.As(err, &mm) {
			return res, mm
		}
		return res, fmt.Errorf("TLS: %w", err)
	}
	hello := proto.Hello{V: proto.Version, Password: e.Password, Mode: mode, Client: clientName}
	if err := proto.WriteLine(conn, hello); err != nil {
		conn.Close()
		return res, err
	}
	if err := proto.ReadJSONUnbuffered(conn, &res.reply); err != nil {
		conn.Close()
		return res, fmt.Errorf("brak odpowiedzi serwera: %w", err)
	}
	if !res.reply.OK {
		conn.Close()
		return res, errors.New(res.reply.Error)
	}
	_ = conn.SetDeadline(time.Time{})
	res.conn = conn
	return res, nil
}

// controlCall wykonuje jedno polecenie w kanale sterującym.
func controlCall(e ServerEntry, req proto.Request) (*dialResult, *proto.Response, error) {
	res, err := dialServer(e, proto.ModeControl)
	if err != nil {
		return res, nil, err
	}
	defer res.conn.Close()
	_ = res.conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := proto.WriteLine(res.conn, req); err != nil {
		return res, nil, err
	}
	line, err := bufio.NewReader(res.conn).ReadBytes('\n')
	if err != nil {
		return res, nil, err
	}
	var resp proto.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return res, nil, err
	}
	if !resp.OK {
		return res, &resp, errors.New(resp.Error)
	}
	return res, &resp, nil
}
