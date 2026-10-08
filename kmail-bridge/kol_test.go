package main

import (
	"reflect"
	"strings"
	"testing"
)

// Fixtures from kol.js's KmailMailbox tests.
func TestParseKmail(t *testing.T) {
	tests := []struct {
		name, typ, message string
		want               ParsedKmail
	}{
		{
			name:    "plain",
			typ:     "normal",
			message: "Hello there! &lt;3",
			want:    ParsedKmail{Text: "Hello there! <3"},
		},
		{
			name:    "valentine",
			typ:     "normal",
			message: `<center><table><tr><td><img src="https://d2uyhvukfffg5a.cloudfront.net/adventureimages/smiley.gif" width=100 height=100></td><td valign=center>You zerg rush'd my heart</td></tr></table></center>`,
			want:    ParsedKmail{Valentine: "smiley"},
		},
		{
			name:    "items and meat",
			typ:     "normal",
			message: `Here you go<center><table class="item" style="float: none" rel="id=641&s=14&q=0&d=1&g=0&t=1&n=1&m=0&p=0&u=e"><tr><td><img src="https://d2uyhvukfffg5a.cloudfront.net/itemimages/toast.gif" alt="toast" title="toast" class=hand onClick='descitem(931984879)' ></td><td valign=center class=effect>You acquire an item: <b>toast</b></td></tr></table></center><center>You gain 1,000 Meat</center>`,
			want:    ParsedKmail{Text: "Here you go", Items: []KmailItem{{ID: 641, Name: "toast", Quantity: 1, Image: "toast.gif"}}, Meat: 1000},
		},
		{
			name:    "stack of items",
			typ:     "normal",
			message: `<center><table class="item" style="float: none" rel="id=6863&s=1&q=0&d=1&g=0&t=1&n=2&m=0&p=0&u=e"><tr><td><img src="https://d2uyhvukfffg5a.cloudfront.net/itemimages/butterpat.gif" alt="pat of butter" title="pat of butter" class=hand onClick='descitem(310457727)' ></td><td valign=center class=effect>You acquire <b>2 pats of butter</b></td></tr></table></center>`,
			want:    ParsedKmail{Items: []KmailItem{{ID: 6863, Name: "pat of butter", Quantity: 2, Image: "butterpat.gif"}}},
		},
		{
			name:    "system kmail with formatting",
			typ:     "normal",
			message: `Dear dEvstEr5:<p>We found this telegram.<p>Respectfully,<br>The Postmen of Loathing<center><table class="item" style="float: none" rel="id=7304&s=0&q=1&d=0&g=0&t=0&n=1&m=0&p=0&u=u"><tr><td><img src="https://images.kingdomofloathing.com/itemimages/document.gif" alt="telegram from Lady Spookyraven" title="telegram from Lady Spookyraven" class=hand onClick='descitem(514364304)' ></td><td valign=center class=effect>You acquire an item: <b>telegram from Lady Spookyraven</b></td></tr></table></center>`,
			want: ParsedKmail{
				Text:  "Dear dEvstEr5:\n\nWe found this telegram.\n\nRespectfully,\nThe Postmen of Loathing",
				Items: []KmailItem{{ID: 7304, Name: "telegram from Lady Spookyraven", Quantity: 1, Image: "document.gif"}},
			},
		},
		{
			name:    "meat only",
			typ:     "normal",
			message: "<center>You acquire <b>5,000</b> Meat.<br>You gain 5,000 Meat</center>",
			want:    ParsedKmail{Meat: 5000},
		},
		{
			name:    "gift shop",
			typ:     "giftshop",
			message: "Thanks for everything!<p>Inside Note:<p>Hope you enjoy this!",
			want:    ParsedKmail{Text: "Thanks for everything!", InsideNote: "Hope you enjoy this!"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseKmail(Kmail{Type: tt.typ, Message: tt.message})
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPlayerAddress(t *testing.T) {
	for id, want := range map[int]string{24: "24@kingdomofloathing.internal", 0: "noreply@kingdomofloathing.internal", -1: "noreply@kingdomofloathing.internal"} {
		if got := playerAddress("x", id).Address; got != want {
			t.Errorf("%d: got %s, want %s", id, got, want)
		}
	}
}

func TestResultText(t *testing.T) {
	page := `<table><tr><td><b>Results:</b></td></tr><tr><td><table><tr><td>That player could not be found.</td></tr></table></td></tr></table>`
	if got := resultText(page); got != "That player could not be found." {
		t.Errorf("got %q", got)
	}
}

func TestOutgoingText(t *testing.T) {
	tests := []struct{ name, email, want string }{
		{
			name: "new message keeps subject",
			email: "Subject: Hello\r\nContent-Type: text/plain\r\n\r\n" +
				"Want some toast?\r\n\r\n-- \r\nSent from my iPhone\r\n",
			want: "Hello\n\nWant some toast?",
		},
		{
			name: "reply drops subject and quote",
			email: "Subject: Re: Want some toast?\r\nContent-Type: text/plain\r\n\r\n" +
				"Yes please\r\n\r\nOn 8 Oct 2026, at 12:00, Bob wrote:\r\n> Want some toast?\r\n",
			want: "Yes please",
		},
		{
			name: "format=flowed",
			email: "Subject: Re: x\r\nContent-Type: text/plain; format=flowed; delsp=yes\r\n\r\n" +
				"This is a long line that  \r\nwas wrapped.\r\nNew line.\r\n",
			want: "This is a long line that was wrapped.\nNew line.",
		},
		{
			name: "html only",
			email: "Subject: Re: x\r\nContent-Type: text/html\r\n\r\n" +
				"<p>Hi <b>there</b></p>",
			want: "Hi there",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := OutgoingText(strings.NewReader(tt.email))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
