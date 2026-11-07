package execadmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CheckPath is the bridge's authenticated loopback check endpoint.
const CheckPath = "/internal/approvals/check"

const defaultTimeout = 5 * time.Second

// Config is the host-only bridge description. The bearer is supplied in
// process memory from a private file: an environment variable would be
// inherited by every agent process the backend spawns.
type Config struct {
	BaseURL string `json:"baseUrl"`
	Token   string `json:"token"`
	// InvestigationProfiles is the private host proof allowlist.
	InvestigationProfiles []string `json:"investigationProfiles,omitempty"`
	Timeout               time.Duration
}

// FileConfig is the private JSON file shape.
type FileConfig struct {
	BaseURL string `json:"baseUrl"`
	Token   string `json:"token"`
	// InvestigationProfiles is the private proof allowlist: only these exact
	// agent profile ids may run a read-only investigation. A read-only profile
	// that is not listed is not investigation, and an approved read-only review
	// session stays an implementation intent.
	InvestigationProfiles []string `json:"investigationProfiles,omitempty"`
}

// LoadConfig reads the private bridge file. A missing or unreadable file
// disables admission rather than falling back to any other source.
func LoadConfig(path string) (Config, error) {
	// The bearer is a private process secret: a symlink, a non-regular file or
	// group/world-readable permissions are refused before the read.
	info, err := os.Lstat(path)
	if err != nil {
		return Config{}, fmt.Errorf("inspect execution approval config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Config{}, errors.New("execution approval config must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Config{}, errors.New("execution approval config must not be readable by group or others")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read execution approval config: %w", err)
	}
	var file FileConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return Config{}, fmt.Errorf("parse execution approval config: %w", err)
	}
	return Config{BaseURL: file.BaseURL, Token: file.Token, InvestigationProfiles: file.InvestigationProfiles}, nil
}

// Result is a bridge decision. Approved is true only for an exact binding.
type Result struct {
	Approved    bool
	Reason      string
	ApprovalID  string
	PlanVersion string
	Basis       string
}

// Denied reports a bridge refusal. The reason is safe to log; it never carries
// credentials.
type Denied struct {
	Reason string
}

func (d *Denied) Error() string {
	return "execution approval denied: " + d.Reason
}

// Client calls the bridge check endpoint. A nil client, an unavailable bridge,
// a malformed response and a refusal all deny execution.
type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

// NewClient validates that the configured bridge is loopback and builds the
// check endpoint. Only the fixed CheckPath is reachable.
func NewClient(cfg Config) (*Client, error) {
	base, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("parse execution approval base URL: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, errors.New("execution approval base URL must be http or https")
	}
	host := base.Hostname()
	if host == "" {
		return nil, errors.New("execution approval base URL is empty")
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		if !strings.EqualFold(host, "localhost") {
			return nil, errors.New("execution approval bridge must be reachable on loopback only")
		}
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("execution approval token is unavailable")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		endpoint: strings.TrimRight(base.String(), "/") + CheckPath,
		token:    cfg.Token,
		http: &http.Client{
			Timeout: timeout,
			// A redirect would move the bearer off loopback.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

type checkBody struct {
	OrganizationID   string  `json:"organizationId"`
	IssueID          string  `json:"issueId"`
	RootTaskID       string  `json:"rootTaskId"`
	WorkspaceID      string  `json:"workspaceId"`
	TaskID           string  `json:"taskId"`
	SessionID        string  `json:"sessionId"`
	Intent           string  `json:"intent"`
	PlanVersion      string  `json:"planVersion"`
	EffectiveProfile Profile `json:"effectiveProfile"`
}

type checkResponse struct {
	Approved    *bool  `json:"approved"`
	Reason      string `json:"reason"`
	ApprovalID  string `json:"approvalId"`
	PlanVersion string `json:"planVersion"`
	Basis       string `json:"basis"`
}

// Check asks the bridge whether this exact binding may execute. Any transport,
// status, encoding or binding failure denies.
func (c *Client) Check(ctx context.Context, req Request) (Result, error) {
	if c == nil {
		return Result{}, errors.New("execution approval bridge is unavailable")
	}
	if err := req.Validate(); err != nil {
		return Result{}, err
	}
	decoded, err := c.post(ctx, req)
	if err != nil {
		return Result{}, err
	}
	return evaluate(req, decoded)
}

// post sends the check request and decodes the bridge reply. Every transport,
// status or encoding failure denies.
func (c *Client) post(ctx context.Context, req Request) (checkResponse, error) {
	payload, err := json.Marshal(checkBody{
		OrganizationID:   req.OrganizationID,
		IssueID:          req.IssueID,
		RootTaskID:       req.RootTaskID,
		WorkspaceID:      req.WorkspaceID,
		TaskID:           req.TaskID,
		SessionID:        req.SessionID,
		Intent:           req.Intent,
		PlanVersion:      req.PlanVersion,
		EffectiveProfile: req.Profile,
	})
	if err != nil {
		return checkResponse{}, fmt.Errorf("encode execution approval request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return checkResponse{}, fmt.Errorf("build execution approval request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return checkResponse{}, fmt.Errorf("execution approval bridge is unavailable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return checkResponse{}, fmt.Errorf("read execution approval response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return checkResponse{}, fmt.Errorf("execution approval bridge returned status %d", resp.StatusCode)
	}
	var decoded checkResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return checkResponse{}, fmt.Errorf("decode execution approval response: %w", err)
	}
	return decoded, nil
}

// evaluate turns a decoded reply into a decision. An approval is only ever
// accepted for the exact intent and binding proof it carries.
func evaluate(req Request, decoded checkResponse) (Result, error) {
	result := Result{
		Approved:    decoded.Approved != nil && *decoded.Approved,
		Reason:      decoded.Reason,
		ApprovalID:  decoded.ApprovalID,
		PlanVersion: decoded.PlanVersion,
		Basis:       decoded.Basis,
	}
	if !result.Approved {
		reason := result.Reason
		if reason == "" {
			reason = "not-approved"
		}
		return result, &Denied{Reason: reason}
	}
	if req.Intent == IntentInvestigation {
		// A read-only investigation is admitted by its private proof only: it must
		// not borrow a human plan approval, and it must not pin a version.
		if result.Basis != BasisInvestigationProof || result.ApprovalID != "" || result.PlanVersion != "" || req.PlanVersion != "" {
			return Result{}, errors.New("execution approval response is not an investigation proof")
		}
		return result, nil
	}
	// An implementation approval without its binding proof cannot be pinned, so
	// it cannot admit.
	if result.Basis == BasisInvestigationProof || result.ApprovalID == "" || !planVersionPattern.MatchString(result.PlanVersion) {
		return Result{}, errors.New("execution approval response is incomplete")
	}
	if req.PlanVersion != "" && result.PlanVersion != req.PlanVersion {
		return Result{}, errors.New("execution approval plan version changed")
	}
	return result, nil
}

// ConfigFileName is the private bridge file inside the native runtime home.
const ConfigFileName = "execution-approval.json"

// DiscoverConfigPath returns the private bridge file under the resolved native
// runtime home (KANDEV_HOME_DIR or its default). There is no working-directory
// or repository lookup: the bearer never lives in the integration repo.
func DiscoverConfigPath(homeDir string) string {
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return ""
	}
	path := filepath.Join(homeDir, ConfigFileName)
	if info, err := os.Lstat(path); err == nil && !info.IsDir() {
		return path
	}
	return ""
}
