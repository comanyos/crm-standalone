package domain

import "time"

// Created returns the record's creation time.
func (e *Account) Created() time.Time { return e.CreatedAt }

// Created returns the record's creation time.
func (e *Contact) Created() time.Time { return e.CreatedAt }

// Created returns the record's creation time.
func (e *Lead) Created() time.Time { return e.CreatedAt }

// Created returns the record's creation time.
func (e *Opportunity) Created() time.Time { return e.CreatedAt }

// Created returns the record's creation time.
func (e *Activity) Created() time.Time { return e.CreatedAt }
