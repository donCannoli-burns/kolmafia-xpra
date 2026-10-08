package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/emersion/go-message/mail"
)

func TestRenderAttachments(t *testing.T) {
	k := Kmail{
		ID: 1, Type: "normal", FromID: 456, FromName: "Bob",
		Message: `Here you go<center><table class="item" style="float: none" rel="id=641&s=14&q=0&d=1&g=0&t=1&n=3&m=0&p=0&u=e"><tr><td><img src="https://images.kingdomofloathing.com/itemimages/toast.gif" alt="toast" title="toast" class=hand onClick='descitem(931984879)' ></td><td valign=center class=effect>You acquire <b>3 toasts</b></td></tr></table></center>` +
			`<center><table class="item" style="float: none" rel="id=99&s=0&q=0&d=0&g=0&t=1&n=1&m=0&p=0&u=u"><tr><td><img src="https://images.kingdomofloathing.com/itemimages/gone.gif" alt="a/b" title="a/b" class=hand ></td><td valign=center class=effect>You acquire an item: <b>a/b</b></td></tr></table></center>` +
			`<center>You gain 1,000 Meat</center>`,
	}
	images := map[string][]byte{"toast.gif": []byte("TOAST"), "meat.gif": []byte("MEAT")}
	raw, err := RenderKmail(k, playerAddress("Alice", 123), func(name string) ([]byte, error) { return images[name], nil })
	if err != nil {
		t.Fatal(err)
	}

	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	type part struct{ filename, contentType, itemID, body string }
	var got []part
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		h, ok := p.Header.(*mail.AttachmentHeader)
		if !ok {
			continue
		}
		name, _ := h.Filename()
		ct, _, _ := h.ContentType()
		b, _ := io.ReadAll(p.Body)
		got = append(got, part{name, ct, h.Get("X-KoL-Item-Id"), string(b)})
	}
	want := []part{
		{"1,000 Meat.gif", "image/gif", "", "MEAT"},
		{"3 × toast.gif", "image/gif", "641", "TOAST"},
		// No icon to be had, and no slashes in file names.
		{"a-b", "application/octet-stream", "99", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("attachment %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}
