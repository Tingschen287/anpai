package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/lease"
	"github.com/Tingschen287/terminal-deck/work/internal/writerlock"
)

// deckd 占着同一个 profile 的写入者锁时，旧 work 拿不到派工能力，并点名是谁占着（n050，架构稿 §3）。
func TestOldWorkDeckIsReadOnlyWhenDeckdHoldsTheProfile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	t.Setenv("WORKDECK_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	bin := filepath.Join(dir, "agent-deck")
	os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	os.WriteFile(cfg, []byte(`{"upstream_binary":"`+bin+`","profile":"deck",
		"models":[{"id":"m1","tool":"claude","route_class":"frontier"}]}`), 0o600)

	held, err := writerlock.Acquire("deck", "deckd")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	leases, err := lease.Open(filepath.Join(dir, "leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leases.Close)
	bs, bsErr := openBindings()
	prev := heldWriterLock
	heldWriterLock = nil
	defer func() { heldWriterLock = prev }()

	d, err := buildDispatcher(dir, leases, bs, bsErr, nil)
	var he *writerlock.ErrHeld
	if d != nil || !errors.As(err, &he) || he.By == nil || he.By.Program != "deckd" {
		t.Fatalf("deckd 占着时旧 work 不应得到派工器，并点名 deckd：%v %v", d, err)
	}
	if heldWriterLock != nil {
		t.Fatal("没取到锁时不该标为可写")
	}
}
