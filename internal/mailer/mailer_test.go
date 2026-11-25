// SPDX-License-Identifier: AGPL-3.0-or-later

package mailer

import (
	"bufio"
	"context"
	"net"
	"net/textproto"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

// fakeSMTP answers one session in plain text and returns what it received.
func fakeSMTP(t *testing.T, starttls bool) (port int, got <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			ch <- ""
			return
		}
		defer func() { _ = conn.Close() }()
		tp := textproto.NewConn(conn)
		var b strings.Builder
		_ = tp.PrintfLine("220 fake")
		for {
			line, err := tp.ReadLine()
			if err != nil {
				ch <- b.String()
				return
			}
			b.WriteString(line + "\n")
			switch cmd := strings.ToUpper(strings.Fields(line + " x")[0]); cmd {
			case "EHLO":
				if starttls {
					_ = tp.PrintfLine("250-fake\r\n250 STARTTLS")
				} else {
					_ = tp.PrintfLine("250 fake")
				}
			case "DATA":
				_ = tp.PrintfLine("354 go on")
				data, _ := tp.ReadDotBytes()
				b.Write(data)
				_ = tp.PrintfLine("250 queued")
			case "QUIT":
				_ = tp.PrintfLine("221 bye")
				ch <- b.String()
				return
			default:
				_ = tp.PrintfLine("250 ok")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, ch
}

func TestSendPlain(t *testing.T) {
	port, got := fakeSMTP(t, false)
	m := fleet.Mail{Host: "127.0.0.1", Port: port, From: "Fleet <fleet@example.test>", TLS: "none"}
	err := Send(context.Background(), m, "", Message{To: "admin@example.test", Subject: "NepFleet test mail", Body: "hello\n.dot line\n"})
	if err != nil {
		t.Fatal(err)
	}
	s := <-got
	for _, want := range []string{"MAIL FROM:<fleet@example.test>", "RCPT TO:<admin@example.test>", "Subject: NepFleet test mail", "hello", ".dot line"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func TestSendRefuses(t *testing.T) {
	ctx := context.Background()
	// A password never goes over a plain connection.
	port, _ := fakeSMTP(t, false)
	m := fleet.Mail{Host: "127.0.0.1", Port: port, User: "u", From: "fleet@example.test", TLS: "none"}
	if err := Send(ctx, m, "pw-secret", Message{To: "a@example.test"}); err == nil || !strings.Contains(err.Error(), "without TLS") || strings.Contains(err.Error(), "pw-secret") {
		t.Errorf("none with a password: %v", err)
	}
	// starttls needs the server to offer it.
	port, _ = fakeSMTP(t, false)
	m = fleet.Mail{Host: "127.0.0.1", Port: port, From: "fleet@example.test", TLS: "starttls"}
	if err := Send(ctx, m, "", Message{To: "a@example.test"}); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("starttls not offered: %v", err)
	}
	// Header injection through the recipient.
	if err := Send(ctx, m, "", Message{To: "a@example.test\r\nBcc: b@example.test"}); err == nil {
		t.Error("a recipient with a line break was accepted")
	}
	// No sender.
	if err := Send(ctx, fleet.Mail{Host: "127.0.0.1", Port: 1}, "", Message{To: "a@example.test"}); err == nil || !strings.Contains(err.Error(), "sender") {
		t.Errorf("no sender: %v", err)
	}
}

func TestErrorsHidePassword(t *testing.T) {
	// A server that echoes what it gets; the password must not come back.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write([]byte("554 no pw-secret for you\r\n"))
		_, _ = bufio.NewReader(conn).ReadString('\n')
	}()
	m := fleet.Mail{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: "u", From: "f@example.test", TLS: "none"}
	err = Send(context.Background(), m, "pw-secret", Message{To: "a@example.test"})
	if err == nil || strings.Contains(err.Error(), "pw-secret") {
		t.Errorf("err = %v", err)
	}
}
