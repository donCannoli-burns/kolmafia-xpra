package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"io"
	"slices"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-message/textproto"
)

// Modelled on go-imap's imapmemserver, with storage in Account and INBOX deletes sent to KoL.

const mailboxDelim = '/'

type session struct {
	bridge   *Bridge
	password string

	acct      *Account
	mbox      *Mailbox
	tracker   *imapserver.SessionTracker
	searchRes imap.UIDSet
}

var _ imapserver.SessionMove = (*session)(nil)

func noErr(text string) error {
	return &imap.Error{Type: imap.StatusResponseTypeNo, Text: text}
}

func cannotErr(text string) error {
	return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeCannot, Text: text}
}

func checkPassword(want, got string) bool {
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

func (s *session) Close() error {
	if s.tracker != nil {
		s.tracker.Close()
	}
	return nil
}

func (s *session) Login(username, password string) error {
	if !checkPassword(s.password, password) || !s.bridge.Known(username) {
		return imapserver.ErrAuthFailed
	}
	acct, err := s.bridge.Account(username)
	if err != nil {
		return err
	}
	s.acct = acct
	return nil
}

func (s *session) Select(name string, options *imap.SelectOptions) (*imap.SelectData, error) {
	s.acct.mu.Lock()
	defer s.acct.mu.Unlock()
	mbox, err := s.acct.mailbox(name)
	if err != nil {
		return nil, err
	}
	if s.tracker != nil {
		s.tracker.Close()
	}
	s.mbox, s.tracker, s.searchRes = mbox, mbox.tracker.NewSession(), nil
	if mbox.Name == "INBOX" {
		s.bridge.TriggerSync()
	}
	return mbox.selectDataLocked(), nil
}

func (s *session) Unselect() error {
	s.tracker.Close()
	s.mbox, s.tracker, s.searchRes = nil, nil, nil
	return nil
}

func (s *session) Create(string, *imap.CreateOptions) error {
	return cannotErr("Mailboxes are fixed")
}

func (s *session) Delete(string) error {
	return cannotErr("Mailboxes are fixed")
}

func (s *session) Rename(string, string, *imap.RenameOptions) error {
	return cannotErr("Mailboxes are fixed")
}

// Every mailbox is always subscribed.
func (s *session) Subscribe(string) error   { return nil }
func (s *session) Unsubscribe(string) error { return nil }

func (s *session) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	if len(patterns) == 0 {
		return w.WriteList(&imap.ListData{Attrs: []imap.MailboxAttr{imap.MailboxAttrNoSelect}, Delim: mailboxDelim})
	}
	s.acct.mu.Lock()
	defer s.acct.mu.Unlock()
	for _, mbox := range s.acct.Mailboxes {
		if !slices.ContainsFunc(patterns, func(p string) bool { return imapserver.MatchList(mbox.Name, mailboxDelim, ref, p) }) {
			continue
		}
		if options.SelectSpecialUse && mbox.specialUse == "" {
			continue
		}
		data := imap.ListData{Mailbox: mbox.Name, Delim: mailboxDelim, Attrs: []imap.MailboxAttr{imap.MailboxAttrSubscribed}}
		if mbox.specialUse != "" {
			data.Attrs = append(data.Attrs, mbox.specialUse)
		}
		if options.ReturnStatus != nil {
			data.Status = mbox.statusLocked(options.ReturnStatus)
		}
		if err := w.WriteList(&data); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) Status(name string, options *imap.StatusOptions) (*imap.StatusData, error) {
	s.acct.mu.Lock()
	defer s.acct.mu.Unlock()
	mbox, err := s.acct.mailbox(name)
	if err != nil {
		return nil, err
	}
	return mbox.statusLocked(options), nil
}

func (s *session) Append(name string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return nil, err
	}
	s.acct.mu.Lock()
	defer s.acct.mu.Unlock()
	mbox, err := s.acct.mailbox(name)
	if err != nil {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeTryCreate, Text: "No such mailbox"}
	}
	if mbox.Name == "INBOX" {
		return nil, cannotErr("INBOX only holds kmails")
	}
	data := mbox.appendLocked(buf.Bytes(), options.Time, options.Flags)
	s.acct.save()
	return data, nil
}

func (s *session) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	if s.tracker == nil {
		return nil
	}
	return s.tracker.Poll(w, allowExpunge)
}

func (s *session) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	if s.tracker == nil {
		<-stop
		return nil
	}
	return s.tracker.Idle(w, stop)
}

// remove deletes msgs from the selected mailbox, and from KoL first if it's the inbox.
func (s *session) remove(msgs []*Message) error {
	if len(msgs) == 0 {
		return nil
	}
	if s.mbox.Name == "INBOX" {
		pwd, err := s.bridge.pwdFor(s.acct.Name)
		if err != nil {
			return noErr(err.Error())
		}
		ids := make([]uint32, len(msgs))
		for i, m := range msgs {
			ids[i] = uint32(m.UID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.bridge.kol.Delete(ctx, pwd, ids); err != nil {
			// Whatever did get deleted is picked up by the next sync.
			s.bridge.TriggerSync()
			return noErr(err.Error())
		}
	}
	s.acct.mu.Lock()
	defer s.acct.mu.Unlock()
	gone := map[*Message]bool{}
	for _, m := range msgs {
		if slices.Contains(s.mbox.Messages, m) {
			gone[m] = true
		}
	}
	s.mbox.expungeLocked(gone)
	s.acct.save()
	return nil
}

func (s *session) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	s.acct.mu.Lock()
	var msgs []*Message
	for _, m := range s.mbox.Messages {
		if (uids == nil || uids.Contains(m.UID)) && m.hasFlag(imap.FlagDeleted) {
			msgs = append(msgs, m)
		}
	}
	s.acct.mu.Unlock()
	return s.remove(msgs)
}

func (s *session) copyLocked(numSet imap.NumSet, destName string) (*imap.CopyData, []*Message, error) {
	dest, err := s.acct.mailbox(destName)
	if err != nil {
		return nil, nil, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeTryCreate, Text: "No such mailbox"}
	}
	if dest == s.mbox {
		return nil, nil, noErr("Source and destination mailboxes are identical")
	}
	if dest.Name == "INBOX" {
		return nil, nil, cannotErr("INBOX only holds kmails")
	}
	var srcUIDs, destUIDs imap.UIDSet
	var msgs []*Message
	s.forEachLocked(numSet, func(_ uint32, m *Message) {
		data := dest.appendLocked(m.Raw, m.Time, copiedFlags(m))
		srcUIDs.AddNum(m.UID)
		destUIDs.AddNum(data.UID)
		msgs = append(msgs, m)
	})
	return &imap.CopyData{UIDValidity: dest.UIDValidity, SourceUIDs: srcUIDs, DestUIDs: destUIDs}, msgs, nil
}

func copiedFlags(m *Message) []imap.Flag {
	return slices.DeleteFunc(slices.Clone(m.Flags), func(f imap.Flag) bool {
		return canonicalFlag(f) == canonicalFlag(imap.FlagDeleted)
	})
}

func (s *session) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	s.acct.mu.Lock()
	defer s.acct.mu.Unlock()
	data, _, err := s.copyLocked(numSet, dest)
	if err == nil {
		s.acct.save()
	}
	return data, err
}

// Move deletes from KoL before copying, so a failed delete leaves things as they were.
func (s *session) Move(w *imapserver.MoveWriter, numSet imap.NumSet, dest string) error {
	if s.mbox.Name == "INBOX" {
		s.acct.mu.Lock()
		var msgs []*Message
		s.forEachLocked(numSet, func(_ uint32, m *Message) { msgs = append(msgs, m) })
		s.acct.mu.Unlock()
		// The mailbox list never changes after loading, so this needs no lock.
		dst, err := s.acct.mailbox(dest)
		if err != nil {
			return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeTryCreate, Text: "No such mailbox"}
		}
		if dst == s.mbox {
			return noErr("Source and destination mailboxes are identical")
		}
		if err := s.remove(msgs); err != nil {
			return err
		}
		s.acct.mu.Lock()
		var srcUIDs, destUIDs imap.UIDSet
		for _, m := range msgs {
			data := dst.appendLocked(m.Raw, m.Time, copiedFlags(m))
			srcUIDs.AddNum(m.UID)
			destUIDs.AddNum(data.UID)
		}
		s.acct.save()
		s.acct.mu.Unlock()
		return w.WriteCopyData(&imap.CopyData{UIDValidity: dst.UIDValidity, SourceUIDs: srcUIDs, DestUIDs: destUIDs})
	}

	s.acct.mu.Lock()
	data, msgs, err := s.copyLocked(numSet, dest)
	s.acct.mu.Unlock()
	if err != nil {
		return err
	}
	if err := s.remove(msgs); err != nil {
		return err
	}
	return w.WriteCopyData(data)
}

func (s *session) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	markSeen := slices.ContainsFunc(options.BodySection, func(bs *imap.FetchItemBodySection) bool { return !bs.Peek })

	s.acct.mu.Lock()
	defer s.acct.mu.Unlock()
	var err error
	s.forEachLocked(numSet, func(seqNum uint32, m *Message) {
		if err != nil {
			return
		}
		if markSeen && !m.hasFlag(imap.FlagSeen) {
			m.addFlag(imap.FlagSeen)
			s.mbox.tracker.QueueMessageFlags(seqNum, m.UID, slices.Clone(m.Flags), nil)
		}
		err = fetchMessage(w.CreateMessage(s.tracker.EncodeSeqNum(seqNum)), m, options)
	})
	if markSeen {
		s.acct.save()
	}
	return err
}

func fetchMessage(w *imapserver.FetchResponseWriter, m *Message, options *imap.FetchOptions) error {
	w.WriteUID(m.UID)
	if options.Flags {
		w.WriteFlags(slices.Clone(m.Flags))
	}
	if options.InternalDate {
		w.WriteInternalDate(m.Time)
	}
	if options.RFC822Size {
		w.WriteRFC822Size(int64(len(m.Raw)))
	}
	if options.Envelope {
		if h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(m.Raw))); err == nil {
			w.WriteEnvelope(imapserver.ExtractEnvelope(h))
		}
	}
	if options.BodyStructure != nil {
		w.WriteBodyStructure(imapserver.ExtractBodyStructure(bytes.NewReader(m.Raw)))
	}
	write := func(wc io.WriteCloser, buf []byte) error {
		_, err := wc.Write(buf)
		if cerr := wc.Close(); err == nil {
			err = cerr
		}
		return err
	}
	for _, bs := range options.BodySection {
		buf := imapserver.ExtractBodySection(bytes.NewReader(m.Raw), bs)
		if err := write(w.WriteBodySection(bs, int64(len(buf))), buf); err != nil {
			return err
		}
	}
	for _, bs := range options.BinarySection {
		buf := imapserver.ExtractBinarySection(bytes.NewReader(m.Raw), bs)
		if err := write(w.WriteBinarySection(bs, int64(len(buf))), buf); err != nil {
			return err
		}
	}
	for _, bss := range options.BinarySectionSize {
		w.WriteBinarySectionSize(bss, imapserver.ExtractBinarySectionSize(bytes.NewReader(m.Raw), bss))
	}
	return w.Close()
}

func (s *session) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	s.acct.mu.Lock()
	s.forEachLocked(numSet, func(seqNum uint32, m *Message) {
		m.store(flags)
		s.mbox.tracker.QueueMessageFlags(seqNum, m.UID, slices.Clone(m.Flags), s.tracker)
	})
	s.acct.save()
	s.acct.mu.Unlock()
	if !flags.Silent {
		return s.Fetch(w, numSet, &imap.FetchOptions{Flags: true})
	}
	return nil
}

func (s *session) Search(numKind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	s.acct.mu.Lock()
	defer s.acct.mu.Unlock()

	s.staticSearchCriteria(criteria)

	var (
		data   imap.SearchData
		seqSet imap.SeqSet
		uidSet imap.UIDSet
	)
	for i, m := range s.mbox.Messages {
		seqNum := s.tracker.EncodeSeqNum(uint32(i) + 1)
		if !searchMessage(m, seqNum, criteria) {
			continue
		}
		uidSet.AddNum(m.UID)
		var num uint32
		switch numKind {
		case imapserver.NumKindSeq:
			if seqNum == 0 {
				continue
			}
			seqSet.AddNum(seqNum)
			num = seqNum
		case imapserver.NumKindUID:
			num = uint32(m.UID)
		}
		if data.Min == 0 || num < data.Min {
			data.Min = num
		}
		if num > data.Max {
			data.Max = num
		}
		data.Count++
	}
	switch numKind {
	case imapserver.NumKindSeq:
		data.All = seqSet
	case imapserver.NumKindUID:
		data.All = uidSet
	}
	if options.ReturnSave {
		s.searchRes = uidSet
	}
	return &data, nil
}

func (s *session) staticSearchCriteria(criteria *imap.SearchCriteria) {
	seqNums := make([]imap.SeqSet, 0, len(criteria.SeqNum))
	for _, seqSet := range criteria.SeqNum {
		switch numSet := s.staticNumSet(seqSet).(type) {
		case imap.SeqSet:
			seqNums = append(seqNums, numSet)
		case imap.UIDSet: // SEARCHRES
			criteria.UID = append(criteria.UID, numSet)
		}
	}
	criteria.SeqNum = seqNums
	for i, uidSet := range criteria.UID {
		criteria.UID[i] = s.staticNumSet(uidSet).(imap.UIDSet)
	}
	for i := range criteria.Not {
		s.staticSearchCriteria(&criteria.Not[i])
	}
	for i := range criteria.Or {
		for j := range criteria.Or[i] {
			s.staticSearchCriteria(&criteria.Or[i][j])
		}
	}
}

func (s *session) forEachLocked(numSet imap.NumSet, f func(seqNum uint32, m *Message)) {
	numSet = s.staticNumSet(numSet)
	for i, m := range slices.Clone(s.mbox.Messages) {
		seqNum := uint32(i) + 1
		var contains bool
		switch numSet := numSet.(type) {
		case imap.SeqSet:
			clientSeq := s.tracker.EncodeSeqNum(seqNum)
			contains = clientSeq != 0 && numSet.Contains(clientSeq)
		case imap.UIDSet:
			contains = numSet.Contains(m.UID)
		}
		if contains {
			f(seqNum, m)
		}
	}
}

// staticNumSet resolves "*" and the SEARCHRES "$" marker.
func (s *session) staticNumSet(numSet imap.NumSet) imap.NumSet {
	if imap.IsSearchRes(numSet) {
		return s.searchRes
	}
	switch numSet := numSet.(type) {
	case imap.SeqSet:
		max := uint32(len(s.mbox.Messages))
		for i := range numSet {
			staticNumRange(&numSet[i].Start, &numSet[i].Stop, max)
		}
	case imap.UIDSet:
		max := uint32(s.mbox.UIDNext) - 1
		for i := range numSet {
			staticNumRange((*uint32)(&numSet[i].Start), (*uint32)(&numSet[i].Stop), max)
		}
	}
	return numSet
}

func staticNumRange(start, stop *uint32, max uint32) {
	dyn := false
	if *start == 0 {
		*start, dyn = max, true
	}
	if *stop == 0 {
		*stop, dyn = max, true
	}
	if dyn && *start > *stop {
		*start, *stop = *stop, *start
	}
}
