package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

// INBOX mirrors the KoL inbox, with kmail IDs as UIDs. The rest only exist here, so that clients
// have somewhere to keep sent copies, drafts and deleted kmails.
var mailboxDefs = []struct {
	name       string
	specialUse imap.MailboxAttr
}{
	{"INBOX", ""},
	{"Sent", imap.MailboxAttrSent},
	{"Drafts", imap.MailboxAttrDrafts},
	{"Trash", imap.MailboxAttrTrash},
	{"Archive", imap.MailboxAttrArchive},
}

const inboxFetchCount = 100

type Bridge struct {
	kol *KoL
	dir string

	mu       sync.Mutex
	accounts map[string]*Account
	current  *Status

	syncNow chan struct{}
}

func NewBridge(kol *KoL, dir string) *Bridge {
	return &Bridge{kol: kol, dir: dir, accounts: map[string]*Account{}, syncNow: make(chan struct{}, 1)}
}

var validName = regexp.MustCompile(`^[a-z0-9_ ]+$`)

func (b *Bridge) accountPath(name string) (string, error) {
	key := strings.ToLower(name)
	if !validName.MatchString(key) {
		return "", fmt.Errorf("invalid character name %q", name)
	}
	return filepath.Join(b.dir, key+".json"), nil
}

// Known reports whether name is logged in now or has been before.
func (b *Bridge) Known(name string) bool {
	if s := b.currentStatus(); s != nil && strings.EqualFold(s.Name, name) {
		return true
	}
	path, err := b.accountPath(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

func (b *Bridge) Account(name string) (*Account, error) {
	path, err := b.accountPath(name)
	if err != nil {
		return nil, err
	}
	key := strings.ToLower(name)
	b.mu.Lock()
	defer b.mu.Unlock()
	if a, ok := b.accounts[key]; ok {
		return a, nil
	}
	a, err := loadAccount(b, path)
	if err != nil {
		return nil, err
	}
	b.accounts[key] = a
	return a, nil
}

func (b *Bridge) currentStatus() *Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.current
}

// pwdFor returns the password hash if name is the character KoLmafia is logged in as.
func (b *Bridge) pwdFor(name string) (string, error) {
	s := b.currentStatus()
	if s == nil || !strings.EqualFold(s.Name, name) {
		return "", fmt.Errorf("KoLmafia is not logged in as %s", name)
	}
	return s.Pwd, nil
}

func (b *Bridge) TriggerSync() {
	select {
	case b.syncNow <- struct{}{}:
	default:
	}
}

func (b *Bridge) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := b.Sync(ctx); err != nil {
			log.Printf("sync: %v", err)
		}
		// Some clients reselect INBOX constantly; don't let that turn into a request each time.
		minWait := time.After(10 * time.Second)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-b.syncNow:
			select {
			case <-ctx.Done():
				return
			case <-minWait:
			}
		}
	}
}

func (b *Bridge) Sync(ctx context.Context) error {
	s, err := b.kol.Status(ctx)
	b.mu.Lock()
	b.current = s
	b.mu.Unlock()
	if err != nil {
		return err
	}
	a, err := b.Account(s.Name)
	if err != nil {
		return err
	}
	kmails, err := b.kol.Kmails(ctx, inboxFetchCount)
	if err != nil {
		return err
	}
	playerID, _ := strconv.Atoi(s.PlayerID)
	return a.syncInbox(s.Name, playerID, kmails, len(kmails) >= inboxFetchCount)
}

type Account struct {
	bridge *Bridge
	path   string

	mu        sync.Mutex
	Name      string     `json:"name"`
	PlayerID  int        `json:"playerId"`
	Mailboxes []*Mailbox `json:"mailboxes"`
}

type Mailbox struct {
	Name        string     `json:"name"`
	UIDValidity uint32     `json:"uidValidity"`
	UIDNext     imap.UID   `json:"uidNext"`
	Messages    []*Message `json:"messages"`

	specialUse imap.MailboxAttr
	tracker    *imapserver.MailboxTracker
}

type Message struct {
	UID   imap.UID    `json:"uid"`
	Time  time.Time   `json:"time"`
	Flags []imap.Flag `json:"flags"`
	Raw   []byte      `json:"raw"`
}

func loadAccount(b *Bridge, path string) (*Account, error) {
	a := &Account{bridge: b, path: path}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(data, a); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	for _, def := range mailboxDefs {
		mbox := a.mailboxLocked(def.name)
		if mbox == nil {
			mbox = &Mailbox{Name: def.name, UIDValidity: uint32(time.Now().Unix()), UIDNext: 1}
			if def.name == "INBOX" {
				// UIDs are kmail IDs, which never change, so this never needs to either.
				mbox.UIDValidity = 1
			}
			a.Mailboxes = append(a.Mailboxes, mbox)
		}
		mbox.specialUse = def.specialUse
		mbox.tracker = imapserver.NewMailboxTracker(uint32(len(mbox.Messages)))
	}
	return a, nil
}

func (a *Account) saveLocked() error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

func (a *Account) save() {
	if err := a.saveLocked(); err != nil {
		log.Printf("saving %s: %v", a.path, err)
	}
}

func (a *Account) mailboxLocked(name string) *Mailbox {
	if strings.EqualFold(name, "INBOX") {
		name = "INBOX"
	}
	for _, m := range a.Mailboxes {
		if m.Name == name {
			return m
		}
	}
	return nil
}

func (a *Account) mailbox(name string) (*Mailbox, error) {
	if m := a.mailboxLocked(name); m != nil {
		return m, nil
	}
	return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNonExistent, Text: "No such mailbox"}
}

func (a *Account) syncInbox(name string, playerID int, kmails []Kmail, truncated bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	changed := a.Name != name || a.PlayerID != playerID
	a.Name, a.PlayerID = name, playerID
	inbox := a.mailboxLocked("INBOX")

	fetched := make(map[imap.UID]bool, len(kmails))
	oldest := imap.UID(0)
	for _, k := range kmails {
		uid := imap.UID(k.ID)
		fetched[uid] = true
		if oldest == 0 || uid < oldest {
			oldest = uid
		}
	}

	// Deleted in-game. api.php only shows the newest 100, so anything older than that is left be.
	gone := map[*Message]bool{}
	for _, m := range inbox.Messages {
		if !fetched[m.UID] && (!truncated || m.UID > oldest) {
			gone[m] = true
		}
	}
	if len(gone) > 0 {
		inbox.expungeLocked(gone)
		changed = true
	}

	slices.SortFunc(kmails, func(x, y Kmail) int { return int(int64(x.ID) - int64(y.ID)) })
	me := playerAddress(name, playerID)
	for _, k := range kmails {
		uid := imap.UID(k.ID)
		if uid < inbox.UIDNext {
			continue
		}
		raw, err := RenderKmail(k, me)
		if err != nil {
			log.Printf("rendering kmail %d: %v", k.ID, err)
			continue
		}
		inbox.Messages = append(inbox.Messages, &Message{UID: uid, Time: k.Time, Raw: raw})
		inbox.UIDNext = uid + 1
		inbox.tracker.QueueNumMessages(uint32(len(inbox.Messages)))
		changed = true
	}

	if changed {
		return a.saveLocked()
	}
	return nil
}

func (mbox *Mailbox) appendLocked(raw []byte, t time.Time, flags []imap.Flag) *imap.AppendData {
	if t.IsZero() {
		t = time.Now()
	}
	msg := &Message{UID: mbox.UIDNext, Time: t, Raw: raw}
	for _, f := range flags {
		msg.addFlag(f)
	}
	mbox.UIDNext++
	mbox.Messages = append(mbox.Messages, msg)
	mbox.tracker.QueueNumMessages(uint32(len(mbox.Messages)))
	return &imap.AppendData{UIDValidity: mbox.UIDValidity, UID: msg.UID}
}

// expungeLocked removes messages and returns their sequence numbers, highest first.
func (mbox *Mailbox) expungeLocked(gone map[*Message]bool) []uint32 {
	var seqNums []uint32
	var kept []*Message
	for i := len(mbox.Messages) - 1; i >= 0; i-- {
		if gone[mbox.Messages[i]] {
			seqNum := uint32(i) + 1
			seqNums = append(seqNums, seqNum)
			mbox.tracker.QueueExpunge(seqNum)
		}
	}
	for _, m := range mbox.Messages {
		if !gone[m] {
			kept = append(kept, m)
		}
	}
	mbox.Messages = kept
	return seqNums
}

func (mbox *Mailbox) statusLocked(options *imap.StatusOptions) *imap.StatusData {
	data := imap.StatusData{Mailbox: mbox.Name}
	n := uint32(len(mbox.Messages))
	if options.NumMessages {
		data.NumMessages = &n
	}
	if options.UIDNext {
		data.UIDNext = mbox.UIDNext
	}
	if options.UIDValidity {
		data.UIDValidity = mbox.UIDValidity
	}
	if options.NumUnseen {
		unseen := n - mbox.countFlagLocked(imap.FlagSeen)
		data.NumUnseen = &unseen
	}
	if options.NumDeleted {
		deleted := mbox.countFlagLocked(imap.FlagDeleted)
		data.NumDeleted = &deleted
	}
	if options.Size {
		var size int64
		for _, m := range mbox.Messages {
			size += int64(len(m.Raw))
		}
		data.Size = &size
	}
	if options.NumRecent {
		zero := uint32(0)
		data.NumRecent = &zero
	}
	return &data
}

func (mbox *Mailbox) countFlagLocked(flag imap.Flag) uint32 {
	var n uint32
	for _, m := range mbox.Messages {
		if m.hasFlag(flag) {
			n++
		}
	}
	return n
}

func (mbox *Mailbox) selectDataLocked() *imap.SelectData {
	seen := map[imap.Flag]bool{}
	flags := []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDeleted, imap.FlagDraft}
	for _, f := range flags {
		seen[canonicalFlag(f)] = true
	}
	for _, m := range mbox.Messages {
		for _, f := range m.Flags {
			if !seen[canonicalFlag(f)] {
				seen[canonicalFlag(f)] = true
				flags = append(flags, f)
			}
		}
	}
	var firstUnseen uint32
	for i, m := range mbox.Messages {
		if !m.hasFlag(imap.FlagSeen) {
			firstUnseen = uint32(i) + 1
			break
		}
	}
	return &imap.SelectData{
		Flags:             flags,
		PermanentFlags:    append(slices.Clone(flags), imap.FlagWildcard),
		NumMessages:       uint32(len(mbox.Messages)),
		FirstUnseenSeqNum: firstUnseen,
		UIDNext:           mbox.UIDNext,
		UIDValidity:       mbox.UIDValidity,
	}
}

func canonicalFlag(f imap.Flag) imap.Flag {
	return imap.Flag(strings.ToLower(string(f)))
}

func (m *Message) hasFlag(f imap.Flag) bool {
	return slices.ContainsFunc(m.Flags, func(x imap.Flag) bool { return canonicalFlag(x) == canonicalFlag(f) })
}

func (m *Message) addFlag(f imap.Flag) {
	if !m.hasFlag(f) {
		m.Flags = append(m.Flags, f)
	}
}

func (m *Message) delFlag(f imap.Flag) {
	m.Flags = slices.DeleteFunc(m.Flags, func(x imap.Flag) bool { return canonicalFlag(x) == canonicalFlag(f) })
}

func (m *Message) store(s *imap.StoreFlags) {
	switch s.Op {
	case imap.StoreFlagsSet:
		m.Flags = nil
		fallthrough
	case imap.StoreFlagsAdd:
		for _, f := range s.Flags {
			m.addFlag(f)
		}
	case imap.StoreFlagsDel:
		for _, f := range s.Flags {
			m.delFlag(f)
		}
	}
}
