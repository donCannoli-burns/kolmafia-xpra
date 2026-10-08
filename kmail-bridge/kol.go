package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	nethtml "golang.org/x/net/html"
)

// Everything this program knows about KoL's pages lives in this file. Requests go through the
// KoLmafia relay, which adds the session cookie for whoever KoLmafia is logged in as.

var errNotLoggedIn = errors.New("KoLmafia relay is not reachable (not logged in?)")

type KoL struct {
	base   string
	images string
	client *http.Client
}

func NewKoL(base string) *KoL {
	return &KoL{
		base:   strings.TrimSuffix(base, "/"),
		images: "https://images.kingdomofloathing.com/itemimages/",
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

const meatImage = "meat.gif"

var (
	imageNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.(?:gif|png|jpg)$`)
	errNoImage  = errors.New("no such image")
)

// ItemImage fetches an item's icon from KoL's public image server, not through the relay.
func (k *KoL) ItemImage(ctx context.Context, name string) ([]byte, error) {
	if !imageNameRe.MatchString(name) {
		return nil, errNoImage
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.images+name, nil)
	if err != nil {
		return nil, err
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden:
		return nil, errNoImage
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s: HTTP %d", name, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

type Status struct {
	Name     string `json:"name"`
	PlayerID string `json:"playerid"`
	Pwd      string `json:"pwd"`
}

type Kmail struct {
	ID       uint32
	Type     string
	FromID   int
	FromName string
	Time     time.Time
	Message  string
}

type rawKmail struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	FromID     string `json:"fromid"`
	FromName   string `json:"fromname"`
	AzUnixTime string `json:"azunixtime"`
	Message    string `json:"message"`
}

func (k *KoL) do(ctx context.Context, method, path string, form url.Values) (string, error) {
	u := k.base + "/" + path
	var body io.Reader
	if method == http.MethodGet && form != nil {
		u += "?" + form.Encode()
	} else if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := k.client.Do(req)
	if err != nil {
		var opErr interface{ Timeout() bool }
		if errors.As(err, &opErr) && opErr.Timeout() {
			return "", err
		}
		return "", fmt.Errorf("%w: %v", errNotLoggedIn, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}
	return string(b), nil
}

func (k *KoL) Status(ctx context.Context) (*Status, error) {
	body, err := k.do(ctx, http.MethodGet, "api.php", url.Values{"what": {"status"}, "for": {"kmail-bridge"}})
	if err != nil {
		return nil, err
	}
	var s Status
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		return nil, fmt.Errorf("api.php status: %w", err)
	}
	if s.Name == "" || s.Pwd == "" {
		return nil, errNotLoggedIn
	}
	return &s, nil
}

// Kmails returns up to count of the newest inbox messages, newest first. api.php won't return
// more than 100.
func (k *KoL) Kmails(ctx context.Context, count int) ([]Kmail, error) {
	body, err := k.do(ctx, http.MethodGet, "api.php", url.Values{
		"what": {"kmail"}, "for": {"kmail-bridge"}, "count": {strconv.Itoa(count)},
	})
	if err != nil {
		return nil, err
	}
	var raw []rawKmail
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, fmt.Errorf("api.php kmail: %w", err)
	}
	out := make([]Kmail, 0, len(raw))
	for _, r := range raw {
		id, err := strconv.ParseUint(r.ID, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("api.php kmail: bad id %q", r.ID)
		}
		fromID, _ := strconv.Atoi(r.FromID)
		ts, _ := strconv.ParseInt(r.AzUnixTime, 10, 64)
		out = append(out, Kmail{
			ID: uint32(id), Type: r.Type, FromID: fromID, FromName: html.UnescapeString(r.FromName),
			Time: time.Unix(ts, 0), Message: r.Message,
		})
	}
	return out, nil
}

var deletedRe = regexp.MustCompile(`(\d+) messages? deleted\.`)

func (k *KoL) Delete(ctx context.Context, pwd string, ids []uint32) error {
	if len(ids) == 0 {
		return nil
	}
	form := url.Values{"the_action": {"delete"}, "box": {"Inbox"}, "pwd": {pwd}}
	for _, id := range ids {
		form.Set(fmt.Sprintf("sel%d", id), "on")
	}
	body, err := k.do(ctx, http.MethodPost, "messages.php", form)
	if err != nil {
		return err
	}
	m := deletedRe.FindStringSubmatch(body)
	if m == nil || m[1] != strconv.Itoa(len(ids)) {
		return fmt.Errorf("KoL did not confirm deleting %d kmails", len(ids))
	}
	return nil
}

// Send sends a plain kmail. towho can be a player name or ID.
func (k *KoL) Send(ctx context.Context, pwd, towho, message string) error {
	body, err := k.do(ctx, http.MethodPost, "sendmessage.php", url.Values{
		"action": {"send"}, "pwd": {pwd}, "towho": {towho}, "message": {message},
		"savecopy": {"on"}, "sendmeat": {"0"},
	})
	if err != nil {
		return err
	}
	if strings.Contains(body, "Message sent.") {
		return nil
	}
	if msg := resultText(body); msg != "" {
		return fmt.Errorf("KoL: %s", msg)
	}
	return errors.New("KoL did not confirm the kmail was sent")
}

// resultText pulls the first line of text out of KoL's "Results:" box, which is where errors go.
func resultText(page string) string {
	_, after, ok := strings.Cut(page, "Results:")
	if !ok {
		return ""
	}
	for _, line := range strings.Split(htmlText(after), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

type KmailItem struct {
	ID       int
	Name     string
	Quantity int
	Image    string // file name under itemimages/
}

type ParsedKmail struct {
	Text       string
	InsideNote string
	Valentine  string
	Items      []KmailItem
	Meat       int
}

var (
	meatRe       = regexp.MustCompile(`You (?:gain|acquire) ([\d,]+) Meat`)
	valentineRe  = regexp.MustCompile(`adventureimages/(\w+)\.\w+`)
	attachmentRe = regexp.MustCompile(`<center>(?:<table class="item"|You )`)
	breakRe      = regexp.MustCompile(`(?i)<br\s*/?>`)
	paragraphRe  = regexp.MustCompile(`(?i)<p\s*/?>`)
	tagRe        = regexp.MustCompile(`<[^>]*>`)
)

// noteText turns a kmail note into plain text. Players' notes have no tags (KoL escapes any <
// they type), but system kmails like the Postal Service's use <p> and <br>.
func noteText(s string) string {
	s = breakRe.ReplaceAllString(s, "\n")
	s = paragraphRe.ReplaceAllString(s, "\n\n")
	return html.UnescapeString(tagRe.ReplaceAllString(s, ""))
}

// ParseKmail is based on KmailMailbox.parse in kol.js, but splits off attachments where they
// start rather than at the first tag, so system kmails with formatting keep their whole note.
func ParseKmail(k Kmail) ParsedKmail {
	var p ParsedKmail
	text := k.Message
	inside, hasInside := "", false

	switch k.Type {
	case "normal":
		if strings.HasPrefix(text, "<center><table>") {
			if end := strings.Index(text, "</center>"); end >= 0 {
				p.Valentine = "unknown"
				if m := valentineRe.FindStringSubmatch(text[:end]); m != nil {
					p.Valentine = m[1]
				}
				text = text[end+len("</center>"):]
			}
		}
	case "giftshop":
		text, inside, hasInside = strings.Cut(text, "<p>Inside Note:<p>")
	}

	split := func(s string) (string, string) {
		if loc := attachmentRe.FindStringIndex(s); loc != nil {
			return s[:loc[0]], s[loc[0]:]
		}
		return s, ""
	}
	note, attachments := split(text)
	p.Text = noteText(note)
	if hasInside {
		in, more := split(inside)
		p.InsideNote = noteText(in)
		attachments += more
	}

	if attachments != "" {
		p.Items = parseItems(attachments)
		if m := meatRe.FindStringSubmatch(attachments); m != nil {
			p.Meat, _ = strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
		}
	}
	return p
}

func parseItems(fragment string) []KmailItem {
	doc, err := nethtml.Parse(strings.NewReader(fragment))
	if err != nil {
		return nil
	}
	var items []KmailItem
	var walk func(*nethtml.Node, *KmailItem)
	walk = func(n *nethtml.Node, cur *KmailItem) {
		if n.Type == nethtml.ElementNode && n.Data == "table" && hasClass(n, "item") {
			it := KmailItem{Quantity: 1}
			if q, err := url.ParseQuery(attr(n, "rel")); err == nil {
				it.ID, _ = strconv.Atoi(q.Get("id"))
				if v, err := strconv.Atoi(q.Get("n")); err == nil {
					it.Quantity = v
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c, &it)
			}
			items = append(items, it)
			return
		}
		if n.Type == nethtml.ElementNode && n.Data == "img" && cur != nil && cur.Name == "" {
			cur.Name = attr(n, "title")
			if _, file, ok := strings.Cut(attr(n, "src"), "/itemimages/"); ok && imageNameRe.MatchString(file) {
				cur.Image = file
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, cur)
		}
	}
	walk(doc, nil)
	return items
}

func attr(n *nethtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *nethtml.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

func htmlText(fragment string) string {
	doc, err := nethtml.Parse(strings.NewReader(fragment))
	if err != nil {
		return fragment
	}
	var b strings.Builder
	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		if n.Type == nethtml.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return b.String()
}
