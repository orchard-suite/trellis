package kubernetes

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Client reads the Kubernetes API with a bearer token. In the cluster it is
// the pod's own service account; outside, the settings name a server and a
// token. It speaks plain REST, so the binary carries no client-go.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// Settings is where the API is and how to sign in.
type Settings struct {
	// URL is the API server, such as https://10.0.0.1:6443. Empty means
	// in-cluster.
	URL string
	// Token is the bearer token. Empty means the in-cluster token file.
	Token string
	// CAFile is the server's CA bundle. Empty means the in-cluster file,
	// or the system roots when URL is set.
	CAFile string
}

const (
	inClusterToken = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	inClusterCA    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// NewClient opens the API per the settings.
func NewClient(s Settings) (*Client, error) {
	base, token, caFile := s.URL, s.Token, s.CAFile
	if base == "" {
		host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
		if host == "" || port == "" {
			return nil, errors.New("not in a cluster and the kubernetes source names no url")
		}
		base = "https://" + host + ":" + port
		if caFile == "" {
			caFile = inClusterCA
		}
	}
	if token == "" {
		b, err := os.ReadFile(inClusterToken)
		if err != nil {
			return nil, fmt.Errorf("kubernetes token: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("kubernetes ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("kubernetes ca: no certificate in " + caFile)
		}
		tlsConfig.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 60 * time.Second, Transport: otelhttp.NewTransport(transport)},
	}, nil
}

// ErrNotServed is a resource the API server does not know: its CRD is not
// installed.
var ErrNotServed = errors.New("resource not served")

// List reads every item of a collection, following continue tokens, into out.
func (c *Client) List(ctx context.Context, path string, out any) error {
	var items []json.RawMessage
	next := ""
	for {
		url := c.base + path + "?limit=500"
		if next != "" {
			url += "&continue=" + next
		}
		var page struct {
			Items    []json.RawMessage `json:"items"`
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
		}
		if err := c.get(ctx, url, &page); err != nil {
			return err
		}
		items = append(items, page.Items...)
		next = page.Metadata.Continue
		if next == "" {
			break
		}
	}
	joined, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return json.Unmarshal(joined, out)
}

func (c *Client) get(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 128<<20))
	if err != nil {
		return err
	}
	switch res.StatusCode {
	case http.StatusOK:
		return json.Unmarshal(body, out)
	case http.StatusNotFound:
		return fmt.Errorf("%s: %w", strings.TrimPrefix(url, c.base), ErrNotServed)
	default:
		return fmt.Errorf("kubernetes GET %s: %d %s", strings.TrimPrefix(url, c.base), res.StatusCode, strings.TrimSpace(string(body[:min(len(body), 200)])))
	}
}
