// Package ml calls the Python side (queue_api.py) for what Go does not do itself: the SigLIP2 text model,
// HEIC / RAW conversion and the long jobs (cluster, stacks).
package ml

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	URL       string // e.g. http://192.168.1.10:8503
	TokenFile string // the queue token file next to the DB (or MS_ML_TOKEN)
	Token     string
	http      *http.Client
}

func New(url, token, tokenFile string) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), Token: token, TokenFile: tokenFile,
		http: &http.Client{Timeout: 3 * time.Minute}} // the first text query loads the model
}

func (c *Client) token() string {
	if c.Token != "" {
		return c.Token
	}
	b, _ := os.ReadFile(c.TokenFile)
	return strings.TrimSpace(string(b))
}

// ErrUnavailable: the Python service does not answer (not running yet / restarting).
var ErrUnavailable = errors.New("сервис анализа (Python) не отвечает — подожди минуту и повтори")

func (c *Client) call(path string, req, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	r, err := http.NewRequest(http.MethodPost, c.URL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+c.token())
	r.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(r)
	if err != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var e struct{ Error string }
		_ = json.NewDecoder(res.Body).Decode(&e)
		return fmt.Errorf("%s: %s", path, e.Error)
	}
	return json.NewDecoder(res.Body).Decode(resp)
}

// EmbedText: the SigLIP2 vector of a text query (768 floats, normalized).
func (c *Client) EmbedText(text string) ([]float32, error) {
	var r struct{ Vector []float32 }
	err := c.call("/embed_text", map[string]string{"text": text}, &r)
	return r.Vector, err
}

// Convert: a JPEG of an image the browser cannot show (HEIC, RAW, TIFF), at most 3000 px.
func (c *Client) Convert(path string) ([]byte, error) {
	var r struct{ JPEG string }
	if err := c.call("/convert", map[string]string{"path": path}, &r); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(r.JPEG)
}

// Job is the state of the long job running on the Python side.
type Job struct {
	Name     *string `json:"name"`
	Running  bool    `json:"running"`
	Progress float64 `json:"progress"`
	Message  string  `json:"message"`
	Error    string  `json:"error"`
}

// StartJob starts cluster | stacks_build | stacks_score; "" = just the status.
func (c *Client) StartJob(name string) (Job, error) {
	var req map[string]any
	if name == "" {
		req = map[string]any{"name": nil}
	} else {
		req = map[string]any{"name": name}
	}
	var j Job
	err := c.call("/job", req, &j)
	return j, err
}
