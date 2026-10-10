package deckd

import (
	"path/filepath"

	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/lease"
)

// leaseStore 打开 deck 自己的写入权记账（与旧 Work Deck 的 leases.json 分开）。
func leaseStore(d deckstore.Dirs) (*lease.Store, error) {
	return lease.Open(filepath.Join(d.State, "leases.json"))
}
