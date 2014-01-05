package main

import (
	"errors"
	"io"
	"time"
)

var notFound = errors.New("not found")

type repository interface {
	CreateUser(name string, passwordDigest, passwordSalt []byte) (userID int32, err error)
	GetUserAuthenticationByName(name string) (userID int32, passwordDigest, passwordSalt []byte, err error)
	GetUserName(userID int32) (name string, err error)

	GetFeedsUncheckedSince(since time.Time) (feeds []staleFeed, err error)
	UpdateFeedWithFetchSuccess(feedID int32, update *parsedFeed, etag string, fetchTime time.Time) error
	UpdateFeedWithFetchUnchanged(feedID int32, fetchTime time.Time) error
	UpdateFeedWithFetchFailure(feedID int32, failure string, fetchTime time.Time) (err error)

	CopyUnreadItemsAsJSONByUserID(w io.Writer, userID int32) error
	CopySubscriptionsForUserAsJSON(w io.Writer, userID int32) error

	MarkItemRead(userID, itemID int32) error
	MarkAllItemsRead(userID int32) error

	CreateSubscription(userID int32, feedURL string) (err error)
	DeleteSubscription(userID, feedID int32) (err error)

	CreateSession(id []byte, userID int32) (err error)
	GetUserIDBySessionID(id []byte) (userID int32, err error)
	DeleteSession(id []byte) (err error)
}

type staleFeed struct {
	id   int32
	url  string
	etag string
}
