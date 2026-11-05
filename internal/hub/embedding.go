package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Embedder struct {
	BaseURL, APIKey, Model string
	Dimensions, BatchSize  int
	Timeout                time.Duration
	Client                 *http.Client
}

func (e *Embedder) Validate() error {
	u, err := url.Parse(e.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid embedding base URL")
	}
	if e.Model == "" || e.Dimensions < 1 || e.Dimensions > 16000 || e.BatchSize < 1 || e.BatchSize > 256 || e.Timeout <= 0 {
		return fmt.Errorf("invalid embedding model, dimensions, batch size or timeout")
	}
	return nil
}
func (e *Embedder) Fingerprint() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%d\nchunk-800-overlap-100-v1", strings.TrimRight(e.BaseURL, "/"), e.Model, e.Dimensions)))
	return hex.EncodeToString(sum[:])
}
func (e *Embedder) Vectors(ctx context.Context, texts []string) ([][]float64, error) {
	if e == nil {
		return nil, fail("semantic_unavailable", "embedding is not configured")
	}
	if err := e.Validate(); err != nil {
		return nil, fail("semantic_unavailable", "invalid embedding configuration")
	}
	all := make([][]float64, 0, len(texts))
	for start := 0; start < len(texts); start += e.BatchSize {
		end := min(start+e.BatchSize, len(texts))
		v, err := e.batch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		all = append(all, v...)
	}
	return all, nil
}
func (e *Embedder) batch(ctx context.Context, texts []string) ([][]float64, error) {
	ctx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	body := marshal(map[string]any{"model": e.Model, "input": texts, "dimensions": e.Dimensions, "encoding_format": "float"})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(e.BaseURL, "/")+"/embeddings", bytes.NewBufferString(body))
	if err != nil {
		return nil, fail("semantic_unavailable", "invalid embedding endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	client := e.Client
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fail("semantic_unavailable", "embedding request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fail("semantic_unavailable", fmt.Sprintf("embedding returned HTTP %d", resp.StatusCode))
	}
	var data struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&data); err != nil {
		return nil, fail("semantic_unavailable", "invalid embedding response")
	}
	if len(data.Data) != len(texts) {
		return nil, fail("semantic_unavailable", "embedding count mismatch")
	}
	out := make([][]float64, len(texts))
	for _, item := range data.Data {
		if item.Index < 0 || item.Index >= len(texts) || out[item.Index] != nil || len(item.Embedding) != e.Dimensions {
			return nil, fail("semantic_unavailable", "embedding dimension or index mismatch")
		}
		var norm float64
		for _, v := range item.Embedding {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fail("semantic_unavailable", "non-finite embedding")
			}
			norm += v * v
		}
		if norm == 0 || math.IsInf(norm, 0) {
			return nil, fail("semantic_unavailable", "invalid embedding norm")
		}
		out[item.Index] = item.Embedding
	}
	return out, nil
}

type Chunk struct {
	Position int
	Start    int
	Text     string
}

func Split(text string) []Chunk {
	r := []rune(text)
	out := []Chunk{}
	for start := 0; start < len(r); start += 700 {
		end := min(start+800, len(r))
		out = append(out, Chunk{len(out), start, string(r[start:end])})
		if end == len(r) {
			break
		}
	}
	return out
}
