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

type AttachmentInput struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Data string `json:"data"`
}
type Params struct {
	workspaceID      string
	Type             string            `json:"type,omitempty"`
	ID               string            `json:"id,omitempty"`
	IdempotencyKey   string            `json:"idempotency_key,omitempty"`
	ExpectedRevision int               `json:"expected_revision,omitempty"`
	Body             string            `json:"body,omitempty"`
	Scope            map[string]any    `json:"scope,omitempty"`
	Conditions       string            `json:"conditions,omitempty"`
	Tags             []string          `json:"tags,omitempty"`
	Sources          []string          `json:"sources,omitempty"`
	Reason           string            `json:"reason,omitempty"`
	Provenance       string            `json:"provenance,omitempty"`
	SourceTime       *time.Time        `json:"source_time,omitempty"`
	Metadata         map[string]any    `json:"metadata,omitempty"`
	Attachments      []AttachmentInput `json:"attachments,omitempty"`
	Revision         int               `json:"revision,omitempty"`
	History          bool              `json:"history,omitempty"`
	AttachmentID     string            `json:"attachment_id,omitempty"`
	Query            string            `json:"query,omitempty"`
	Types            []string          `json:"types,omitempty"`
	Since            *time.Time        `json:"since,omitempty"`
	Limit            int               `json:"limit,omitempty"`
	Offset           int               `json:"offset,omitempty"`
}
type Ref struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Revision int    `json:"revision,omitempty"`
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

var Operations = []string{"search", "read", "save"}

func IsWrite(op string) bool        { return op == "save" }
func KnownOperation(op string) bool { return op == "search" || op == "read" || op == "save" }
func marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("invalid internal JSON: %v", err))
	}
	return string(b)
}
