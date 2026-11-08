package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestListNameAndSendQueue(t *testing.T) {
	home := readyHome(t)
	s := newStore(home, 99)
	os.MkdirAll(filepath.Join(home, "gateway"), 0700)
	atomicIdentity(home, 99)
	s.update(func(v *state) error {
		v.Chats["-1"] = &chat{ID: -1, Origin: -1, Type: "group", Title: "Команда\tA", Available: true}
		v.Chats["-2"] = &chat{ID: -2, Origin: -2, Type: "group", Username: "team", Available: true}
		return nil
	})
	var out, diag bytes.Buffer
	a := &app{home: home, ctx: context.Background(), out: &out, errOut: &diag}
	if err := listChats(a, &listCommand{JSON: true}); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Chats []listedChat `json:"chats"`
	}
	if json.Unmarshal(out.Bytes(), &doc) != nil || len(doc.Chats) != 2 || doc.Chats[1].Name != "Команда\tA" || doc.Chats[0].Name != "@team" {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := listChats(a, &listCommand{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Команда A")) {
		t.Fatal("unsafe table name")
	}
	f, err := lockFile(filepath.Join(home, "gateway", "process.lock"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(f)
	out.Reset()
	if err = sendChat(a, &sendCommand{Chat: -1, Message: "compose", JSON: true}); err != nil {
		t.Fatal(err)
	}
	var submitted job
	if json.Unmarshal(out.Bytes(), &submitted) != nil || submitted.Status != "queued" {
		t.Fatal(out.String())
	}
	v, _ := s.snapshot()
	if v.Chats["-1"].Jobs[0].Instruction != "compose" {
		t.Fatal(fmt.Sprint(v))
	}
}
