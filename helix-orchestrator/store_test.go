package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDequeueOrdersAndFilters(t *testing.T) {
	store := testStore(t)
	first, err := store.CreateRequest("key_abc", "windows", "wss://win.example/", "w")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	second, err := store.CreateRequest("key_abc", "linux", "wss://lin.example/", "l")
	if err != nil {
		t.Fatal(err)
	}
	linux, err := store.Dequeue("linux")
	if err != nil {
		t.Fatal(err)
	}
	if linux["uid"] != second || linux["status"] != "pending" || linux["token"] != "l" {
		t.Fatalf("linux claim %#v", linux)
	}
	any, err := store.Dequeue("")
	if err != nil {
		t.Fatal(err)
	}
	if any["uid"] != first || any["os"] != "windows" {
		t.Fatalf("any claim %#v", any)
	}
	empty, err := store.Dequeue("")
	if err != nil || empty != nil {
		t.Fatalf("empty %#v %v", empty, err)
	}
}

func TestClaimIsExclusive(t *testing.T) {
	store := testStore(t)
	for range 2 {
		if _, err := store.CreateRequest("key_abc", "windows", "wss://vm.example/", "t"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := map[string]int{}
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			item, err := store.Dequeue("windows")
			if err != nil {
				t.Errorf("dequeue: %v", err)
				return
			}
			if item == nil {
				t.Error("missing claim")
				return
			}
			mu.Lock()
			got[item["uid"].(string)]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(got) != 2 {
		t.Fatalf("claims %#v", got)
	}
	for uid, n := range got {
		if n != 1 {
			t.Fatalf("%s claimed %d times", uid, n)
		}
	}
}

func TestResultRoundTrip(t *testing.T) {
	store := testStore(t)
	uid, err := store.CreateRequest("key_abc", "windows", "wss://vm.example/", "t")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Dequeue("windows"); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update("windows", uid, map[string]any{
		"status":      "success",
		"host":        "10.0.0.8",
		"n":           float64(2),
		"__proto__":   "nope",
		"nested":      map[string]any{"ok": true},
		"constructor": "nope",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated["status"] != "success" || updated["host"] != "10.0.0.8" || updated["n"] != float64(2) {
		t.Fatalf("updated %#v", updated)
	}
	if _, ok := updated["__proto__"]; ok {
		t.Fatal("proto key stored")
	}
	nested, _ := updated["nested"].(map[string]any)
	if nested["ok"] != true {
		t.Fatalf("nested %#v", updated["nested"])
	}
	body, found := store.GetForUser("key_abc", uid)
	if !found || body["host"] != "10.0.0.8" || body["status"] != "success" {
		t.Fatalf("user %#v", body)
	}
}

func TestImportFile(t *testing.T) {
	store := testStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	body := []byte(`{
		"keys": [{"id":"key_abc","role":"user","name":"alice","prefix":"user_abc…","hash":"` + hashToken("user_secret") + `","createdAt":"2026-01-01T00:00:00.000Z"}],
		"requests": [
			{"uid":"queued1","os":"windows","url":"wss://vm.example/","token":"tok","userKeyId":"key_abc","status":"queued","result":{},"createdAt":"2026-01-01T00:00:00.000Z","updatedAt":"2026-01-01T00:00:00.000Z"},
			{"uid":"gone","os":"windows","userKeyId":"key_abc","status":"deleted","result":{"host":"10.0.0.1"},"createdAt":"2026-01-01T00:00:01.000Z","updatedAt":"2026-01-01T00:00:02.000Z"}
		],
		"deleted": ["ghost"],
		"access": [{"uid":"queued1","username":"vmuser","password":"vmpass","createdAt":"2026-01-01T00:00:00.000Z","url":"wss://vm.example/","token":"tok","rdpUsername":"Administrator","rdpPassword":"secret","key":"0123456789abcdef0123456789abcdef","updatedAt":"2026-01-01T00:00:00.000Z"}]
	}`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	counts, err := store.ImportFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Keys != 1 || counts.Requests != 2 || counts.Queued != 1 || counts.Access != 1 {
		t.Fatalf("counts %+v", counts)
	}
	key, ok := store.FindKey("user_secret")
	if !ok || key.ID != "key_abc" || key.Role != "user" {
		t.Fatalf("key %#v %v", key, ok)
	}
	found, err := store.DeleteKey("key_abc")
	if err != nil || !found {
		t.Fatalf("delete key %v %v", found, err)
	}
	if _, ok := store.FindKey("user_secret"); ok {
		t.Fatal("key still authenticates")
	}
	var viewer string
	for _, row := range store.ListRequests() {
		if row.UID == "queued1" {
			viewer = row.Viewer
		}
		if row.UID == "gone" && row.Viewer != "" {
			t.Fatal("deleted request has a viewer")
		}
	}
	if !strings.Contains(viewer, "username=vmuser") || !strings.Contains(viewer, "password=vmpass") {
		t.Fatalf("viewer %q", viewer)
	}
	pile := store.DeletedAmong([]string{"ghost", "gone", "queued1"})
	if len(pile) != 1 || pile[0] != "gone" {
		t.Fatalf("pile %#v", pile)
	}
	claimed, err := store.Dequeue("windows")
	if err != nil || claimed["uid"] != "queued1" || claimed["token"] != "tok" {
		t.Fatalf("claim %#v %v", claimed, err)
	}
	row := store.Authenticate("vmuser", "vmpass")
	if row == nil || row.RDPUser != "Administrator" || row.Key != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("login %#v", row)
	}
	if store.Authenticate("vmuser", "wrong") != nil || store.Authenticate("nobody", "vmpass") != nil {
		t.Fatal("accepted a bad login")
	}
}

func TestMicrovmRecord(t *testing.T) {
	store := testStore(t)
	if _, err := store.SaveMicrovm(microvmRecord{ID: "bad id"}); err == nil {
		t.Fatal("accepted an id with a space")
	}
	older, err := store.SaveMicrovm(microvmRecord{
		ID:        "m-old",
		Endpoint:  "old.example",
		State:     "PENDING",
		Region:    "us-east-1",
		ImageArn:  "arn:aws:lambda:us-east-1:123:microvm-image:helix",
		CreatedAt: "2020-01-01T00:00:00.000Z",
		Key:       "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	if older.URL != "wss://old.example/" || older.CreatedAt != "2020-01-01T00:00:00.000Z" {
		t.Fatalf("saved %#v", older)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := store.SaveMicrovm(microvmRecord{
		ID:       "m-new",
		Endpoint: "https://new.example/ws",
		State:    "RUNNING",
		Region:   "us-east-1",
	}); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListMicrovms()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "m-new" || list[1].ID != "m-old" {
		t.Fatalf("list %#v", list)
	}
	if list[0].URL != "wss://new.example/ws" {
		t.Fatalf("url %q", list[0].URL)
	}
	updated, err := store.SaveMicrovm(microvmRecord{
		ID:        "m-old",
		Endpoint:  "old.example",
		State:     "RUNNING",
		CreatedAt: older.CreatedAt,
		Token:     "tok-1",
		Key:       older.Key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CreatedAt != older.CreatedAt || updated.State != "RUNNING" || updated.Token != "tok-1" {
		t.Fatalf("updated %#v", updated)
	}
	got, ok, err := store.GetMicrovm("m-old")
	if err != nil || !ok || got.Token != "tok-1" || got.Key != older.Key || got.State != "RUNNING" {
		t.Fatalf("get %#v %v %v", got, ok, err)
	}
	if _, ok, err := store.GetMicrovm("missing"); err != nil || ok {
		t.Fatalf("missing %v %v", ok, err)
	}
}
