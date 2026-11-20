package downloads

import (
	"context"
	"sync"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
)

// Connect signs in to qBittorrent with the saved settings.
//
// The signed-in client is kept and reused, so the app signs in once instead
// of on every check (every 30 seconds), which filled qBittorrent's log. A
// change of address, username or password signs in again; so does an
// expired sign-in (see QBittorrent.do).
func Connect(ctx context.Context, set store.Settings) (*QBittorrent, error) {
	key := set.QBittorrent.URL + "\x00" + set.QBittorrent.Username + "\x00" + set.QBittorrent.Password
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.qb != nil && shared.key == key {
		return shared.qb, nil
	}
	qb, err := NewQBittorrent(set.QBittorrent.URL, set.QBittorrent.Username, set.QBittorrent.Password)
	if err != nil {
		return nil, err
	}
	if err := qb.Login(ctx); err != nil {
		return nil, err
	}
	shared.key, shared.qb = key, qb
	return qb, nil
}

// shared is the signed-in client Connect hands out.
var shared struct {
	mu  sync.Mutex
	key string
	qb  *QBittorrent
}

// Send gives a link to qBittorrent in the app's category (creating it,
// saving into the downloads folder, if needed), with an optional tag. size
// (0 = unknown) is checked against the free space first.
func Send(ctx context.Context, set store.Settings, link, tag string, size int64) error {
	if err := CheckSpace(set, size); err != nil {
		return err
	}
	qb, err := Connect(ctx, set)
	if err != nil {
		return err
	}
	savePath := set.Folders.ToQBit(set.Folders.Downloads)
	if err := qb.EnsureCategory(ctx, set.QBittorrent.Category, savePath); err != nil {
		return err
	}
	return qb.Add(ctx, link, set.QBittorrent.Category, tag)
}

// Remove deletes every torrent with this tag in the app's category from
// qBittorrent, with its downloaded files. A torrent that isn't there (yet)
// is no error.
func Remove(ctx context.Context, set store.Settings, tag string) error {
	qb, err := Connect(ctx, set)
	if err != nil {
		return err
	}
	list, err := qb.Torrents(ctx, set.QBittorrent.Category)
	if err != nil {
		return err
	}
	for _, t := range list {
		if t.HasTag(tag) {
			if err := qb.Delete(ctx, t.Hash); err != nil {
				return err
			}
		}
	}
	return nil
}
