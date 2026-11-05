package hub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *Error) Error() string    { return e.Code + ": " + e.Message }
func fail(code, msg string) error { return &Error{Code: code, Message: msg} }

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func UUID(s string) bool { return uuidPattern.MatchString(s) }
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

type Ref struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version int    `json:"version,omitempty"`
}
type AttachmentInput struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Data string `json:"data"`
}
type SourceInput struct {
	Body        string            `json:"body"`
	Tags        []string          `json:"tags,omitempty"`
	Conditions  string            `json:"conditions,omitempty"`
	Metadata    map[string]any    `json:"metadata,omitempty"`
	Provenance  string            `json:"provenance"`
	SourceTime  *time.Time        `json:"source_time,omitempty"`
	SourceType  string            `json:"source_type"`
	Attachments []AttachmentInput `json:"attachments,omitempty"`
}
type Params struct {
	WorkspaceID     string            `json:"workspace_id"`
	IdempotencyKey  string            `json:"idempotency_key,omitempty"`
	ID              string            `json:"id,omitempty"`
	ExpectedVersion int               `json:"expected_version,omitempty"`
	Body            string            `json:"body,omitempty"`
	Tags            []string          `json:"tags,omitempty"`
	Conditions      string            `json:"conditions,omitempty"`
	Metadata        map[string]any    `json:"metadata,omitempty"`
	Reason          string            `json:"reason,omitempty"`
	Provenance      string            `json:"provenance,omitempty"`
	SourceTime      *time.Time        `json:"source_time,omitempty"`
	SourceType      string            `json:"source_type,omitempty"`
	Attachments     []AttachmentInput `json:"attachments,omitempty"`
	Sources         []Ref             `json:"sources,omitempty"`
	Claim           *Ref              `json:"claim,omitempty"`
	Evidence        []Ref             `json:"evidence,omitempty"`
	OutputSource    *SourceInput      `json:"output_source,omitempty"`
	Method          string            `json:"method,omitempty"`
	PerformedAt     *time.Time        `json:"performed_at,omitempty"`
	Conclusion      string            `json:"conclusion,omitempty"`
	Verifications   []Ref             `json:"verifications,omitempty"`
	ValidFrom       *time.Time        `json:"valid_from,omitempty"`
	ValidUntil      *time.Time        `json:"valid_until,omitempty"`
	Claims          []Ref             `json:"claims,omitempty"`
	Assessments     []Ref             `json:"assessments,omitempty"`
	ResolveEvents   []string          `json:"resolve_events,omitempty"`
	Ref             *Ref              `json:"ref,omitempty"`
	Depth           int               `json:"depth,omitempty"`
	Limit           int               `json:"limit,omitempty"`
	Offset          int               `json:"offset,omitempty"`
	Query           string            `json:"query,omitempty"`
	Mode            string            `json:"mode,omitempty"`
	Types           []string          `json:"types,omitempty"`
	PendingReview   *bool             `json:"pending_review,omitempty"`
}
type Revision struct {
	WorkspaceID  string         `json:"workspace_id"`
	ID           string         `json:"object_id"`
	Version      int            `json:"version"`
	Kind         string         `json:"kind"`
	Body         string         `json:"body"`
	Tags         []string       `json:"tags"`
	Conditions   string         `json:"conditions"`
	Metadata     map[string]any `json:"metadata"`
	Author       string         `json:"author"`
	Reason       string         `json:"reason"`
	CreatedAt    time.Time      `json:"created_at"`
	Provenance   string         `json:"provenance,omitempty"`
	SourceTime   *time.Time     `json:"source_time,omitempty"`
	SourceType   string         `json:"source_type,omitempty"`
	Method       string         `json:"method,omitempty"`
	PerformedAt  *time.Time     `json:"performed_at,omitempty"`
	Conclusion   *string        `json:"conclusion,omitempty"`
	ValidFrom    *time.Time     `json:"valid_from,omitempty"`
	ValidUntil   *time.Time     `json:"valid_until,omitempty"`
	ClaimID      *string        `json:"claim_id,omitempty"`
	ClaimVersion *int           `json:"claim_version,omitempty"`
}

func (r Revision) Ref() Ref { return Ref{r.Kind, r.ID, r.Version} }

type Link struct {
	Role string `json:"role"`
	Ref  Ref    `json:"ref"`
}
type Attachment struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
	Name     string `json:"name"`
	MIME     string `json:"mime"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Data     string `json:"data,omitempty"`
}
type Page struct {
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"has_more"`
}
type Result struct {
	Data     any      `json:"data"`
	Page     *Page    `json:"page,omitempty"`
	Warnings []string `json:"warnings"`
}

func result(v any) Result { return Result{Data: v, Warnings: []string{}} }

var Operations = []string{"source_create", "source_tags_update", "claim_create", "claim_revise", "verification_create", "assessment_create", "knowledge_publish", "knowledge_read", "knowledge_history", "knowledge_search", "attachment_read"}

func IsWrite(op string) bool {
	switch op {
	case "source_create", "source_tags_update", "claim_create", "claim_revise", "verification_create", "assessment_create", "knowledge_publish":
		return true
	}
	return false
}
func KnownOperation(op string) bool {
	for _, n := range Operations {
		if n == op {
			return true
		}
	}
	return false
}
func marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("invalid internal JSON: %v", err))
	}
	return string(b)
}
