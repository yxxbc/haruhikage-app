// Package domain defines provider-neutral music data shared by the application.
package domain

import (
	"net/http"
	"strings"
	"time"
)

type TrackRef struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

// Key is unambiguous even when an ID contains punctuation.
func (r TrackRef) Key() string { return r.Provider + "\x00" + r.ID }
func (r TrackRef) Valid() bool {
	return r.Provider != "" && r.ID != "" && !strings.ContainsRune(r.Provider, '\x00') && !strings.ContainsRune(r.ID, '\x00')
}

type Artist struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

type Album struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	PicURL string `json:"picUrl,omitempty"`
}

type PlaybackIdentity struct {
	ID      string `json:"id,omitempty"`
	Songmid string `json:"songmid,omitempty"`
	Hash    string `json:"hash,omitempty"`
}

type Track struct {
	Provider string           `json:"provider"`
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Artists  []Artist         `json:"artists"`
	Album    Album            `json:"album"`
	Duration int64            `json:"duration"` // milliseconds
	Alias    []string         `json:"alias,omitempty"`
	Playback PlaybackIdentity `json:"playback"`
	WebURL   string           `json:"webUrl,omitempty"`
}

func (t Track) Ref() TrackRef         { return TrackRef{Provider: t.Provider, ID: t.ID} }
func (t Track) Key() string           { return t.Ref().Key() }
func (t Track) Length() time.Duration { return time.Duration(t.Duration) * time.Millisecond }
func (t Track) URL() string           { return t.WebURL }
func (t Track) Playable() bool        { return t.Ref().Valid() }
func (t Track) ArtistNames() string {
	names := make([]string, 0, len(t.Artists))
	for _, a := range t.Artists {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}

type Lyrics struct {
	LRC         string `json:"lrc"`
	Translation string `json:"translation,omitempty"`
	YRC         string `json:"yrc,omitempty"`
	Romaji      string `json:"romaji,omitempty"`
}

// Stream is a transient resolution, never a persisted playback URL.
type Stream struct {
	URL            string      `json:"-"`
	Headers        http.Header `json:"-"`
	Size           int64       `json:"size"` // -1 means unknown
	Type           string      `json:"type,omitempty"`
	Level          string      `json:"level,omitempty"`
	Gain           float64     `json:"gain,omitempty"`
	Peak           float64     `json:"peak,omitempty"`
	SourceID       string      `json:"sourceId,omitempty"`
	SourceRevision string      `json:"sourceRevision,omitempty"`
	ExpiresAt      time.Time   `json:"-"`
}

type Playlist struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Count     int       `json:"count"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
