package model

import "github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"

// Activity is one download in progress, for the Activity page.
type Activity struct {
	Kind       string         `json:"kind"` // "movie" or "show"
	ID         int            `json:"id"`   // TMDB id
	Title      string         `json:"title"`
	Year       int            `json:"year,omitempty"`
	Label      string         `json:"label,omitempty"` // "S01E02", "Season 1"
	PosterPath string         `json:"posterPath,omitempty"`
	Download   store.Download `json:"download"`
}
