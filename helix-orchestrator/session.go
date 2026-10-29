package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	sessionSeconds = 12 * 60 * 60
	guestPort      = 8081
)

//go:embed session.html
var sessionPage string

func (s *server) adminOK(r *http.Request) bool {
	value, ok := signedCookie(r, "admin", s.adminPass)
	if !ok {
		return false
	}
	user, exp, found := strings.Cut(value, ".")
	if !found || user != "admin" {
		return false
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return true
	}
	return n >= time.Now().Unix()
}

func (s *server) accessUID(r *http.Request) string {
	value, ok := signedCookie(r, "microvm", s.adminPass)
	if !ok {
		return ""
	}
	uid, exp, found := strings.Cut(value, ".")
	if !found || uid == "" {
		return ""
	}
	// The uid itself can contain dots; the expiry is the final field.
	if dot := strings.LastIndex(value, "."); dot > 0 {
		uid = value[:dot]
		exp = value[dot+1:]
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || n < time.Now().Unix() {
		return ""
	}
	return uid
}

func (s *server) setAccessSession(w http.ResponseWriter, r *http.Request, uid string) {
	clearAccessSession(w)
	exp := time.Now().Unix() + sessionSeconds
	http.SetCookie(w, sessionCookie("microvm", sign(s.adminPass, uid+"."+strconv.FormatInt(exp, 10)), sessionSeconds, secureReq(r)))
}

func secureReq(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func signedCookie(r *http.Request, name, secret string) (string, bool) {
	cookie, err := r.Cookie(name)
	if err != nil {
		return "", false
	}
	return unsign(secret, cookie.Value)
}

func sessionCookie(name, value string, maxAge int, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	}
}

func clearAccessSession(w http.ResponseWriter) {
	http.SetCookie(w, sessionCookie("microvm", "", -1, false))
	http.SetCookie(w, sessionCookie("microvm", "", -1, true))
}

func sign(secret, value string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(value))
	return value + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func unsign(secret, token string) (string, bool) {
	dot := strings.LastIndex(token, ".")
	if dot <= 0 {
		return "", false
	}
	value, mac := token[:dot], token[dot+1:]
	expected := sign(secret, value)
	want := expected[strings.LastIndex(expected, ".")+1:]
	if len(mac) != len(want) || subtle.ConstantTimeCompare([]byte(mac), []byte(want)) != 1 {
		return "", false
	}
	return value, true
}

func clampDim(raw string, fallback, low, high int) int {
	size := float64(fallback)
	if raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
			size = parsed
		}
	}
	rounded := int(math.Round(size))
	if rounded < low {
		return low
	}
	if rounded > high {
		return high
	}
	return rounded
}

func microvmWebSocketURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	case "ws", "wss":
	default:
		return ""
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/websocket") {
		path += "/websocket"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	parsed.Path = path
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func encryptToken(key string, payload any) (string, error) {
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	plain = pkcs7Pad(plain, aes.BlockSize)
	iv := make([]byte, aes.BlockSize)
	if _, err = rand.Read(iv); err != nil {
		return "", err
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(plain, plain)
	wrapped, err := json.Marshal(struct {
		IV    string `json:"iv"`
		Value string `json:"value"`
	}{
		IV:    base64.StdEncoding.EncodeToString(iv),
		Value: base64.StdEncoding.EncodeToString(plain),
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(wrapped), nil
}

func pkcs7Pad(in []byte, size int) []byte {
	pad := size - len(in)%size
	return append(in, bytesRepeat(byte(pad), pad)...)
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func renderSession(token, ws string, protocols []string) (string, error) {
	tokenJSON, err := embedJSON(token)
	if err != nil {
		return "", err
	}
	wsJSON, err := embedJSON(ws)
	if err != nil {
		return "", err
	}
	protocolJSON, err := embedJSON(protocols)
	if err != nil {
		return "", err
	}
	return strings.NewReplacer(
		"__TOKEN_JSON__", tokenJSON,
		"__WS_URL_JSON__", wsJSON,
		"__PROTOCOLS_JSON__", protocolJSON,
	).Replace(sessionPage), nil
}

func embedJSON(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	text := string(body)
	text = strings.ReplaceAll(text, "<", `\u003c`)
	text = strings.ReplaceAll(text, ">", `\u003e`)
	text = strings.ReplaceAll(text, "&", `\u0026`)
	text = strings.ReplaceAll(text, "\u2028", `\u2028`)
	text = strings.ReplaceAll(text, "\u2029", `\u2029`)
	return text, nil
}

func writeAccessLogin(w http.ResponseWriter, code int, message, username, password string) {
	page := strings.NewReplacer(
		"__ERROR__", html.EscapeString(message),
		"__USER__", html.EscapeString(username),
		"__PASS__", html.EscapeString(password),
	).Replace(accessLoginPage)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(page))
}

const accessLoginPage = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>MicroVM RDP</title>
  <style>
    :root { color-scheme: dark; --bg: #121418; --panel: #1c2028; --line: #2d3340; --text: #e7ebf2; --muted: #9aa3b2; --accent: #7eb6ff; --danger: #ff8d8d; }
    * { box-sizing: border-box; }
    body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: var(--bg); color: var(--text); font: 16px/1.45 "Segoe UI", sans-serif; }
    form { width: min(100% - 32px, 380px); background: var(--panel); border: 1px solid var(--line); border-radius: 14px; padding: 18px; }
    h1 { margin: 0; font-size: 28px; }
    p { margin: 4px 0 16px; color: var(--muted); }
    label { display: block; margin: 10px 0 4px; color: var(--muted); font-size: 13px; }
    input, button { font: inherit; width: 100%; }
    input { padding: 10px 12px; border-radius: 8px; border: 1px solid var(--line); background: #101318; color: var(--text); }
    button { margin-top: 16px; border: 0; border-radius: 8px; padding: 10px 14px; background: var(--accent); color: #0d1520; cursor: pointer; }
    .error { color: var(--danger); min-height: 1.2em; }
    .eyebrow { letter-spacing: .08em; text-transform: uppercase; font-size: 12px; margin: 0; }
  </style>
</head>
<body>
  <form method="post" action="/microvm">
    <p class="eyebrow">RDP</p>
    <h1>MicroVM</h1>
    <p>Sign in to this desktop.</p>
    <p class="error">__ERROR__</p>
    <label>Username <input name="username" autocomplete="username" required value="__USER__"></label>
    <label>Password <input name="password" type="password" autocomplete="current-password" required value="__PASS__"></label>
    <input type="hidden" name="width" value="1280">
    <input type="hidden" name="height" value="720">
    <button type="submit">Sign in</button>
  </form>
  <script>
    const form = document.querySelector('form');
    form.addEventListener('submit', () => {
      form.width.value = Math.max(200, Math.round(window.innerWidth));
      form.height.value = Math.max(200, Math.round(window.innerHeight));
    });
  </script>
</body>
</html>`
