// Package rachio is a client of Rachio's public cloud API, the one way
// into a Rachio irrigation controller: the account's controllers with
// their zones and schedules, what is watering now, the controller's
// events, and the commands that start a zone, stop the watering, delay for
// rain and put a controller in standby. It is REST and JSON over HTTPS
// under the account's API key, 3,500 requests a day.
//
// The contract is Rachio's documentation, https://rachio.readme.io:
// docs/design.md says what was read there, where it contradicts itself,
// and what it leaves out.
package rachio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// BaseURL is where Rachio's documentation puts the API.
const BaseURL = "https://api.rach.io/1/public"

// maxAnswer bounds one answer read; an account with its controllers and
// zones is some tens of kilobytes.
const maxAnswer = 16 << 20

// The errors a Client hands back, to test with errors.Is. Each carries
// Rachio's own message and code when Rachio gave them.
var (
	// ErrCredentials: Rachio answered 401 or 403. The documentation gives
	// no status for a key it refuses; Rachio's servers answer one 401.
	ErrCredentials = errors.New("rachio refused the API key")
	// ErrRateLimited: Rachio answered 429. The documentation gives the
	// daily limit and no status for reaching it.
	ErrRateLimited = errors.New("rachio's request limit is reached")
	// ErrNotFound: Rachio answered 404.
	ErrNotFound = errors.New("rachio has no such record")
	// ErrRefused: any other answer that is not a success, the documented
	// 400 among them.
	ErrRefused = errors.New("rachio refused the request")
	// ErrAnswer: the answer is not in the shape the documentation shows,
	// or is larger than an answer may be.
	ErrAnswer = errors.New("rachio's answer is not in the expected shape")
	// ErrArgument: an id, a duration or a range that cannot be sent;
	// nothing was.
	ErrArgument = errors.New("not a valid argument")
)

// Options shape a Client. The zero value talks to BaseURL through
// http.DefaultClient.
type Options struct {
	// BaseURL is where the API is; empty is BaseURL.
	BaseURL string
	// HTTP is the client every request goes through; nil is
	// http.DefaultClient.
	HTTP *http.Client
}

// Client is one account's access to the API. Its methods may be called
// from several goroutines. A Client formatted shows nothing of its key.
//
//nolint:recvcheck // Format alone has a value receiver, so that a Client held by value is formatted by it too
type Client struct {
	base string
	http *http.Client
	// Behind a func, not a pointer: where fmt cannot call Format, a Client
	// in another struct's unexported field, it prints through a pointer
	// for a verb it does not know, and never calls a func.
	held func() *secrets
}

// Format keeps what a Client holds out of a formatted value, whatever
// the verb, and whether the Client is held by value or by pointer.
func (Client) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, "rachio client") }

// secrets is what a Client must not show, the key, with what it learned
// along the way.
type secrets struct {
	key string

	mu     sync.Mutex
	person string
	limit  Limit
	seen   bool

	// asking is held while the account's id is asked for, so that it is
	// asked once however many callers want it.
	asking sync.Mutex
}

// New is a client for the account the API key belongs to: the key the
// Rachio app gives, the whole account's. Nothing is sent until a method is
// called.
func New(key string, opts Options) *Client {
	state := &secrets{key: key}

	client := &Client{
		base: opts.BaseURL,
		http: opts.HTTP,
		held: func() *secrets { return state },
	}

	if client.base == "" {
		client.base = BaseURL
	}

	if client.http == nil {
		client.http = http.DefaultClient
	}

	return client
}

// Limit is the account's daily count of requests, as the last answer's
// headers gave it. The count is the key's, whatever program spends it.
type Limit struct {
	// Limit is how many requests a day the account may make.
	Limit int
	// Remaining is how many are left until Reset.
	Remaining int
	// Reset is when the count starts again; zero when the header was not
	// a time in either form the documentation shows.
	Reset time.Time
}

// Limit is the count the last answer gave, and whether any answer gave
// one.
func (c *Client) Limit() (Limit, bool) {
	held := c.held()

	held.mu.Lock()
	defer held.mu.Unlock()

	return held.limit, held.seen
}

// The bodies of a refusal: the one the documentation shows under a 400,
// an error and its code, and the one Rachio's servers send with a 401, a
// list of messages.
type (
	refused struct {
		Error  string    `json:"error"`
		Code   int       `json:"code"`
		Errors []message `json:"errors"`
	}
	message struct {
		Message string `json:"message"`
	}
)

// call sends one request under the account's key and returns the answer's
// body, empty for a command, which Rachio answers with 204.
func (c *Client) call(ctx context.Context, method, path string, query url.Values, body any) ([]byte, error) {
	held := c.held()
	if held.key == "" {
		return nil, fmt.Errorf("%w: no API key", ErrArgument)
	}

	var payload io.Reader = http.NoBody

	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("%w: the body for %s: %w", ErrArgument, path, err)
		}

		payload = bytes.NewReader(raw)
	}

	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %s %s: %w", ErrArgument, method, path, err)
	}

	// The documentation's every sample sends the content type, on a GET
	// too.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+held.key)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return nil, fmt.Errorf("%s %s: reading the answer: %w", method, path, err)
	}

	if len(raw) > maxAnswer {
		return nil, fmt.Errorf("%w: %s %s: over %d bytes", ErrAnswer, method, path, maxAnswer)
	}

	held.count(resp.Header)

	// The documentation shows 200 for a read and 204 for a command; any
	// success is taken as one.
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, refusal(method, path, resp.StatusCode, raw)
	}

	return raw, nil
}

// count keeps the limit an answer's headers give; an answer without them
// changes nothing.
func (s *secrets) count(header http.Header) {
	remaining, err := strconv.Atoi(header.Get("X-Ratelimit-Remaining"))
	if err != nil {
		return
	}

	limit := Limit{Remaining: remaining}
	limit.Limit, _ = strconv.Atoi(header.Get("X-Ratelimit-Limit"))

	// The documentation says RFC 1123 and shows RFC 3339.
	written := header.Get("X-Ratelimit-Reset")
	if reset, err := time.Parse(time.RFC3339, written); err == nil {
		limit.Reset = reset
	} else if reset, err := http.ParseTime(written); err == nil {
		limit.Reset = reset
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.limit, s.seen = limit, true
}

// refusal is the error for an answer that is not a success: its class by
// the status, with Rachio's message and code when it gave them.
func refusal(method, path string, status int, raw []byte) error {
	class := ErrRefused

	switch status {
	case http.StatusTooManyRequests:
		class = ErrRateLimited
	case http.StatusNotFound:
		class = ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		class = ErrCredentials
	}

	var said refused

	switch {
	case json.Unmarshal(raw, &said) != nil:
	case said.Error != "":
		return fmt.Errorf("%w: %s %s: HTTP %d, code %d: %q", class, method, path, status, said.Code, said.Error)
	case len(said.Errors) > 0 && said.Errors[0].Message != "":
		return fmt.Errorf("%w: %s %s: HTTP %d: %q", class, method, path, status, said.Errors[0].Message)
	}

	return fmt.Errorf("%w: %s %s: HTTP %d", class, method, path, status)
}

// read sends a GET and decodes its answer into into.
func (c *Client) read(ctx context.Context, path string, query url.Values, into any) error {
	raw, err := c.call(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}

	if err = json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("%w: GET %s: %w", ErrAnswer, path, err)
	}

	return nil
}

// subject is the body of a command: the id of what it acts on, and a
// duration in seconds for the commands that take one.
type subject struct {
	ID       string `json:"id"`
	Duration *int64 `json:"duration,omitempty"`
}

// command sends a PUT on the record id; a nil length sends no duration.
func (c *Client) command(ctx context.Context, path, id string, length *time.Duration) error {
	if id == "" {
		return fmt.Errorf("%w: an empty id for %s", ErrArgument, path)
	}

	body := subject{ID: id}

	if length != nil {
		seconds := int64(*length / time.Second)
		body.Duration = &seconds
	}

	_, err := c.call(ctx, http.MethodPut, path, nil, body)

	return err
}

// within refuses a duration outside a command's range.
func within(length, least, most time.Duration, what string) error {
	if length < least || length > most {
		return fmt.Errorf("%w: %s of %s, outside %s to %s", ErrArgument, what, length, least, most)
	}

	return nil
}

// record is the path of one record, the id escaped; an empty id is
// refused.
func record(kind, id, rest string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("%w: an empty %s id", ErrArgument, kind)
	}

	return "/" + kind + "/" + url.PathEscape(id) + rest, nil
}

// stamp is a time as the API writes one, milliseconds since 1970; zero
// and the negative value the documentation shows for a date that is none
// are the zero time.
func stamp(millis int64) time.Time {
	if millis <= 0 {
		return time.Time{}
	}

	return time.UnixMilli(millis)
}

// seconds is a duration as the API writes one.
func seconds(count int64) time.Duration { return time.Duration(count) * time.Second }
