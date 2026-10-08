package main

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"path"
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

// ImageFunc returns an item icon by file name, or nil if KoL doesn't have it.
type ImageFunc func(name string) ([]byte, error)

// attachment is an item or Meat received with a kmail. They're attached as their icons so mail
// clients show them along the bottom like the game does. They're for show: sending them on
// doesn't send anything.
type attachment struct {
	filename string
	image    string
	itemID   int
}

func attachments(p ParsedKmail) []attachment {
	var out []attachment
	if p.Meat > 0 {
		out = append(out, attachment{filename: commas(p.Meat) + " Meat", image: meatImage})
	}
	for _, it := range p.Items {
		name := strings.NewReplacer("/", "-", "\\", "-").Replace(it.Name)
		if it.Quantity != 1 {
			name = commas(it.Quantity) + " × " + name
		}
		out = append(out, attachment{filename: name, image: it.Image, itemID: it.ID})
	}
	return out
}

func RenderKmail(k Kmail, me *mail.Address, images ImageFunc) ([]byte, error) {
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

	var buf bytes.Buffer
	atts := attachments(p)
	if len(atts) == 0 {
		h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		w, err := mail.CreateSingleInlineWriter(&buf, h)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, body(p)); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	mw, err := mail.CreateWriter(&buf, h)
	if err != nil {
		return nil, err
	}
	var th mail.InlineHeader
	th.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
	tw, err := mw.CreateSingleInline(th)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(tw, body(p)); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}

	for _, a := range atts {
		var img []byte
		if a.image != "" {
			if img, err = images(a.image); err != nil {
				return nil, err
			}
		}
		var ah mail.AttachmentHeader
		if img != nil {
			ah.SetContentType(mime.TypeByExtension(path.Ext(a.image)), nil)
			ah.SetFilename(a.filename + path.Ext(a.image))
		} else {
			ah.SetContentType("application/octet-stream", nil)
			ah.SetFilename(a.filename)
		}
		if a.itemID != 0 {
			ah.Set("X-KoL-Item-Id", strconv.Itoa(a.itemID))
		}
		aw, err := mw.CreateAttachment(ah)
		if err != nil {
			return nil, err
		}
		if _, err := aw.Write(img); err != nil {
			return nil, err
		}
		if err := aw.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
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
