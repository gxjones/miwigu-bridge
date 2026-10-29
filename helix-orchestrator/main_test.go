package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	endpoint := os.Getenv("DYNAMODB_ENDPOINT")
	if endpoint == "" {
		t.Skip("set DYNAMODB_ENDPOINT to a DynamoDB Local URL to run store tests")
	}
	store, err := openStore(storeConfig{
		Table:    "t" + randHex(8),
		Region:   "us-east-1",
		Endpoint: endpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.deleteTable(); err != nil {
			t.Errorf("delete table: %v", err)
		}
	})
	return store
}

func testApp(t *testing.T) *server {
	t.Helper()
	return &server{adminUser: "admin", adminPass: "secret", passHash: sha256.Sum256([]byte("secret")), store: testStore(t)}
}

func TestAPIContract(t *testing.T) {
	app := testApp(t)
	mux := app.routes()

	rec := call(mux, http.MethodGet, "/healthz", "", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("health: %d %s", rec.Code, rec.Body)
	}
	rec = call(mux, http.MethodPost, "/api/user/vm", `{"os":"windows"}`, "")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("content type: %d %s", rec.Code, rec.Body)
	}
	rec = call(mux, http.MethodPost, "/api/user/vm", `{`, "application/json")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid JSON") {
		t.Fatalf("invalid json: %d %s", rec.Code, rec.Body)
	}
	rec = call(mux, http.MethodPost, "/api/user/vm", `{"os":"windows","url":"wss://vm.example/","token":"t"}`, "application/json")
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "api key required") {
		t.Fatalf("missing key: %d %s", rec.Code, rec.Body)
	}

	user := createKey(t, mux, "user", "alice")
	worker := createKey(t, mux, "worker", "bob")
	other := createKey(t, mux, "user", "carol")

	rec = callAuth(mux, http.MethodPost, "/api/user/vm", `{"os":"nope!","url":"wss://vm.example/","token":"t"}`, user.Token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad os: %d %s", rec.Code, rec.Body)
	}
	rec = callAuth(mux, http.MethodPost, "/api/user/vm", `{"os":"windows","url":"wss://vm.example/path/?q=1","token":"auth-token"}`, user.Token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created struct{ UID string }
	decode(t, rec, &created)

	rec = callAuth(mux, http.MethodGet, "/api/user/vm/"+created.UID, "", other.Token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other user: %d", rec.Code)
	}
	rec = callAuth(mux, http.MethodGet, "/api/worker/vm?os=linux", "", worker.Token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("empty claim: %d %s", rec.Code, rec.Body)
	}
	rec = callAuth(mux, http.MethodGet, "/api/worker/vm?os=windows", "", worker.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", rec.Code, rec.Body)
	}
	var claimed map[string]any
	decode(t, rec, &claimed)
	if claimed["status"] != "pending" || claimed["url"] != "wss://vm.example/path/?q=1" || claimed["token"] != "auth-token" {
		t.Fatalf("claimed %#v", claimed)
	}
	rec = callAuth(mux, http.MethodPut, "/api/worker/vm/windows/"+created.UID, `{"status":"nope"}`, worker.Token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d %s", rec.Code, rec.Body)
	}
	rec = callAuth(mux, http.MethodPut, "/api/worker/vm/windows/"+created.UID, `{"status":"success","host":"10.0.0.8"}`, worker.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("finish: %d %s", rec.Code, rec.Body)
	}

	rec = callAuth(mux, http.MethodPost, "/api/user/access/"+created.UID, `{"url":"https://vm.example/path/","token":"auth-token","username":"Administrator","password":"s3cret","key":"0123456789abcdef0123456789abcdef"}`, user.Token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("access: %d %s", rec.Code, rec.Body)
	}
	var login struct{ Username, Password string }
	decode(t, rec, &login)
	rec = callAuth(mux, http.MethodPost, "/api/user/access/"+created.UID, `{"url":"https://vm.example/path/","token":"auth-token","username":"Administrator","password":"s3cret","key":"0123456789abcdef0123456789abcdef"}`, user.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("access update: %d %s", rec.Code, rec.Body)
	}
	var again struct{ Username, Password string }
	decode(t, rec, &again)
	if again.Username != login.Username || again.Password != login.Password {
		t.Fatalf("login changed")
	}

	rec = callAuth(mux, http.MethodGet, "/api/user/vm/"+created.UID, "", user.Token)
	var got map[string]any
	decode(t, rec, &got)
	if got["status"] != "success" || got["host"] != "10.0.0.8" || got["username"] != login.Username || got["password"] != login.Password {
		t.Fatalf("user view %#v", got)
	}

	rec = call(mux, http.MethodGet, "/microvm?username="+url.QueryEscape(login.Username)+"&password="+url.QueryEscape(login.Password), "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "wss://vm.example/path/websocket") || !strings.Contains(rec.Body.String(), "lambda-microvms.authentication.auth-token") {
		t.Fatalf("desktop: %d %s", rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}
	if strings.Contains(rec.Body.String(), "__TOKEN_JSON__") {
		t.Fatal("token placeholder left in page")
	}

	rec = callAuth(mux, http.MethodPost, "/api/worker/deleted", `["missing", "`+created.UID+`", "`+created.UID+`"]`, worker.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("deleted before: %d %s", rec.Code, rec.Body)
	}
	var pile []string
	decode(t, rec, &pile)
	if len(pile) != 0 {
		t.Fatalf("pile %#v", pile)
	}
	rec = callAuth(mux, http.MethodDelete, "/api/user/vm/"+created.UID, "", user.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	rec = callAuth(mux, http.MethodPost, "/api/worker/deleted", `["missing","`+created.UID+`","`+created.UID+`"]`, worker.Token)
	decode(t, rec, &pile)
	if len(pile) != 1 || pile[0] != created.UID {
		t.Fatalf("pile after %#v", pile)
	}

	rec = call(mux, http.MethodGet, "/queue", "", "")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("queue redirect: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = call(mux, http.MethodPost, "/api/admin/login", `{"username":"admin","password":"secret"}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	cookie := rec.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.Name != "admin" {
		t.Fatalf("cookie %#v", cookie)
	}
	req := httptest.NewRequest(http.MethodGet, "/queue", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Queue") {
		t.Fatalf("queue page: %d", rec.Code)
	}
	rec = call(mux, http.MethodGet, "/launch-student", "", "")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("launch student redirect: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	req = httptest.NewRequest(http.MethodGet, "/launch-student", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Launch Student") || !strings.Contains(rec.Body.String(), ">Launch<") || strings.Contains(rec.Body.String(), "<table") {
		t.Fatalf("launch student page: %d %s", rec.Code, rec.Body)
	}
	req = httptest.NewRequest(http.MethodGet, "/style.css", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "--bg") {
		t.Fatalf("css: %d", rec.Code)
	}
}

type scriptedVMs struct {
	runSnap microvmSnapshot
	getSnap microvmSnapshot
	token   string
	runErr  error
	runs    int
}

func (s *scriptedVMs) Run(context.Context) (microvmSnapshot, error) {
	s.runs++
	if s.runErr != nil {
		return microvmSnapshot{}, s.runErr
	}
	return s.runSnap, nil
}

func (s *scriptedVMs) Get(_ context.Context, id string) (microvmSnapshot, error) {
	snap := s.getSnap
	if snap.ID == "" {
		snap.ID = id
	}
	return snap, nil
}

func (s *scriptedVMs) AuthToken(context.Context, string) (string, error) {
	return s.token, nil
}

func TestLaunchStudent(t *testing.T) {
	app := testApp(t)
	mux := app.routes()
	rec := call(mux, http.MethodPost, "/api/admin/launch", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon launch: %d %s", rec.Code, rec.Body)
	}
	rec = call(mux, http.MethodPost, "/api/admin/login", `{"username":"admin","password":"secret"}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	cookie := rec.Result().Cookies()[0]
	post := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.AddCookie(cookie)
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		return out
	}
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		return out
	}
	rec = post("/api/admin/launch")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured: %d %s", rec.Code, rec.Body)
	}
	blocked := &scriptedVMs{runSnap: microvmSnapshot{ID: "mvm-blocked", State: "RUNNING"}}
	app.vms = blocked
	rec = post("/api/admin/launch")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "GUAC_RDP_USER") || blocked.runs != 0 {
		t.Fatalf("missing desktop login: %d %s runs=%d", rec.Code, rec.Body, blocked.runs)
	}
	t.Setenv("GUAC_RDP_USER", "Administrator")
	t.Setenv("GUAC_RDP_PASSWORD", "s3cret")
	app.vms = &scriptedVMs{runErr: errors.New("denied")}
	rec = post("/api/admin/launch")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("failed launch: %d %s", rec.Code, rec.Body)
	}
	listRec := get("/api/admin/microvms")
	var empty []microvmRecord
	decode(t, listRec, &empty)
	if len(empty) != 0 {
		t.Fatalf("stored a failed launch %#v", empty)
	}
	app.vms = &scriptedVMs{
		runSnap: microvmSnapshot{
			ID:       "mvm-1",
			Endpoint: "abc.lambda-microvm.us-east-1.on.aws",
			Region:   "us-east-1",
			State:    "PENDING",
			ImageArn: "arn:aws:lambda:us-east-1:123:microvm-image:helix",
			Key:      "0123456789abcdef0123456789abcdef",
		},
		getSnap: microvmSnapshot{
			Endpoint:  "abc.lambda-microvm.us-east-1.on.aws",
			Region:    "us-east-1",
			State:     "RUNNING",
			StartedAt: "2023-11-14T22:13:20Z",
			ImageArn:  "arn:aws:lambda:us-east-1:123:microvm-image:helix",
		},
		token: "tok-1",
	}
	rec = post("/api/admin/launch")
	if rec.Code != http.StatusCreated {
		t.Fatalf("launch: %d %s", rec.Code, rec.Body)
	}
	var launched microvmRecord
	decode(t, rec, &launched)
	if launched.ID != "mvm-1" || launched.State != "PENDING" || launched.URL != "wss://abc.lambda-microvm.us-east-1.on.aws/" || launched.Token != "tok-1" || launched.Key == "" || launched.UID != "" {
		t.Fatalf("launched %#v", launched)
	}
	rec = get("/api/admin/microvms/mvm-1")
	var refreshed microvmRecord
	decode(t, rec, &refreshed)
	if rec.Code != http.StatusOK || refreshed.State != "RUNNING" || refreshed.Token != "tok-1" || refreshed.StartedAt != "2023-11-14T22:13:20Z" || refreshed.CreatedAt != launched.CreatedAt {
		t.Fatalf("refresh %d %#v", rec.Code, refreshed)
	}
	if refreshed.UID == "" || !strings.HasPrefix(refreshed.Viewer, "/microvm?username=") {
		t.Fatalf("desktop %#v", refreshed)
	}
	queued := app.store.ListRequests()
	if len(queued) != 1 || queued[0].OS != "windows" || queued[0].Status != "queued" || queued[0].UID != refreshed.UID {
		t.Fatalf("queue %#v", queued)
	}
	if queued[0].Result["url"] != refreshed.URL || queued[0].Result["token"] != "tok-1" || queued[0].Viewer != refreshed.Viewer {
		t.Fatalf("queued desktop %#v", queued[0])
	}
	access := app.store.GetAccess(refreshed.UID)
	if access == nil || access.RDPUser != "Administrator" || access.RDPPass != "s3cret" || access.Key != launched.Key || access.Token != "tok-1" {
		t.Fatalf("access %#v", access)
	}
	rec = get("/api/admin/microvms/mvm-1")
	var again microvmRecord
	decode(t, rec, &again)
	if again.UID != refreshed.UID || len(app.store.ListRequests()) != 1 {
		t.Fatalf("queued twice %#v", again)
	}
	var list []microvmRecord
	decode(t, get("/api/admin/microvms"), &list)
	if len(list) != 1 || list[0].State != "RUNNING" || list[0].ID != "mvm-1" || list[0].Viewer != refreshed.Viewer {
		t.Fatalf("list %#v", list)
	}
	app.vms = &scriptedVMs{
		runSnap: microvmSnapshot{
			ID:       "mvm-nokey",
			Endpoint: "nokey.lambda-microvm.us-east-1.on.aws",
			Region:   "us-east-1",
			State:    "RUNNING",
		},
		token: "tok-2",
	}
	rec = post("/api/admin/launch")
	var nokey microvmRecord
	decode(t, rec, &nokey)
	if rec.Code != http.StatusCreated || nokey.DesktopError == "" || nokey.UID != "" || len(app.store.ListRequests()) != 1 {
		t.Fatalf("missing key: %d %#v", rec.Code, nokey)
	}
}

func TestCryptMatchesNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	key := "0123456789abcdef0123456789abcdef"
	token, err := encryptToken(key, map[string]any{
		"connection": map[string]any{
			"type": "rdp",
			"settings": map[string]any{
				"hostname": "127.0.0.1",
				"password": `p&<ass"word`,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", "-e", `
		const crypto = require("crypto");
		const [token, key] = process.argv.slice(1);
		const outer = JSON.parse(Buffer.from(token, "base64").toString("utf8"));
		const iv = Buffer.from(outer.iv, "base64");
		const value = Buffer.from(outer.value, "base64");
		const decipher = crypto.createDecipheriv("aes-256-cbc", Buffer.from(key), iv);
		process.stdout.write(Buffer.concat([decipher.update(value), decipher.final()]));
	`, token, key)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err, string(out))
	}
	conn := payload["connection"].(map[string]any)
	settings := conn["settings"].(map[string]any)
	if conn["type"] != "rdp" || settings["hostname"] != "127.0.0.1" || settings["password"] != `p&<ass"word` {
		t.Fatalf("payload %#v", payload)
	}
}

func TestWebSocketURL(t *testing.T) {
	cases := map[string]string{
		"https://vm.example/path/?q=1#x": "wss://vm.example/path/websocket",
		"wss://vm.example/websocket/":    "wss://vm.example/websocket",
		"http://vm.example":              "ws://vm.example/websocket",
		"ftp://vm.example":               "",
		"not a url":                      "",
	}
	for raw, want := range cases {
		if got := microvmWebSocketURL(raw); got != want {
			t.Errorf("%s: got %q want %q", raw, got, want)
		}
	}
}

func TestReload(t *testing.T) {
	store := testStore(t)
	uid, err := store.CreateRequest("key_abc", "windows", "wss://vm.example/", "tok")
	if err != nil {
		t.Fatal(err)
	}
	again, err := openStore(storeConfig{Table: store.table, Region: "us-east-1", Endpoint: store.endpoint})
	if err != nil {
		t.Fatal(err)
	}
	body, found := again.GetForUser("key_abc", uid)
	if !found || body["token"] != "tok" || body["status"] != "queued" {
		t.Fatalf("reloaded %#v", body)
	}
}

type issuedKey struct {
	Token string `json:"token"`
	ID    string `json:"id"`
}

func createKey(t *testing.T, mux http.Handler, role, name string) issuedKey {
	t.Helper()
	app := mux
	rec := call(app, http.MethodPost, "/api/admin/login", `{"username":"admin","password":"secret"}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	cookie := rec.Result().Cookies()[0]
	req := httptest.NewRequest(http.MethodPost, "/api/admin/keys", strings.NewReader(`{"role":"`+role+`","name":"`+name+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", rec.Code, rec.Body)
	}
	var key issuedKey
	decode(t, rec, &key)
	return key
}

func call(mux http.Handler, method, path, body, contentType string) *httptest.ResponseRecorder {
	return callAuthType(mux, method, path, body, "", contentType)
}

func callAuth(mux http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	return callAuthType(mux, method, path, body, token, "application/json")
}

func callAuthType(mux http.Handler, method, path, body, token, contentType string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" && contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, dest any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dest); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
}
