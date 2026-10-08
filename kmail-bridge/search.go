package main

// Adapted from go-imap's imapmemserver (MIT, Copyright (c) 2013 The Go-IMAP Authors).

import (
	"bytes"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	gomessage "github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

func entity(m *Message) *gomessage.Entity {
	e, _ := gomessage.Read(bytes.NewReader(m.Raw))
	if e == nil {
		e, _ = gomessage.New(gomessage.Header{}, bytes.NewReader(nil))
	}
	return e
}

func searchMessage(m *Message, seqNum uint32, criteria *imap.SearchCriteria) bool {
	for _, seqSet := range criteria.SeqNum {
		if seqNum == 0 || !seqSet.Contains(seqNum) {
			return false
		}
	}
	for _, uidSet := range criteria.UID {
		if !uidSet.Contains(m.UID) {
			return false
		}
	}
	if !matchDate(m.Time, criteria.Since, criteria.Before) {
		return false
	}
	for _, flag := range criteria.Flag {
		if !m.hasFlag(flag) {
			return false
		}
	}
	for _, flag := range criteria.NotFlag {
		if m.hasFlag(flag) {
			return false
		}
	}
	if criteria.Larger != 0 && int64(len(m.Raw)) <= criteria.Larger {
		return false
	}
	if criteria.Smaller != 0 && int64(len(m.Raw)) >= criteria.Smaller {
		return false
	}

	header := mail.Header{Header: entity(m).Header}
	for _, field := range criteria.Header {
		if !matchHeaderFields(header.FieldsByKey(field.Key), field.Value) {
			return false
		}
	}
	if !criteria.SentSince.IsZero() || !criteria.SentBefore.IsZero() {
		t, err := header.Date()
		if err != nil || !matchDate(t, criteria.SentSince, criteria.SentBefore) {
			return false
		}
	}
	for _, text := range criteria.Text {
		if !matchEntity(entity(m), text, true) {
			return false
		}
	}
	for _, body := range criteria.Body {
		if !matchEntity(entity(m), body, false) {
			return false
		}
	}
	for _, not := range criteria.Not {
		if searchMessage(m, seqNum, &not) {
			return false
		}
	}
	for _, or := range criteria.Or {
		if !searchMessage(m, seqNum, &or[0]) && !searchMessage(m, seqNum, &or[1]) {
			return false
		}
	}
	return true
}

// RFC 3501 says date comparisons ignore time zones.
func matchDate(t, since, before time.Time) bool {
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	if !since.IsZero() && t.Before(since) {
		return false
	}
	if !before.IsZero() && !t.Before(before) {
		return false
	}
	return true
}

func matchHeaderFields(fields gomessage.HeaderFields, pattern string) bool {
	if pattern == "" {
		return fields.Len() > 0
	}
	pattern = strings.ToLower(pattern)
	for fields.Next() {
		v, _ := fields.Text()
		if strings.Contains(strings.ToLower(v), pattern) {
			return true
		}
	}
	return false
}

func matchEntity(e *gomessage.Entity, pattern string, includeHeader bool) bool {
	if pattern == "" {
		return true
	}
	if includeHeader && matchHeaderFields(e.Header.Fields(), pattern) {
		return true
	}
	if mr := e.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err != nil {
				return false
			}
			if matchEntity(part, pattern, includeHeader) {
				return true
			}
		}
	}
	t, _, err := e.Header.ContentType()
	if err != nil || (!strings.HasPrefix(t, "text/") && !strings.HasPrefix(t, "message/")) {
		return false
	}
	buf, err := io.ReadAll(e.Body)
	if err != nil {
		return false
	}
	return bytes.Contains(bytes.ToLower(buf), bytes.ToLower([]byte(pattern)))
}
