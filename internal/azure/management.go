package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Scope is what a Resource Graph query reads: the subscriptions named, or
// the management groups named, or everything the identity may read when it
// names neither. A management group reads what is assigned at the group
// itself, such as a policy assignment or an exemption, as well as its
// subscriptions; a query scoped to subscriptions sees only theirs.
type Scope struct {
	Subscriptions    []string `yaml:"subscriptions"`
	ManagementGroups []string `yaml:"management_groups"`
}

// Check says what is wrong with the scope, or nil.
func (s Scope) Check() error {
	if len(s.Subscriptions) > 0 && len(s.ManagementGroups) > 0 {
		return errors.New("name subscriptions or management_groups, not both")
	}
	return nil
}

// ErrNotFound is a read of an id Azure does not have.
var ErrNotFound = errors.New("not found")

// Management is the Azure management API as one identity reads it:
// Resource Graph queries, page by page, and reads of one resource by id.
// Both wait out Azure's throttling instead of failing the read.
type Management struct {
	endpoint string
	token    func(context.Context) (string, error)
	http     *http.Client
	// Sleep waits between tries; a test swaps it to count the waits.
	Sleep func(context.Context, time.Duration) error

	mu     sync.Mutex
	cached string
	at     time.Time
}

// tokenLife is how long a token is reused. Azure's last an hour or more.
const tokenLife = 10 * time.Minute

// tries is how often one request is sent before a throttle or a server
// error fails it.
const tries = 6

// NewManagement reads the management API at endpoint,
// https://management.azure.com unless set, signing in with token.
func NewManagement(endpoint string, token func(context.Context) (string, error), client *http.Client) *Management {
	if endpoint == "" {
		endpoint = "https://management.azure.com"
	}
	return &Management{endpoint: strings.TrimRight(endpoint, "/"), token: token, http: client, Sleep: sleep}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (m *Management) bearer(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cached != "" && time.Since(m.at) < tokenLife {
		return m.cached, nil
	}
	t, err := m.token(ctx)
	if err != nil {
		return "", err
	}
	m.cached, m.at = t, time.Now()
	return t, nil
}

// Query runs a Resource Graph query over scope and returns every row once,
// page by page. key names a string column that is unique per row, and the
// pages are read in its order: Resource Graph pages an unordered result by
// position, so it repeats some rows and leaves others out. A key that
// repeats, or a result Azure cut short, fails the read rather than passing
// for the whole. Project only the columns you need: a page is a thousand
// rows.
func (m *Management) Query(ctx context.Context, scope Scope, query, key string) ([]map[string]any, error) {
	if err := scope.Check(); err != nil {
		return nil, err
	}
	if !IsColumn(key) {
		return nil, fmt.Errorf("resource graph: key %q is not a column name", key)
	}
	query += "\n| order by " + key + " asc"
	var rows []map[string]any
	seen := map[string]bool{}
	skip := ""
	for {
		options := map[string]any{"resultFormat": "objectArray", "$top": 1000}
		if skip != "" {
			options["$skipToken"] = skip
		}
		body := map[string]any{"query": query, "options": options}
		if len(scope.Subscriptions) > 0 {
			body["subscriptions"] = scope.Subscriptions
		}
		if len(scope.ManagementGroups) > 0 {
			body["managementGroups"] = scope.ManagementGroups
		}
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		data, header, err := m.send(ctx, http.MethodPost, "/providers/Microsoft.ResourceGraph/resources?api-version=2022-10-01", buf)
		if err != nil {
			return nil, fmt.Errorf("resource graph: %w", err)
		}
		var page struct {
			Data      []map[string]any `json:"data"`
			SkipToken string           `json:"$skipToken"`
			Truncated any              `json:"resultTruncated"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("resource graph: %w", err)
		}
		for _, row := range page.Data {
			k, _ := row[key].(string)
			if k == "" {
				continue
			}
			if seen[k] {
				return nil, fmt.Errorf("resource graph: %s %q came back twice, so %s is not unique per row and the pages cannot be read whole", key, k, key)
			}
			seen[k] = true
		}
		rows = append(rows, page.Data...)
		if page.SkipToken == "" {
			if fmt.Sprint(page.Truncated) == "true" {
				return nil, fmt.Errorf("resource graph: Azure cut the result short after %d rows and gave no way to read the rest", len(rows))
			}
			return rows, nil
		}
		skip = page.SkipToken
		// Resource Graph counts each identity's queries in a window; with
		// none left, the next page waits for the window to turn.
		if header.Get("x-ms-user-quota-remaining") == "0" {
			if err := m.Sleep(ctx, quotaReset(header, time.Second)); err != nil {
				return nil, err
			}
		}
	}
}

var columnRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsColumn says whether name is a column a query can be ordered by, by its
// bare name.
func IsColumn(name string) bool { return columnRE.MatchString(name) }

// Get reads one resource by its id, such as a policy definition, into out.
// A missing one is ErrNotFound.
func (m *Management) Get(ctx context.Context, id, apiVersion string, out any) error {
	data, _, err := m.send(ctx, http.MethodGet, id+"?api-version="+apiVersion, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	return nil
}

// send makes one request, trying again while Azure throttles it or its
// server fails, for as long as Azure asks.
func (m *Management) send(ctx context.Context, method, path string, body []byte) ([]byte, http.Header, error) {
	for try := 1; ; try++ {
		token, err := m.bearer(ctx)
		if err != nil {
			return nil, nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, m.endpoint+path, bytes.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := m.http.Do(req)
		if err != nil {
			return nil, nil, err
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
		res.Body.Close()
		if err != nil {
			return nil, nil, err
		}
		switch {
		case res.StatusCode == http.StatusOK:
			return data, res.Header, nil
		case res.StatusCode == http.StatusNotFound:
			return nil, nil, ErrNotFound
		case retryable(res.StatusCode) && try < tries:
			if err := m.Sleep(ctx, quotaReset(res.Header, time.Duration(try)*5*time.Second)); err != nil {
				return nil, nil, err
			}
			continue
		}
		return nil, nil, fmt.Errorf("azure answered %d: %s", res.StatusCode, strings.TrimSpace(string(data[:min(len(data), 300)])))
	}
}

func retryable(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

// quotaReset is how long Azure asks a client to wait: Retry-After in
// seconds, or Resource Graph's quota reset as hh:mm:ss, or fallback. It is
// never more than a minute.
func quotaReset(h http.Header, fallback time.Duration) time.Duration {
	wait := fallback
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil && s > 0 {
		wait = time.Duration(s) * time.Second
	} else if r := h.Get("x-ms-user-quota-resets-after"); r != "" {
		var hh, mm, ss int
		if _, err := fmt.Sscanf(r, "%d:%d:%d", &hh, &mm, &ss); err == nil {
			wait = time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute + time.Duration(ss)*time.Second
		}
	}
	if wait <= 0 {
		wait = time.Second
	}
	return min(wait, time.Minute)
}
