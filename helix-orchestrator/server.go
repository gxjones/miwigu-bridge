package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	jsonLimit = 32 << 10
	formLimit = 8 << 10
)

var (
	osName     = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9._-]{0,31}$`)
	bearerAuth = regexp.MustCompile(`(?i)^Bearer\s+(\S+)$`)
)

//go:embed public
var publicFS embed.FS

type server struct {
	adminUser string
	adminPass string
	passHash  [32]byte
	store     *Store
	vms       microvmAPI
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /api/admin/login", s.login)
	mux.HandleFunc("POST /api/user/vm", s.createVM)
	mux.HandleFunc("DELETE /api/user/vm/{uid}", s.deleteVM)
	mux.HandleFunc("GET /api/user/vm/{uid}", s.getVM)
	mux.HandleFunc("POST /api/worker/deleted", s.deleted)
	mux.HandleFunc("GET /api/worker/vm", s.claimVM)
	mux.HandleFunc("PUT /api/worker/vm/{os}/{uid}", s.finishVM)
	mux.HandleFunc("POST /api/user/access/{uid}", s.publishAccess)
	mux.HandleFunc("GET /microvm", s.microvm)
	mux.HandleFunc("POST /microvm", s.microvm)

	mux.HandleFunc("POST /api/admin/logout", s.admin(s.logout))
	mux.HandleFunc("GET /api/admin/keys", s.admin(s.listKeys))
	mux.HandleFunc("POST /api/admin/keys", s.createKey)
	mux.HandleFunc("DELETE /api/admin/keys/{id}", s.admin(s.deleteKey))
	mux.HandleFunc("GET /api/admin/queue", s.admin(s.queue))
	mux.HandleFunc("POST /api/admin/launch", s.admin(s.launch))
	mux.HandleFunc("GET /api/admin/microvms", s.admin(s.listMicrovms))
	mux.HandleFunc("GET /api/admin/microvms/{id}", s.admin(s.getMicrovm))
	mux.HandleFunc("GET /{$}", s.admin(s.home))
	mux.HandleFunc("GET /queue", s.admin(s.page("queue.html")))
	mux.HandleFunc("GET /keys", s.admin(s.page("keys.html")))
	mux.HandleFunc("GET /launch", s.admin(s.page("launch.html")))

	sub, err := fs.Sub(publicFS, "public")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("GET /", s.adminH(http.FileServer(http.FS(sub))))
	return mux
}

func (s *server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.adminOK(r) {
			next(w, r)
			return
		}
		if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api") {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
	}
}

func (s *server) adminH(next http.Handler) http.Handler {
	return http.HandlerFunc(s.admin(next.ServeHTTP))
}

func (s *server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.adminOK(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	writePublic(w, "login.html")
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	body, ok := publicBody(w, r)
	if !ok {
		return
	}
	fields, _ := body.(map[string]any)
	userHash := sha256.Sum256([]byte(asString(fields["username"])))
	adminHash := sha256.Sum256([]byte(s.adminUser))
	passHash := sha256.Sum256([]byte(asString(fields["password"])))
	userOK := subtle.ConstantTimeCompare(userHash[:], adminHash[:]) == 1
	passOK := subtle.ConstantTimeCompare(passHash[:], s.passHash[:]) == 1
	if !userOK || !passOK {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid login"})
		return
	}
	exp := time.Now().Unix() + sessionSeconds
	http.SetCookie(w, sessionCookie("admin", sign(s.adminPass, "admin."+strconv.FormatInt(exp, 10)), sessionSeconds, secureReq(r)))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, sessionCookie("admin", "", -1, secureReq(r)))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) createVM(w http.ResponseWriter, r *http.Request) {
	body, key, ok := s.publicJSON(w, r, "user")
	if !ok {
		return
	}
	fields, _ := body.(map[string]any)
	name := strings.TrimSpace(asString(fields["os"]))
	rawURL := strings.TrimSpace(asString(fields["url"]))
	token := strings.TrimSpace(asString(fields["token"]))
	if !osName.MatchString(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "os is required"})
		return
	}
	if rawURL == "" || token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url and token are required"})
		return
	}
	uid, err := s.store.CreateRequest(key.ID, name, rawURL, token)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"uid": uid})
}

func (s *server) deleteVM(w http.ResponseWriter, r *http.Request) {
	key, ok := s.requireKey(w, r, "user")
	if !ok {
		return
	}
	uid, found, err := s.store.DeleteForUser(key.ID, r.PathValue("uid"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"uid": uid})
}

func (s *server) getVM(w http.ResponseWriter, r *http.Request) {
	key, ok := s.requireKey(w, r, "user")
	if !ok {
		return
	}
	body, found := s.store.GetForUser(key.ID, r.PathValue("uid"))
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *server) deleted(w http.ResponseWriter, r *http.Request) {
	body, _, ok := s.publicJSON(w, r, "worker")
	if !ok {
		return
	}
	list, isList := body.([]any)
	if !isList {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be a list of uids"})
		return
	}
	uids := make([]string, 0, len(list))
	for _, item := range list {
		text, isString := item.(string)
		if !isString {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be a list of uids"})
			return
		}
		uids = append(uids, text)
	}
	writeJSON(w, http.StatusOK, s.store.DeletedAmong(uids))
}

func (s *server) claimVM(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireKey(w, r, "worker"); !ok {
		return
	}
	name := r.URL.Query().Get("os")
	if name != "" && !osName.MatchString(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid os"})
		return
	}
	item, err := s.store.Dequeue(name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed"})
		return
	}
	if item == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no queued request"})
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *server) finishVM(w http.ResponseWriter, r *http.Request) {
	body, _, ok := s.publicJSON(w, r, "worker")
	if !ok {
		return
	}
	fields, isMap := body.(map[string]any)
	if !isMap {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON object required"})
		return
	}
	item, err := s.store.Update(r.PathValue("os"), r.PathValue("uid"), fields)
	if errors.Is(err, errStatus) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errStatus.Error()})
		return
	}
	if errors.Is(err, errMissing) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed"})
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *server) publishAccess(w http.ResponseWriter, r *http.Request) {
	body, key, ok := s.publicJSON(w, r, "user")
	if !ok {
		return
	}
	fields, _ := body.(map[string]any)
	rawURL := strings.TrimSpace(asString(fields["url"]))
	token := strings.TrimSpace(asString(fields["token"]))
	username := strings.TrimSpace(asString(fields["username"]))
	password := asString(fields["password"])
	cryptKey := asString(fields["key"])
	if microvmWebSocketURL(rawURL) == "" || token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url and token are required"})
		return
	}
	if username == "" || password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username and password are required"})
		return
	}
	if len(cryptKey) != 32 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key must be 32 bytes"})
		return
	}
	saved, created, err := s.store.SaveAccess(key.ID, r.PathValue("uid"), accessRow{
		URL: rawURL, Token: token, RDPUser: username, RDPPass: password, Key: cryptKey,
	})
	if errors.Is(err, errMissing) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed"})
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, map[string]string{"username": saved.Username, "password": saved.Password})
}

func (s *server) microvm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, formLimit)
		if err := r.ParseForm(); err != nil {
			writeAccessLogin(w, http.StatusUnauthorized, "invalid login", "", "")
			return
		}
		user, pass := r.PostFormValue("username"), r.PostFormValue("password")
		row := s.store.Authenticate(user, pass)
		if row == nil {
			writeAccessLogin(w, http.StatusUnauthorized, "invalid login", user, pass)
			return
		}
		s.setAccessSession(w, r, row.UID)
		s.sendDesktop(w, row, r.PostFormValue("width"), r.PostFormValue("height"))
		return
	}
	user, pass := r.URL.Query().Get("username"), r.URL.Query().Get("password")
	if user != "" && pass != "" {
		row := s.store.Authenticate(user, pass)
		if row == nil {
			clearAccessSession(w)
			writeAccessLogin(w, http.StatusUnauthorized, "invalid login", user, pass)
			return
		}
		s.setAccessSession(w, r, row.UID)
		s.sendDesktop(w, row, "", "")
		return
	}
	row := s.store.GetAccess(s.accessUID(r))
	if row == nil {
		writeAccessLogin(w, http.StatusOK, "", user, pass)
		return
	}
	s.sendDesktop(w, row, "", "")
}

func (s *server) sendDesktop(w http.ResponseWriter, row *accessRow, widthRaw, heightRaw string) {
	ws := microvmWebSocketURL(row.URL)
	if ws == "" || row.Token == "" || len(row.Key) != 32 {
		writeAccessLogin(w, http.StatusConflict, "This desktop is not available.", "", "")
		return
	}
	width := clampDim(widthRaw, 1280, 200, 4096)
	height := clampDim(heightRaw, 720, 200, 2160)
	token, err := encryptToken(row.Key, map[string]any{
		"connection": map[string]any{
			"type": "rdp",
			"settings": map[string]any{
				"hostname":         "127.0.0.1",
				"port":             "3389",
				"username":         row.RDPUser,
				"password":         row.RDPPass,
				"security":         "any",
				"ignore-cert":      "true",
				"resize-method":    "display-update",
				"enable-wallpaper": "false",
				"width":            strconv.Itoa(width),
				"height":           strconv.Itoa(height),
			},
		},
	})
	if err != nil {
		writeAccessLogin(w, http.StatusConflict, "This desktop is not available.", "", "")
		return
	}
	page, err := renderSession(token, ws, []string{
		"lambda-microvms",
		"lambda-microvms.authentication." + row.Token,
		"lambda-microvms.port." + strconv.Itoa(guestPort),
		"guacamole",
	})
	if err != nil {
		writeAccessLogin(w, http.StatusConflict, "This desktop is not available.", "", "")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}

func (s *server) listKeys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.ListKeys())
}

func (s *server) createKey(w http.ResponseWriter, r *http.Request) {
	body, ok := s.adminJSON(w, r)
	if !ok {
		return
	}
	fields, _ := body.(map[string]any)
	role := asString(fields["role"])
	if role != "user" && role != "worker" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "role must be user or worker"})
		return
	}
	created, err := s.store.CreateKey(role, clipName(asString(fields["name"])))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed"})
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *server) deleteKey(w http.ResponseWriter, r *http.Request) {
	found, err := s.store.DeleteKey(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) queue(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.ListRequests())
}

func (s *server) launch(w http.ResponseWriter, r *http.Request) {
	if s.vms == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "microvm image is not configured"})
		return
	}
	if user, pass := desktopCreds(); user == "" || pass == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "GUAC_RDP_USER and GUAC_RDP_PASSWORD are required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	snap, err := s.vms.Run(ctx)
	if err != nil || !validMicrovmID(snap.ID) {
		log.Printf("launch: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not start the microvm"})
		return
	}
	rec, err := s.store.SaveMicrovm(recordFromSnapshot(snap))
	if err != nil {
		log.Printf("launch: store %s: %v", snap.ID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not store the microvm"})
		return
	}
	if rec.Token == "" {
		token, tokenErr := s.vms.AuthToken(ctx, rec.ID)
		if tokenErr != nil {
			log.Printf("launch: auth token %s: %v", rec.ID, tokenErr)
		} else if token != "" {
			rec.Token = token
			saved, saveErr := s.store.SaveMicrovm(rec)
			if saveErr != nil {
				log.Printf("launch: store token %s: %v", rec.ID, saveErr)
			} else {
				rec = saved
			}
		}
	}
	rec = s.ensureDesktop(rec)
	log.Printf("launch: %s %s", rec.ID, rec.State)
	writeJSON(w, http.StatusCreated, rec)
}

func (s *server) listMicrovms(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.store.ListMicrovms()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list microvms"})
		return
	}
	if rows == nil {
		rows = []microvmRecord{}
	}
	for i := range rows {
		rows[i] = s.withViewer(rows[i])
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *server) getMicrovm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, ok, err := s.store.GetMicrovm(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the microvm"})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if s.vms == nil || rec.State == "TERMINATED" {
		writeJSON(w, http.StatusOK, s.withViewer(rec))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	snap, err := s.vms.Get(ctx, id)
	if err != nil {
		log.Printf("launch: get %s: %v", id, err)
		writeJSON(w, http.StatusOK, s.withViewer(rec))
		return
	}
	next := applySnapshot(rec, snap)
	if next.State == "RUNNING" && next.Token == "" {
		token, tokenErr := s.vms.AuthToken(ctx, id)
		if tokenErr != nil {
			log.Printf("launch: auth token %s: %v", id, tokenErr)
		} else if token != "" {
			next.Token = token
		}
	}
	next = s.ensureDesktop(next)
	if microvmChanged(rec, next) {
		saved, saveErr := s.store.SaveMicrovm(next)
		if saveErr != nil {
			log.Printf("launch: store %s: %v", id, saveErr)
		} else {
			next = saved
		}
	}
	writeJSON(w, http.StatusOK, next)
}

// launchOwner is the user-key id stored on a desktop queued from the launch page.
// The worker claim does not check it. The admin queue lists the request.
const launchOwner = "launch"

func desktopCreds() (string, string) {
	return strings.TrimSpace(os.Getenv("GUAC_RDP_USER")), os.Getenv("GUAC_RDP_PASSWORD")
}

// ensureDesktop queues a Windows VM and publishes its desktop once the MicroVM
// is RUNNING. That is the same sequence as scripts/spawn.py: websocket URL,
// auth token, RDP login, and the guacamole key.
func (s *server) ensureDesktop(rec microvmRecord) microvmRecord {
	rec = s.withViewer(rec)
	if rec.Viewer != "" || rec.State != "RUNNING" {
		return rec
	}
	user, pass := desktopCreds()
	ws := rec.URL
	if ws == "" {
		ws = websocketURL(rec.Endpoint)
	}
	switch {
	case user == "" || pass == "":
		rec.DesktopError = "GUAC_RDP_USER and GUAC_RDP_PASSWORD are required"
		return rec
	case rec.Token == "" || ws == "":
		return rec
	case len(rec.Key) != 32:
		rec.DesktopError = "desktop key is missing"
		return rec
	}
	if rec.UID == "" {
		fresh, ok, err := s.store.GetMicrovm(rec.ID)
		if err != nil {
			log.Printf("launch: read %s: %v", rec.ID, err)
			return rec
		}
		if ok && fresh.UID != "" {
			rec.UID = fresh.UID
			rec = s.withViewer(rec)
			if rec.Viewer != "" {
				return rec
			}
		}
	}
	if rec.UID == "" {
		uid, err := s.store.CreateRequest(launchOwner, "windows", ws, rec.Token)
		if err != nil {
			log.Printf("launch: queue %s: %v", rec.ID, err)
			return rec
		}
		rec.UID = uid
		saved, err := s.store.SaveMicrovm(rec)
		if err != nil {
			log.Printf("launch: store uid %s: %v", rec.ID, err)
			return rec
		}
		rec = saved
	}
	if s.store.GetAccess(rec.UID) == nil {
		_, _, err := s.store.SaveAccess(launchOwner, rec.UID, accessRow{
			URL: ws, Token: rec.Token, RDPUser: user, RDPPass: pass, Key: rec.Key,
		})
		if err != nil {
			log.Printf("launch: access %s: %v", rec.ID, err)
			if errors.Is(err, errMissing) {
				rec.DesktopError = "could not publish the desktop"
			}
			return rec
		}
	}
	return s.withViewer(rec)
}

func (s *server) withViewer(rec microvmRecord) microvmRecord {
	if rec.UID == "" || rec.Viewer != "" {
		return rec
	}
	access := s.store.GetAccess(rec.UID)
	if access == nil {
		return rec
	}
	rec.Viewer = viewerPath(access.Username, access.Password)
	return rec
}

func recordFromSnapshot(snap microvmSnapshot) microvmRecord {
	return microvmRecord{
		ID:           snap.ID,
		Endpoint:     snap.Endpoint,
		Region:       snap.Region,
		State:        snap.State,
		StateReason:  snap.StateReason,
		ImageArn:     snap.ImageArn,
		ImageVersion: snap.ImageVersion,
		StartedAt:    snap.StartedAt,
		Key:          snap.Key,
	}
}

func applySnapshot(rec microvmRecord, snap microvmSnapshot) microvmRecord {
	if snap.Endpoint != "" {
		rec.Endpoint = snap.Endpoint
	}
	if snap.Region != "" {
		rec.Region = snap.Region
	}
	if snap.State != "" {
		rec.State = snap.State
	}
	if snap.StateReason != "" {
		rec.StateReason = snap.StateReason
	}
	if snap.ImageArn != "" {
		rec.ImageArn = snap.ImageArn
	}
	if snap.ImageVersion != "" {
		rec.ImageVersion = snap.ImageVersion
	}
	if snap.StartedAt != "" {
		rec.StartedAt = snap.StartedAt
	}
	if snap.Key != "" {
		rec.Key = snap.Key
	}
	return rec
}

func microvmChanged(before, after microvmRecord) bool {
	return before.Endpoint != after.Endpoint ||
		before.Region != after.Region ||
		before.State != after.State ||
		before.StateReason != after.StateReason ||
		before.ImageArn != after.ImageArn ||
		before.ImageVersion != after.ImageVersion ||
		before.StartedAt != after.StartedAt ||
		before.Token != after.Token ||
		before.Key != after.Key ||
		before.UID != after.UID
}

func (s *server) home(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/queue", http.StatusFound)
}

func (s *server) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writePublic(w, name)
	}
}

func (s *server) publicJSON(w http.ResponseWriter, r *http.Request, role string) (any, storedKey, bool) {
	body, ok := publicBody(w, r)
	if !ok {
		return nil, storedKey{}, false
	}
	key, ok := s.requireKey(w, r, role)
	if !ok {
		return nil, storedKey{}, false
	}
	return body, key, true
}

func (s *server) adminJSON(w http.ResponseWriter, r *http.Request) (any, bool) {
	body, ok, parsed := decodeIfJSON(w, r)
	if parsed && !ok {
		return nil, false
	}
	if !s.adminOK(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
		return nil, false
	}
	if !parsed {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
		return nil, false
	}
	return body, true
}

func publicBody(w http.ResponseWriter, r *http.Request) (any, bool) {
	body, ok, parsed := decodeIfJSON(w, r)
	if parsed {
		if !ok {
			return nil, false
		}
		return body, true
	}
	writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
	return nil, false
}

func (s *server) requireKey(w http.ResponseWriter, r *http.Request, role string) (storedKey, bool) {
	token := bearer(r)
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "api key required"})
		return storedKey{}, false
	}
	key, ok := s.store.FindKey(token)
	if !ok || key.Role != role {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid api key"})
		return storedKey{}, false
	}
	return key, true
}

func decodeIfJSON(w http.ResponseWriter, r *http.Request) (any, bool, bool) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, false, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, jsonLimit)
	dec := json.NewDecoder(r.Body)
	var body any
	err = dec.Decode(&body)
	if err == io.EOF {
		return map[string]any{}, true, true
	}
	if err != nil || dec.More() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return nil, false, true
	}
	return body, true, true
}

func bearer(r *http.Request) string {
	match := bearerAuth.FindStringSubmatch(r.Header.Get("Authorization"))
	if match == nil {
		return ""
	}
	return match[1]
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(body)
}

func writePublic(w http.ResponseWriter, name string) {
	body, err := publicFS.ReadFile("public/" + name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

func clipName(name string) string {
	name = strings.TrimSpace(name)
	runes := []rune(name)
	if len(runes) > 64 {
		return string(runes[:64])
	}
	return name
}
