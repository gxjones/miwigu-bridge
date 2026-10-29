package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
)

const (
	microvmAPIVersion    = "2025-09-09"
	defaultPeerPublicKey = "GHhBnw7IqG3OcOcjH7lE3xXVOsNLH64SWpofim1wkE8="
)

// microvmAPI starts a MicroVM and reads it back. Launch stores the result.
type microvmAPI interface {
	Run(ctx context.Context) (microvmSnapshot, error)
	Get(ctx context.Context, id string) (microvmSnapshot, error)
	AuthToken(ctx context.Context, id string) (string, error)
}

type microvmSnapshot struct {
	ID           string
	Endpoint     string
	Region       string
	State        string
	StateReason  string
	ImageArn     string
	ImageVersion string
	StartedAt    string
	Key          string
}

type awsMicrovms struct {
	region     string
	image      string
	ingress    string
	egress     string
	maxIdle    int
	suspended  int
	privateKey string
	peerKey    string
	http       *http.Client
	cfg        aws.Config
	signer     *v4.Signer
}

type idlePolicy struct {
	AutoResume bool `json:"autoResumeEnabled"`
	MaxIdle    int  `json:"maxIdleDurationSeconds"`
	Suspended  int  `json:"suspendedDurationSeconds"`
}

type runRequest struct {
	ClientToken     string      `json:"clientToken"`
	ImageIdentifier string      `json:"imageIdentifier"`
	Ingress         []string    `json:"ingressNetworkConnectors,omitempty"`
	Egress          []string    `json:"egressNetworkConnectors,omitempty"`
	Idle            *idlePolicy `json:"idlePolicy,omitempty"`
	RunHookPayload  string      `json:"runHookPayload,omitempty"`
}

type microvmBody struct {
	Endpoint     string          `json:"endpoint"`
	ImageArn     string          `json:"imageArn"`
	ImageVersion string          `json:"imageVersion"`
	MicrovmID    string          `json:"microvmId"`
	StartedAt    json.RawMessage `json:"startedAt"`
	State        string          `json:"state"`
	StateReason  string          `json:"stateReason"`
}

func openMicrovmAPI() (microvmAPI, error) {
	image := strings.TrimSpace(os.Getenv("MICROVM_IMAGE_ARN"))
	if image == "" {
		return nil, nil
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		return nil, errors.New("AWS_REGION is required to launch a microvm")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	maxIdle, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("MICROVM_MAX_IDLE_SECONDS")))
	suspended, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("MICROVM_SUSPENDED_SECONDS")))
	peer := strings.TrimSpace(os.Getenv("WG_PEER_PUBLIC_KEY"))
	if peer == "" {
		peer = defaultPeerPublicKey
	}
	return &awsMicrovms{
		region:     region,
		image:      image,
		ingress:    strings.TrimSpace(os.Getenv("MICROVM_INGRESS_CONNECTOR")),
		egress:     strings.TrimSpace(os.Getenv("MICROVM_EGRESS_CONNECTOR")),
		maxIdle:    maxIdle,
		suspended:  suspended,
		privateKey: strings.TrimSpace(os.Getenv("WG_SERVER_PRIVATE_KEY")),
		peerKey:    peer,
		http:       &http.Client{Timeout: 10 * time.Second},
		cfg:        cfg,
		signer:     v4.NewSigner(),
	}, nil
}

func (c *awsMicrovms) Run(ctx context.Context) (microvmSnapshot, error) {
	payload, guac, err := hookPayload(c.privateKey, c.peerKey)
	if err != nil {
		return microvmSnapshot{}, err
	}
	body := runRequest{
		ClientToken:     randHex(16),
		ImageIdentifier: c.image,
		RunHookPayload:  payload,
	}
	if c.ingress != "" {
		body.Ingress = []string{c.ingress}
	}
	if c.egress != "" {
		body.Egress = []string{c.egress}
	}
	if c.maxIdle > 0 && c.suspended > 0 {
		body.Idle = &idlePolicy{AutoResume: true, MaxIdle: c.maxIdle, Suspended: c.suspended}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return microvmSnapshot{}, err
	}
	var parsed microvmBody
	if err := c.call(ctx, http.MethodPost, "/"+microvmAPIVersion+"/microvms", raw, &parsed); err != nil {
		return microvmSnapshot{}, err
	}
	snap := parsed.snapshot(c.region)
	snap.Key = guac
	if snap.ID == "" {
		return microvmSnapshot{}, errors.New("RunMicrovm returned no microvm id")
	}
	return snap, nil
}

func (c *awsMicrovms) Get(ctx context.Context, id string) (microvmSnapshot, error) {
	var parsed microvmBody
	path := "/" + microvmAPIVersion + "/microvms/" + url.PathEscape(id)
	if err := c.call(ctx, http.MethodGet, path, nil, &parsed); err != nil {
		return microvmSnapshot{}, err
	}
	snap := parsed.snapshot(c.region)
	if snap.ID == "" {
		snap.ID = id
	}
	return snap, nil
}

func (c *awsMicrovms) AuthToken(ctx context.Context, id string) (string, error) {
	raw := []byte(`{"allowedPorts":[{"allPorts":{}}],"expirationInMinutes":60}`)
	var parsed struct {
		AuthToken map[string]string `json:"authToken"`
	}
	path := "/" + microvmAPIVersion + "/microvms/" + url.PathEscape(id) + "/auth-token"
	if err := c.call(ctx, http.MethodPost, path, raw, &parsed); err != nil {
		return "", err
	}
	for key, value := range parsed.AuthToken {
		if strings.EqualFold(key, "X-aws-proxy-auth") && value != "" {
			return value, nil
		}
	}
	return "", errors.New("auth token response had no X-aws-proxy-auth")
}

func (c *awsMicrovms) call(ctx context.Context, method, path string, payload []byte, out any) error {
	host := "lambda." + c.region + ".amazonaws.com"
	req, err := http.NewRequestWithContext(ctx, method, "https://"+host+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.URL.Opaque = "//" + host + path
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	sum := sha256.Sum256(payload)
	creds, err := c.cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return err
	}
	if err := c.signer.SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), "lambda", c.region, time.Now()); err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(payload))
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("lambda microvms %s %s: %d %s", method, path, resp.StatusCode, clip(respBody))
	}
	if out == nil || len(bytes.TrimSpace(respBody)) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("lambda microvms %s %s: %w", method, path, err)
	}
	return nil
}

func (b microvmBody) snapshot(region string) microvmSnapshot {
	return microvmSnapshot{
		ID:           b.MicrovmID,
		Endpoint:     b.Endpoint,
		Region:       region,
		State:        b.State,
		StateReason:  b.StateReason,
		ImageArn:     b.ImageArn,
		ImageVersion: b.ImageVersion,
		StartedAt:    formatStarted(b.StartedAt),
	}
}

func hookPayload(private, peer string) (string, string, error) {
	private = strings.TrimSpace(private)
	if private == "" {
		return "", "", nil
	}
	peer = strings.TrimSpace(peer)
	if peer == "" {
		peer = defaultPeerPublicKey
	}
	guac := randHex(16)
	body, err := json.Marshal(map[string]string{
		"serverPrivateKey": private,
		"peerPublicKey":    peer,
		"hostAddr":         "127.0.0.1",
		"guacCryptKey":     guac,
	})
	if err != nil {
		return "", "", err
	}
	return string(body), guac, nil
}

func websocketURL(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if strings.Contains(endpoint, "://") {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" {
			return ""
		}
		switch parsed.Scheme {
		case "https", "wss":
			parsed.Scheme = "wss"
		default:
			parsed.Scheme = "ws"
		}
		if parsed.Path == "" {
			parsed.Path = "/"
		}
		return parsed.String()
	}
	return "wss://" + strings.Trim(endpoint, "/") + "/"
}

func formatStarted(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		if n <= 0 {
			return ""
		}
		sec, frac := math.Modf(n)
		if n > 1e12 {
			sec, frac = math.Modf(n / 1000)
		}
		return time.Unix(int64(sec), int64(frac*1e9)).UTC().Format(time.RFC3339Nano)
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return ""
}

func validMicrovmID(id string) bool {
	if id == "" || len(id) > 256 || strings.ContainsAny(id, "#/\\\n\r\t ") {
		return false
	}
	return true
}

func clip(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}
