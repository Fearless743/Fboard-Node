package panel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fearless743/fboard-node/internal/config"
	"github.com/fearless743/fboard-node/internal/nlog"
)

// ErrEndpointUnsupported marks a 404/405 response: the panel does not expose
// this REST endpoint (for example an older panel build). Callers may fall back
// to the WebSocket path instead of treating it as a transient failure.
var ErrEndpointUnsupported = errors.New("panel endpoint unsupported")

// maxResponseBody bounds how much of a panel response we are willing to read.
// User lists can be large, but a broken panel must not be able to OOM the node.
const maxResponseBody = 512 << 20

// StatusError describes a non-2xx panel response.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("panel status %d", e.Code)
	}
	return fmt.Sprintf("panel status %d: %s", e.Code, e.Body)
}

// IsEndpointUnsupported reports whether err was caused by a missing endpoint.
func IsEndpointUnsupported(err error) bool {
	return errors.Is(err, ErrEndpointUnsupported)
}

// APIMetrics holds aggregated API call statistics.
type APIMetrics struct {
	Success uint64
	Failure uint64
}

// NodeReport is the consolidated node report sent to /api/v2/server/report.
// ReportID is an idempotency key: retries of the same report must reuse it so
// the panel can avoid double-counting traffic when a response is lost.
type NodeReport struct {
	ReportID string
	Traffic  map[int][2]int64
	Alive    map[int][]string
	Online   map[int]int
	CPU      float64
	Mem      [2]uint64
	Swap     [2]uint64
	Disk     [2]uint64
	Metrics  map[string]interface{}
}

// Client communicates with the Fboard panel REST API using machine
// authentication (token + machine_id, and node_id for per-node calls).
type Client struct {
	baseURL   string
	token     string
	nodeID    int
	machineID int

	httpClient *http.Client

	// ETag / body-hash dedupe. ETag is the primary mechanism; the body hash is
	// a fallback for panels that do not emit ETags or proxies that strip them.
	configETag     string
	userETag       string
	configBodyHash string
	userBodyHash   string
	lastUsersEmpty bool

	// Last successfully decoded snapshots. Returned on 304 / unchanged-body so
	// callers can keep counting confirmation polls (removals, empty lists,
	// config changes) instead of seeing a difference only once.
	lastConfig *NodeConfig
	lastUsers  []User

	apiSuccess atomic.Uint64
	apiFailure atomic.Uint64
}

// NewClient creates a machine-scoped panel API client. Call ForNode to create
// a per-node client that shares the HTTP transport and connection pool.
func NewClient(cfg config.PanelConfig) *Client {
	return &Client{
		baseURL:   strings.TrimRight(cfg.URL, "/"),
		token:     cfg.Token,
		nodeID:    cfg.NodeID,
		machineID: cfg.MachineID,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// ForNode returns a client bound to a specific node_id, sharing the same base
// URL, auth and HTTP transport. ETags and body hashes start fresh per node.
func (c *Client) ForNode(nodeID int) *Client {
	return &Client{
		baseURL:    c.baseURL,
		token:      c.token,
		nodeID:     nodeID,
		machineID:  c.machineID,
		httpClient: c.httpClient,
	}
}

// ResetETags clears cached validators so the next calls return full responses.
func (c *Client) ResetETags() {
	c.configETag = ""
	c.userETag = ""
	c.configBodyHash = ""
	c.userBodyHash = ""
	c.lastUsersEmpty = false
	c.lastConfig = nil
	c.lastUsers = nil
}

// GetConfig fetches the node configuration. Returns (nil, nil) when the panel
// answers 304 or when the response body is byte-identical to the last one.
func (c *Client) GetConfig(ctx context.Context) (*NodeConfig, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/v2/server/config", nil, c.configETag)
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return c.lastConfig, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp)
	}

	body, err := readBody(resp)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	hash := bodyHash(body)
	if c.configBodyHash != "" && hash == c.configBodyHash {
		// Same content, new ETag (or no ETag at all). Nothing new to decode,
		// but return the cached snapshot so callers can still confirm a
		// pending change across repeated polls.
		c.configETag = resp.Header.Get("ETag")
		return c.lastConfig, nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode raw config: %w", err)
	}
	var cfg NodeConfig
	if err := decodeWeakRaw(raw, &cfg); err != nil {
		return nil, fmt.Errorf("weak decode config: %w", err)
	}
	if strings.TrimSpace(cfg.Protocol) == "" {
		return nil, fmt.Errorf("invalid config: missing protocol")
	}

	c.configBodyHash = hash
	c.configETag = resp.Header.Get("ETag")
	c.lastConfig = &cfg
	return &cfg, nil
}

// GetUsers fetches the available user list. Returns (nil, nil) when the panel
// answers 304 or the body is unchanged. A non-nil empty slice means the panel
// explicitly returned an empty user set (for example maintenance mode).
func (c *Client) GetUsers(ctx context.Context) ([]User, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/v2/server/user", nil, c.userETag)
	if err != nil {
		return nil, fmt.Errorf("get users: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return c.lastUsers, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp)
	}

	body, err := readBody(resp)
	if err != nil {
		return nil, fmt.Errorf("read users: %w", err)
	}
	hash := bodyHash(body)
	if c.userBodyHash != "" && hash == c.userBodyHash {
		c.userETag = resp.Header.Get("ETag")
		return c.lastUsers, nil
	}

	var usersResp UsersResponse
	if err := json.Unmarshal(body, &usersResp); err != nil {
		return nil, fmt.Errorf("decode users: %w", err)
	}
	// `users: []` is an explicit clear. A missing/null key is malformed and
	// must never be interpreted as "remove all users".
	if usersResp.Users == nil {
		return nil, fmt.Errorf("decode users: missing users array")
	}

	c.lastUsersEmpty = len(usersResp.Users) == 0
	c.userBodyHash = hash
	c.userETag = resp.Header.Get("ETag")
	c.lastUsers = usersResp.Users
	return usersResp.Users, nil
}

// UsersKnownEmpty reports whether the most recent 200 user response was an
// explicit empty list. It lets callers keep counting confirmations across
// 304 responses instead of seeing an empty list only once.
func (c *Client) UsersKnownEmpty() bool {
	return c.lastUsersEmpty
}

// Report sends a consolidated traffic/alive/online/status/metrics report.
func (c *Client) Report(ctx context.Context, report NodeReport) error {
	payload := make(map[string]interface{}, 8)
	if report.ReportID != "" {
		payload["report_id"] = report.ReportID
	}
	if len(report.Traffic) > 0 {
		payload["traffic"] = int64TrafficMap(report.Traffic)
	}
	if len(report.Alive) > 0 {
		payload["alive"] = intStringSliceMap(report.Alive)
	}
	// Always include online (even empty) so stale ONLINE_USER counters clear.
	payload["online"] = intIntMap(report.Online)
	payload["status"] = map[string]interface{}{
		"cpu":  report.CPU,
		"mem":  map[string]interface{}{"total": report.Mem[0], "used": report.Mem[1]},
		"swap": map[string]interface{}{"total": report.Swap[0], "used": report.Swap[1]},
		"disk": map[string]interface{}{"total": report.Disk[0], "used": report.Disk[1]},
	}
	if len(report.Metrics) > 0 {
		payload["metrics"] = report.Metrics
	}
	return c.postJSON(ctx, "/api/v2/server/report", payload)
}

// GetMachineNodes fetches the list of enabled nodes bound to this machine.
func (c *Client) GetMachineNodes(ctx context.Context) (*MachineNodesResponse, error) {
	resp, err := c.postRequest(ctx, "/api/v2/server/machine/nodes", nil)
	if err != nil {
		return nil, fmt.Errorf("get machine nodes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp)
	}
	body, err := readBody(resp)
	if err != nil {
		return nil, fmt.Errorf("read machine nodes: %w", err)
	}
	var out MachineNodesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode machine nodes: %w", err)
	}
	// A missing/null nodes array is malformed, not "no nodes". Treating it as
	// an empty list could stop every node on a transient panel bug.
	if out.Nodes == nil {
		return nil, fmt.Errorf("decode machine nodes: missing nodes array")
	}
	return &out, nil
}

// ReportMachineStatus sends machine-level load metrics to the panel.
func (c *Client) ReportMachineStatus(ctx context.Context, cpu float64, mem, swap, disk [2]uint64, netIn, netOut float64, version string, kernel map[string]interface{}) error {
	payload := map[string]interface{}{
		"cpu":  cpu,
		"mem":  map[string]interface{}{"total": mem[0], "used": mem[1]},
		"swap": map[string]interface{}{"total": swap[0], "used": swap[1]},
		"disk": map[string]interface{}{"total": disk[0], "used": disk[1]},
	}
	if netIn >= 0 && netOut >= 0 {
		payload["net"] = map[string]interface{}{"in_speed": netIn, "out_speed": netOut}
	}
	if version != "" {
		payload["version"] = version
	}
	if kernel != nil {
		payload["kernel"] = kernel
	}
	return c.postJSON(ctx, "/api/v2/server/machine/status", payload)
}

// SnapshotMetrics returns a snapshot of API metrics.
func (c *Client) SnapshotMetrics() APIMetrics {
	return APIMetrics{
		Success: c.apiSuccess.Load(),
		Failure: c.apiFailure.Load(),
	}
}

// ─── internals ─────────────────────────────────────────────────────────────

func (c *Client) postJSON(ctx context.Context, path string, payload map[string]interface{}) error {
	resp, err := c.postRequest(ctx, path, payload)
	if err != nil {
		return fmt.Errorf("post %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

func (c *Client) postRequest(ctx context.Context, path string, payload map[string]interface{}) (*http.Response, error) {
	if payload == nil {
		payload = make(map[string]interface{})
	}
	c.injectAuth(payload)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return c.doRequest(ctx, http.MethodPost, path, body, "")
}

func (c *Client) doRequest(ctx context.Context, method, path string, body []byte, ifNoneMatch string) (*http.Response, error) {
	fullURL := c.baseURL + path
	var bodyReader io.Reader
	if method == http.MethodGet {
		fullURL += "?" + c.authQuery().Encode()
	} else if body != nil {
		bodyReader = bytes.NewReader(body)
	} else {
		authOnly := make(map[string]interface{})
		c.injectAuth(authOnly)
		merged, _ := json.Marshal(authOnly)
		bodyReader = bytes.NewReader(merged)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Primary auth channel: Bearer header keeps the token out of URLs. The
	// query/body token is retained as a fallback for panels that only read it.
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	nlog.Core().Debug("panel request", "method", method, "path", path)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.apiFailure.Add(1)
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		c.apiSuccess.Add(1)
	} else {
		c.apiFailure.Add(1)
	}
	nlog.Core().Debug("panel response", "method", method, "path", path, "status", resp.StatusCode)
	return resp, nil
}

func (c *Client) injectAuth(m map[string]interface{}) {
	m["token"] = c.token
	m["machine_id"] = c.machineID
	if c.nodeID > 0 {
		m["node_id"] = c.nodeID
	}
}

func (c *Client) authQuery() url.Values {
	q := url.Values{}
	q.Set("token", c.token)
	q.Set("machine_id", strconv.Itoa(c.machineID))
	if c.nodeID > 0 {
		q.Set("node_id", strconv.Itoa(c.nodeID))
	}
	return q
}

func responseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(body))
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return fmt.Errorf("%w: status %d: %s", ErrEndpointUnsupported, resp.StatusCode, msg)
	}
	return &StatusError{Code: resp.StatusCode, Body: msg}
}

func readBody(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
}

func bodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func int64TrafficMap(in map[int][2]int64) map[string][2]int64 {
	out := make(map[string][2]int64, len(in))
	for uid, d := range in {
		out[strconv.Itoa(uid)] = d
	}
	return out
}

func intStringSliceMap(in map[int][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for uid, ips := range in {
		out[strconv.Itoa(uid)] = ips
	}
	return out
}

func intIntMap(in map[int]int) map[string]int {
	out := make(map[string]int, len(in))
	for uid, count := range in {
		out[strconv.Itoa(uid)] = count
	}
	return out
}
