// Package model holds the types that several parts of the app share.
package model

import "time"

// Query is one search, as Movies, Shows or the Search page asks it.
type Query struct {
	// Kind is "search" (anything), "movie" or "tv".
	Kind string `json:"kind"`
	// Text is the words to search for, e.g. a title.
	Text string `json:"text"`
	// IMDBID is "tt" and digits, e.g. tt0063350. Used by sites that can
	// search by it; others search by Text.
	IMDBID string `json:"imdbId,omitempty"`
	// Season and Episode narrow a TV search. 0 means not set.
	Season  int `json:"season,omitempty"`
	Episode int `json:"episode,omitempty"`
	// Categories are Newznab category numbers (2000 movies, 5000 TV); empty
	// means all.
	Categories []int `json:"categories,omitempty"`
}

// Release is one search result: a torrent that an indexer offers.
type Release struct {
	IndexerID   string    `json:"indexerId"`
	IndexerName string    `json:"indexerName"`
	Title       string    `json:"title"`
	Size        int64     `json:"size"`
	Seeders     int       `json:"seeders"`
	Leechers    int       `json:"leechers"`
	Grabs       int       `json:"grabs,omitempty"`
	PublishedAt time.Time `json:"publishedAt,omitzero"`
	Categories  []int     `json:"categories"`
	IMDBID      string    `json:"imdbId,omitempty"`
	DetailsURL  string    `json:"detailsUrl,omitempty"`
	// DownloadURL is a .torrent address, or a page that leads to one.
	DownloadURL string `json:"downloadUrl,omitempty"`
	MagnetURL   string `json:"magnetUrl,omitempty"`
	InfoHash    string `json:"infoHash,omitempty"`
}
