package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// fakeRelay stands in for the KoLmafia relay.
type fakeRelay struct {
	mu     sync.Mutex
	inbox  []rawKmail // newest first, like api.php
	sent   []string
	nextID int
}

func (f *fakeRelay) receive(message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.inbox = append([]rawKmail{{
		ID: fmt.Sprint(f.nextID), Type: "normal", FromID: "456", FromName: "Bob &amp; Co",
		AzUnixTime: fmt.Sprint(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC).Unix()), Message: message,
	}}, f.inbox...)
}

func (f *fakeRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r.ParseForm()
	switch {
	case r.URL.Path == "/api.php" && r.Form.Get("what") == "status":
		json.NewEncoder(w).Encode(Status{Name: "Alice", PlayerID: "123", Pwd: "hash"})
	case r.URL.Path == "/api.php" && r.Form.Get("what") == "kmail":
		json.NewEncoder(w).Encode(f.inbox)
	case r.URL.Path == "/messages.php" && r.Form.Get("pwd") == "hash":
		n := 0
		f.inbox = slices.DeleteFunc(f.inbox, func(k rawKmail) bool {
			if r.Form.Get("sel"+k.ID) == "on" {
				n++
				return true
			}
			return false
		})
		fmt.Fprintf(w, "<td>%d message%s deleted.</td>", n, map[bool]string{true: "", false: "s"}[n == 1])
	case r.URL.Path == "/sendmessage.php" && r.Form.Get("pwd") == "hash":
		f.sent = append(f.sent, r.Form.Get("towho")+": "+r.Form.Get("message"))
		io.WriteString(w, "<b>Results:</b> Message sent.")
	default:
		http.Error(w, "unexpected", http.StatusBadRequest)
	}
}

// fakeImages stands in for KoL's image server.
type fakeImages struct {
	mu   sync.Mutex
	down bool
	hits int
}

func (f *fakeImages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits++
	switch {
	case f.down:
		http.Error(w, "down", http.StatusBadGateway)
	case r.URL.Path == "/toast.gif":
		io.WriteString(w, "TOAST")
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeImages) set(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

type harness struct {
	relay    *fakeRelay
	images   *fakeImages
	bridge   *Bridge
	imapAddr string
	smtpAddr string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if err := ensureSelfSigned(cert, key, "localhost"); err != nil {
		t.Fatal(err)
	}
	tlsConfig := &tls.Config{GetCertificate: (&certLoader{cert: cert, key: key}).get}

	relay := &fakeRelay{}
	srv := httptest.NewServer(relay)
	t.Cleanup(srv.Close)
	images := &fakeImages{}
	imgSrv := httptest.NewServer(images)
	t.Cleanup(imgSrv.Close)
	kol := NewKoL(srv.URL)
	kol.images = imgSrv.URL + "/"
	bridge := NewBridge(kol, dir)

	listen := func() net.Listener {
		ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
		if err != nil {
			t.Fatal(err)
		}
		return ln
	}
	imapLn, smtpLn := listen(), listen()
	imapSrv, smtpSrv := newIMAPServer(bridge, "secret"), newSMTPServer(bridge, "secret")
	go imapSrv.Serve(imapLn)
	go smtpSrv.Serve(smtpLn)
	t.Cleanup(func() { imapSrv.Close(); smtpSrv.Close() })

	return &harness{relay: relay, images: images, bridge: bridge, imapAddr: imapLn.Addr().String(), smtpAddr: smtpLn.Addr().String()}
}

func (h *harness) sync(t *testing.T) {
	t.Helper()
	if err := h.bridge.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) imap(t *testing.T) *imapclient.Client {
	t.Helper()
	c, err := imapclient.DialTLS(h.imapAddr, &imapclient.Options{TLSConfig: &tls.Config{InsecureSkipVerify: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Login("alice", "secret").Wait(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoginRejectsUnknownCharacterAndBadPassword(t *testing.T) {
	h := newHarness(t)
	h.sync(t)
	for _, creds := range [][2]string{{"alice", "wrong"}, {"mallory", "secret"}, {"../alice", "secret"}} {
		c, err := imapclient.DialTLS(h.imapAddr, &imapclient.Options{TLSConfig: &tls.Config{InsecureSkipVerify: true}})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Login(creds[0], creds[1]).Wait(); err == nil {
			t.Errorf("login as %v succeeded", creds)
		}
		c.Close()
	}
}

func TestReadKmailOverIMAP(t *testing.T) {
	h := newHarness(t)
	h.relay.receive("Want some toast?")
	h.relay.receive("Hello again")
	h.sync(t)

	c := h.imap(t)
	sel, err := c.Select("INBOX", nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
	if sel.NumMessages != 2 || sel.UIDValidity != 1 || sel.UIDNext != 3 {
		t.Fatalf("select: %+v", sel)
	}

	msgs, err := c.Fetch(imap.UIDSetNum(1), &imap.FetchOptions{
		Envelope: true, Flags: true, BodySection: []*imap.FetchItemBodySection{{}},
	}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	m := msgs[0]
	if m.Envelope.Subject != "Want some toast?" {
		t.Errorf("subject %q", m.Envelope.Subject)
	}
	if from := m.Envelope.From[0]; from.Name != "Bob & Co" || from.Addr() != "456@kingdomofloathing.internal" {
		t.Errorf("from %+v", from)
	}
	if body := string(m.FindBodySection(&imap.FetchItemBodySection{})); !strings.Contains(body, "Want some toast?") {
		t.Errorf("body %q", body)
	}

	// Reading it marked it seen, and that survives a restart.
	h.bridge = NewBridge(h.bridge.kol, h.bridge.dir)
	a, err := h.bridge.Account("Alice")
	if err != nil {
		t.Fatal(err)
	}
	if inbox, _ := a.mailbox("INBOX"); !inbox.Messages[0].hasFlag(imap.FlagSeen) || inbox.Messages[1].hasFlag(imap.FlagSeen) {
		t.Errorf("flags after reload: %v, %v", inbox.Messages[0].Flags, inbox.Messages[1].Flags)
	}
}

func TestNewKmailReachesIdleClient(t *testing.T) {
	h := newHarness(t)
	h.sync(t)

	exists := make(chan uint32, 4)
	c, err := imapclient.DialTLS(h.imapAddr, &imapclient.Options{
		TLSConfig: &tls.Config{InsecureSkipVerify: true},
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				if data.NumMessages != nil {
					exists <- *data.NumMessages
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login("Alice", "secret").Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	idle, err := c.Idle()
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()

	h.relay.receive("ping")
	h.sync(t)
	select {
	case n := <-exists:
		if n != 1 {
			t.Errorf("EXISTS %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no EXISTS while idling")
	}
}

func TestDeleteGoesToKoL(t *testing.T) {
	h := newHarness(t)
	h.relay.receive("one")
	h.relay.receive("two")
	h.sync(t)

	c := h.imap(t)
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	// Mail clients usually delete by moving to Trash.
	if _, err := c.Move(imap.UIDSetNum(1), "Trash").Wait(); err != nil {
		t.Fatal(err)
	}
	if len(h.relay.inbox) != 1 || h.relay.inbox[0].ID != "2" {
		t.Fatalf("KoL inbox: %+v", h.relay.inbox)
	}
	st, err := c.Status("Trash", &imap.StatusOptions{NumMessages: true}).Wait()
	if err != nil || *st.NumMessages != 1 {
		t.Fatalf("Trash: %v %v", st, err)
	}

	// Deleted in-game shows up as an expunge.
	h.relay.mu.Lock()
	h.relay.inbox = nil
	h.relay.mu.Unlock()
	h.sync(t)
	if err := c.Noop().Wait(); err != nil {
		t.Fatal(err)
	}
	st, err = c.Status("INBOX", &imap.StatusOptions{NumMessages: true}).Wait()
	if err != nil || *st.NumMessages != 0 {
		t.Fatalf("INBOX: %v %v", st, err)
	}
}

func TestSendOverSMTP(t *testing.T) {
	h := newHarness(t)
	h.sync(t)

	c, err := smtp.DialTLS(h.smtpAddr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Auth(sasl.NewPlainClient("", "Alice", "secret")); err != nil {
		t.Fatal(err)
	}
	if err := c.Mail("123@kingdomofloathing.internal", nil); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"someone@example.com", "noreply@kingdomofloathing.internal", "0@kingdomofloathing.internal"} {
		if err := c.Rcpt(to, nil); err == nil {
			t.Errorf("accepted %s", to)
		}
	}
	if err := c.Rcpt("456@kingdomofloathing.internal", nil); err != nil {
		t.Fatal(err)
	}
	w, err := c.Data()
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, "Subject: Re: Want some toast?\r\n\r\nYes please\r\n\r\nOn Thu, Bob wrote:\r\n> Want some toast?\r\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"456: Yes please"}; !slices.Equal(h.relay.sent, want) {
		t.Errorf("sent %q, want %q", h.relay.sent, want)
	}
}

const toastKmail = `Here<center><table class="item" style="float: none" rel="id=641&s=14&q=0&d=1&g=0&t=1&n=2&m=0&p=0&u=e"><tr><td><img src="https://images.kingdomofloathing.com/itemimages/toast.gif" alt="toast" title="toast" class=hand ></td><td valign=center class=effect>You acquire <b>2 toasts</b></td></tr></table></center>`

func TestItemsArriveAsAttachments(t *testing.T) {
	h := newHarness(t)
	h.relay.receive(toastKmail)
	h.relay.receive(toastKmail)
	h.sync(t)
	if h.images.hits != 1 {
		t.Errorf("fetched the icon %d times, want once", h.images.hits)
	}

	c := h.imap(t)
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	msgs, err := c.Fetch(imap.UIDSetNum(1), &imap.FetchOptions{BodyStructure: &imap.FetchItemBodyStructure{Extended: true}}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	msgs[0].BodyStructure.Walk(func(_ []int, part imap.BodyStructure) bool {
		if sp, ok := part.(*imap.BodyStructureSinglePart); ok && sp.Filename() != "" {
			files = append(files, sp.MediaType()+" "+sp.Filename())
		}
		return true
	})
	if want := []string{"image/gif 2 × toast.gif"}; !slices.Equal(files, want) {
		t.Errorf("attachments %q, want %q", files, want)
	}
}

func TestIconOutageHoldsKmailsInOrder(t *testing.T) {
	h := newHarness(t)
	h.relay.receive("first")
	h.relay.receive(toastKmail)
	h.relay.receive("third")
	h.images.set(true)
	if err := h.bridge.Sync(context.Background()); err == nil {
		t.Error("sync succeeded with the image server down")
	}

	c := h.imap(t)
	st, err := c.Status("INBOX", &imap.StatusOptions{NumMessages: true, UIDNext: true}).Wait()
	if err != nil {
		t.Fatal(err)
	}
	// "third" waits too, or its UID would put the toast kmail out of order.
	if *st.NumMessages != 1 || st.UIDNext != 2 {
		t.Fatalf("during outage: %d messages, UIDNEXT %d", *st.NumMessages, st.UIDNext)
	}

	h.images.set(false)
	h.sync(t)
	st, err = c.Status("INBOX", &imap.StatusOptions{NumMessages: true}).Wait()
	if err != nil || *st.NumMessages != 3 {
		t.Fatalf("after outage: %v %v", st, err)
	}
}

func TestSendRefusesAttachments(t *testing.T) {
	email := "Subject: Fwd: Here\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nHave some toast\r\n" +
		"--b\r\nContent-Type: image/gif\r\nContent-Disposition: attachment; filename=\"2 x toast.gif\"\r\n\r\nTOAST\r\n--b--\r\n"
	if _, err := OutgoingText(strings.NewReader(email)); err != errNoAttachments {
		t.Errorf("got %v, want errNoAttachments", err)
	}
}
