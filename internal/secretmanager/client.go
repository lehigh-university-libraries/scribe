// Package secretmanager stores JSON credential maps using keyless Google ADC.
package secretmanager

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	sm "google.golang.org/api/secretmanager/v1"
)

type Client struct {
	service *sm.Service
	project string
	prefix  string
}

func New(ctx context.Context, project, prefix string, opts ...option.ClientOption) (*Client, error) {
	valid := regexp.MustCompile(`^[a-z][a-z0-9-]{2,62}$`)
	if !valid.MatchString(project) || !valid.MatchString(prefix) {
		return nil, errors.New("invalid Secret Manager project or deployment prefix")
	}
	service, err := sm.NewService(ctx, opts...)
	if err != nil {
		return nil, errors.New("initialize Secret Manager credentials")
	}
	return &Client{service: service, project: project, prefix: prefix}, nil
}

// Name is deterministic and collision resistant. The logical locator remains
// in the existing metadata ledger; tenant validation happens before each call.
func (c *Client) Name(path string) (string, error) {
	if path == "" || strings.TrimSpace(path) != path || strings.ContainsAny(path, "\r\n\\") {
		return "", errors.New("invalid secret locator")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("invalid secret locator")
		}
	}
	prefix := c.prefix
	if strings.Contains(path, "/provider-secrets/workspaces/") {
		prefix += "-provider"
	}
	return fmt.Sprintf("projects/%s/secrets/%s-%x", c.project, prefix, sha256.Sum256([]byte(path))), nil
}

func (c *Client) Read(ctx context.Context, path string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	name, err := c.Name(path)
	if err != nil {
		return nil, err
	}
	response, err := c.service.Projects.Secrets.Versions.Access(name + "/versions/latest").Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	if response.Payload == nil {
		return nil, errors.New("empty secret payload")
	}
	data, err := base64.StdEncoding.DecodeString(response.Payload.Data)
	if err != nil || len(data) > 65536 {
		return nil, errors.New("invalid secret payload")
	}
	var values map[string]string
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, errors.New("invalid secret JSON")
	}
	return values, nil
}

func (c *Client) Write(ctx context.Context, path string, values map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	name, err := c.Name(path)
	if err != nil {
		return err
	}
	data, err := json.Marshal(values)
	if err != nil || len(data) > 65536 {
		return errors.New("invalid secret payload")
	}
	_, err = c.service.Projects.Secrets.Create("projects/"+c.project, &sm.Secret{
		Replication: &sm.Replication{Automatic: &sm.Automatic{}},
	}).SecretId(strings.TrimPrefix(name, "projects/"+c.project+"/secrets/")).Context(ctx).Do()
	if err != nil && !hasCode(err, 409) {
		return err
	}
	_, err = c.service.Projects.Secrets.AddVersion(name, &sm.AddSecretVersionRequest{
		Payload: &sm.SecretPayload{Data: base64.StdEncoding.EncodeToString(data)},
	}).Context(ctx).Do()
	return err
}

func (c *Client) Delete(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	name, err := c.Name(path)
	if err != nil {
		return err
	}
	_, err = c.service.Projects.Secrets.Delete(name).Context(ctx).Do()
	if IsNotFound(err) {
		return nil
	}
	return err
}

func hasCode(err error, code int) bool {
	var apiError *googleapi.Error
	return errors.As(err, &apiError) && apiError.Code == code
}

func IsNotFound(err error) bool { return hasCode(err, 404) }

func IsRetryable(err error) bool {
	return hasCode(err, 429) || hasCode(err, 500) || hasCode(err, 502) || hasCode(err, 503) || hasCode(err, 504)
}
