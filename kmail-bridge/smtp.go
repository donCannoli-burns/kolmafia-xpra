package main

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

type smtpBackend struct {
	bridge   *Bridge
	password string
}

func (be *smtpBackend) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &smtpSession{be: be}, nil
}

type smtpSession struct {
	be   *smtpBackend
	user string
	to   []string
}

func smtpErr(code int, enhanced smtp.EnhancedCode, msg string) error {
	return &smtp.SMTPError{Code: code, EnhancedCode: enhanced, Message: msg}
}

func (s *smtpSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *smtpSession) Auth(mech string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(identity, username, password string) error {
		if identity != "" && identity != username {
			return smtp.ErrAuthFailed
		}
		if !checkPassword(s.be.password, password) || !s.be.bridge.Known(username) {
			return smtp.ErrAuthFailed
		}
		s.user = username
		return nil
	}), nil
}

func (s *smtpSession) Mail(string, *smtp.MailOptions) error {
	if s.user == "" {
		return smtp.ErrAuthRequired
	}
	return nil
}

var recipientRe = regexp.MustCompile(`(?i)^([a-z0-9_ ]+)@` + regexp.QuoteMeta(domain) + `$`)

// Rcpt only accepts player addresses, so this can't be used to send mail anywhere else.
func (s *smtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	m := recipientRe.FindStringSubmatch(to)
	if m == nil {
		return smtpErr(550, smtp.EnhancedCode{5, 1, 1}, "Can only send to <player id>@"+domain)
	}
	// Otherwise a reply to a system kmail would go to a player called noreply, if there is one.
	if strings.EqualFold(m[1], noReply) || strings.Trim(m[1], "0") == "" {
		return smtpErr(550, smtp.EnhancedCode{5, 1, 1}, "KoL's own characters can't be kmailed")
	}
	s.to = append(s.to, m[1])
	return nil
}

func (s *smtpSession) Data(r io.Reader) error {
	text, err := OutgoingText(r)
	if err != nil {
		return smtpErr(554, smtp.EnhancedCode{5, 6, 0}, err.Error())
	}
	pwd, err := s.be.bridge.pwdFor(s.user)
	if err != nil {
		return smtpErr(451, smtp.EnhancedCode{4, 4, 0}, err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, to := range s.to {
		if err := s.be.bridge.kol.Send(ctx, pwd, to, text); err != nil {
			if errors.Is(err, errNotLoggedIn) {
				return smtpErr(451, smtp.EnhancedCode{4, 4, 0}, err.Error())
			}
			return smtpErr(554, smtp.EnhancedCode{5, 0, 0}, err.Error())
		}
	}
	return nil
}

func (s *smtpSession) Reset()        { s.to = nil }
func (s *smtpSession) Logout() error { return nil }

var (
	attributionRe = regexp.MustCompile(`^On .*wrote:\s*$`)
	replyPrefixRe = regexp.MustCompile(`(?i)^(re|fwd?|aw|sv):`)
)

// OutgoingText turns an email into kmail text: the plain-text body without quoted replies or a
// signature, led by the subject unless it's a reply to one of ours, which is made from the text.
func OutgoingText(r io.Reader) (string, error) {
	mr, err := mail.CreateReader(r)
	if err != nil && !message.IsUnknownCharset(err) {
		return "", err
	}
	var plain, html string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		} else if err != nil {
			return "", err
		}
		h, ok := p.Header.(*mail.InlineHeader)
		if !ok {
			continue
		}
		ct, params, _ := h.ContentType()
		b, err := io.ReadAll(p.Body)
		if err != nil {
			return "", err
		}
		switch {
		case ct == "text/plain" && plain == "":
			plain = string(b)
			if strings.EqualFold(params["format"], "flowed") {
				plain = unflow(plain, strings.EqualFold(params["delsp"], "yes"))
			}
		case ct == "text/html" && html == "":
			html = htmlText(string(b))
		}
	}
	text := plain
	if text == "" {
		text = html
	}
	text = stripReply(text)

	subj, _ := mr.Header.Subject()
	subj = strings.TrimSpace(subj)
	if subj != "" && !replyPrefixRe.MatchString(subj) && !strings.HasPrefix(text, subj) {
		text = strings.TrimSpace(subj + "\n\n" + text)
	}
	if text == "" {
		return "", errors.New("empty message")
	}
	return text, nil
}

func stripReply(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line == "-- " || attributionRe.MatchString(line) || strings.HasPrefix(line, "-----Original Message-----") {
			break
		}
		if strings.HasPrefix(line, ">") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// unflow joins RFC 3676 soft line breaks, which Apple Mail uses, so KoL gets whole paragraphs.
func unflow(text string, delSp bool) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, " ") {
			line = line[1:] // space-stuffing
		}
		if strings.HasSuffix(line, " ") && line != "-- " {
			if delSp {
				line = line[:len(line)-1]
			}
			b.WriteString(line)
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
