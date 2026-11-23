// Package azure signs a pod in to Azure as its workload identity, and reads
// the management API as it: Resource Graph queries over subscriptions or
// management groups, and single resources by id. The pod's own service
// account token is traded for an Azure token, so no password is stored
// anywhere.
package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Identity is what the azure-identity ConfigMap gives a pod: the pod's own
// service account token is traded for an Azure token, so no password is
// stored anywhere.
type Identity struct {
	TenantID, ClientID, TokenFile, AuthorityHost string
}

// IdentityFromEnv reads the identity from the environment the ConfigMap sets.
func IdentityFromEnv() (Identity, error) {
	id := Identity{
		TenantID:      os.Getenv("AZURE_TENANT_ID"),
		ClientID:      os.Getenv("AZURE_CLIENT_ID"),
		TokenFile:     os.Getenv("AZURE_FEDERATED_TOKEN_FILE"),
		AuthorityHost: os.Getenv("AZURE_AUTHORITY_HOST"),
	}
	if id.TenantID == "" || id.ClientID == "" || id.TokenFile == "" {
		return id, errors.New("AZURE_TENANT_ID, AZURE_CLIENT_ID, and AZURE_FEDERATED_TOKEN_FILE must be set")
	}
	if id.AuthorityHost == "" {
		id.AuthorityHost = "https://login.microsoftonline.com/"
	}
	return id, nil
}

// The token audiences trellis asks for.
const (
	VaultScope      = "https://vault.azure.net/.default"
	ManagementScope = "https://management.azure.com/.default"
	// PostgresScope signs in to Azure Database for PostgreSQL.
	PostgresScope = "https://ossrdbms-aad.database.windows.net/.default"
)

// SignIn trades the service account token for a token for scope. The
// kubelet rotates the file, so it is read on every sign-in.
func SignIn(ctx context.Context, id Identity, scope string) (string, error) {
	assertion, err := os.ReadFile(id.TokenFile)
	if err != nil {
		return "", fmt.Errorf("service account token: %w", err)
	}
	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {id.ClientID},
		"scope":                 {scope},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {strings.TrimSpace(string(assertion))},
	}
	endpoint := strings.TrimSuffix(id.AuthorityHost, "/") + "/" + id.TenantID + "/oauth2/v2.0/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error_description"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("token: %s", out.Error)
	}
	return out.AccessToken, nil
}
