package main

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message/mail"
)

// Domain for player addresses. .internal is reserved (ICANN, 2024) and will never resolve, so a
// reply sent from the wrong account bounces instead of reaching someone.
const domain = "kingdomofloathing.internal"

// noReply is the sender for KoL's own characters (the Postal Service is 0, item vendors are -1),
// which can't be kmailed.
const noReply = "noreply"

func playerAddress(name string, id int) *mail.Address {
	if id <= 0 {
		return &mail.Address{Name: name, Address: noReply + "@" + domain}
	}
	return &mail.Address{Name: name, Address: fmt.Sprintf("%d@%s", id, domain)}
}

func RenderKmail(k Kmail, me *mail.Address) ([]byte, error) {
	p := ParseKmail(k)

	var h mail.Header
	h.SetDate(k.Time)
	h.SetAddressList("From", []*mail.Address{playerAddress(k.FromName, k.FromID)})
	h.SetAddressList("To", []*mail.Address{me})
	h.SetSubject(subject(p))
	h.SetMessageID(fmt.Sprintf("kmail-%d@%s", k.ID, domain))
	h.Set("X-KoL-Kmail-Id", strconv.FormatUint(uint64(k.ID), 10))
	if p.Meat > 0 {
		h.Set("X-KoL-Meat", strconv.Itoa(p.Meat))
	}
	h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})

	var buf bytes.Buffer
	w, err := mail.CreateSingleInlineWriter(&buf, h)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(body(p))); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// KoL has no subjects, so use the start of the message.
func subject(p ParsedKmail) string {
	for _, line := range strings.Split(p.Text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if utf8.RuneCountInString(line) > 72 {
				line = string([]rune(line)[:71]) + "…"
			}
			return line
		}
	}
	switch {
	case p.InsideNote != "" || len(p.Items) > 0 || p.Meat > 0:
		return "(package)"
	case p.Valentine != "":
		return "(valentine)"
	}
	return "(no message)"
}

func body(p ParsedKmail) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(p.Text, "\n"))
	b.WriteString("\n")
	if p.InsideNote != "" {
		b.WriteString("\nInside note:\n")
		b.WriteString(strings.TrimRight(p.InsideNote, "\n"))
		b.WriteString("\n")
	}
	if p.Valentine != "" || p.Meat > 0 || len(p.Items) > 0 {
		b.WriteString("\n--\n")
	}
	if p.Valentine != "" {
		fmt.Fprintf(&b, "Valentine: %s\n", p.Valentine)
	}
	if p.Meat > 0 {
		fmt.Fprintf(&b, "Meat: %s\n", commas(p.Meat))
	}
	for _, it := range p.Items {
		fmt.Fprintf(&b, "Item: %s × %s\n", commas(it.Quantity), it.Name)
	}
	return b.String()
}

func commas(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
