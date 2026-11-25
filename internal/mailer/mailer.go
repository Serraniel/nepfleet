// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mailer sends a mail through the fleet's SMTP server (fleet.yaml
// spec.mail), for the test in the app.
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

// Timeout bounds a whole send.
const Timeout = 30 * time.Second

// Message is a plain-text mail.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Send delivers msg through m, logging in with password when m has a user.
// starttls refuses a server that can't upgrade the connection; a password is
// never sent over a connection without TLS. Errors never contain password.
func Send(ctx context.Context, m fleet.Mail, password string, msg Message) error {
	err := send(ctx, m, password, msg)
	if err != nil && password != "" && strings.Contains(err.Error(), password) {
		return errors.New(strings.ReplaceAll(err.Error(), password, "••••••"))
	}
	return err
}

func send(ctx context.Context, m fleet.Mail, password string, msg Message) error {
	to, err := mail.ParseAddress(msg.To)
	if err != nil || strings.ContainsAny(msg.To, "\r\n") {
		return fmt.Errorf("%q is not a mail address", msg.To)
	}
	from := m.From
	if from == "" {
		from = m.User
	}
	sender, err := mail.ParseAddress(from)
	if err != nil {
		return errors.New("set a sender address (from) in the mail settings")
	}
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return errors.New("the subject must be one line")
	}
	tlsMode := m.TLS
	if tlsMode == "" {
		tlsMode = "starttls"
	}
	port := m.Port
	if port == 0 {
		port = fleet.DefaultMailPort(tlsMode)
	}
	host := strings.Trim(m.Host, "[]")
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	tlsConf := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	if tlsMode == "tls" {
		conn, err = (&tls.Dialer{Config: tlsConf}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", addr, err)
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("%s: %w", addr, err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello("localhost"); err != nil {
		return err
	}
	if tlsMode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%s doesn't offer STARTTLS; use tls (port 465) or none", addr)
		}
		if err := c.StartTLS(tlsConf); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if m.User != "" && password != "" {
		if tlsMode == "none" {
			return errors.New("a password is never sent without TLS: use starttls or tls")
		}
		if err := c.Auth(smtp.PlainAuth("", m.User, password, host)); err != nil {
			return fmt.Errorf("logging in as %s: %w", m.User, err)
		}
	}
	if err := c.Mail(sender.Address); err != nil {
		return fmt.Errorf("sender %s: %w", sender.Address, err)
	}
	if err := c.Rcpt(to.Address); err != nil {
		return fmt.Errorf("recipient %s: %w", to.Address, err)
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(compose(sender, to, msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// compose writes the message with CRLF line ends, as SMTP wants.
func compose(from, to *mail.Address, msg Message) []byte {
	var b strings.Builder
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "8bit")
	b.WriteString("\r\n")
	body := strings.ReplaceAll(msg.Body, "\r\n", "\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}
