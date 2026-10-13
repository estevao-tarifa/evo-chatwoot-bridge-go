package evolution

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Instance is the subset of an /instance/all item the bridge consumes. Token is
// a credential: it is never logged (AGENTS.md §8).
type Instance struct {
	Name  string
	Token string
}

// instanceItem decodes both the Evolution GO flat shape and the Evolution API
// v2 nested shape (auth.md §3).
type instanceItem struct {
	Name         string `json:"name"`
	InstanceName string `json:"instanceName"`
	Token        string `json:"token"`
	APIKey       string `json:"apikey"`
	Instance     *struct {
		InstanceName string `json:"instanceName"`
		APIKey       string `json:"apikey"`
	} `json:"instance"`
}

// ResolveInstanceToken fetches GET /instance/all with the admin key, matches
// the configured InstanceName, stores the token for subsequent instance calls,
// and returns it. It returns ErrInstanceNotFound when no instance matches and
// ErrInstanceTokenMissing when the match has no token. The configured name is
// never echoed in an error.
func (c *Client) ResolveInstanceToken(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	raw, err := c.doJSON(ctx, http.MethodGet, "/instance/all", scopeAdmin, nil)
	if err != nil {
		return "", fmt.Errorf("evolution: resolving instance token: %w", err)
	}
	instances, err := parseInstanceList(raw)
	if err != nil {
		return "", fmt.Errorf("evolution: resolving instance token: %w", err)
	}

	for _, inst := range instances {
		if inst.Name != c.instanceName {
			continue
		}
		if inst.Token == "" {
			return "", ErrInstanceTokenMissing
		}
		c.setInstanceToken(inst.Token)
		return inst.Token, nil
	}
	return "", ErrInstanceNotFound
}

// ListInstances returns every instance from GET /instance/all (admin key).
func (c *Client) ListInstances(ctx context.Context) ([]Instance, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	raw, err := c.doJSON(ctx, http.MethodGet, "/instance/all", scopeAdmin, nil)
	if err != nil {
		return nil, fmt.Errorf("evolution: listing instances: %w", err)
	}
	instances, err := parseInstanceList(raw)
	if err != nil {
		return nil, fmt.Errorf("evolution: listing instances: %w", err)
	}
	return instances, nil
}

// GetInstanceStatus calls GET /instance/status (instance key). The response
// shape is not contracted, so it is returned as a decoded object; callers must
// not assume field names. A malformed body yields ErrMalformedResponse with no
// upstream content in the error.
func (c *Client) GetInstanceStatus(ctx context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	raw, err := c.doJSON(ctx, http.MethodGet, "/instance/status", scopeInstance, nil)
	if err != nil {
		return nil, fmt.Errorf("evolution: getting instance status: %w", err)
	}
	var status map[string]any
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, fmt.Errorf("evolution: getting instance status: %w", ErrMalformedResponse)
	}
	return status, nil
}

// FetchContacts calls GET /user/contacts (instance key). The response shape is
// not contracted, so contacts are returned as decoded objects. A malformed list
// or entry fails closed with ErrMalformedResponse.
func (c *Client) FetchContacts(ctx context.Context) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	raw, err := c.doJSON(ctx, http.MethodGet, "/user/contacts", scopeInstance, nil)
	if err != nil {
		return nil, fmt.Errorf("evolution: fetching contacts: %w", err)
	}
	items, err := decodeArray(raw, "data", "contacts")
	if err != nil {
		return nil, fmt.Errorf("evolution: fetching contacts: %w", err)
	}
	contacts := make([]map[string]any, 0, len(items))
	for _, item := range items {
		var contact map[string]any
		if json.Unmarshal(item, &contact) != nil {
			return nil, fmt.Errorf("evolution: fetching contacts: %w", ErrMalformedResponse)
		}
		contacts = append(contacts, contact)
	}
	return contacts, nil
}

// parseInstanceList decodes /instance/all for both the Evolution GO
// `{data:[...]}` envelope and the v2 flat array.
func parseInstanceList(raw []byte) ([]Instance, error) {
	items, err := decodeArray(raw, "data", "instances")
	if err != nil {
		return nil, err
	}
	instances := make([]Instance, 0, len(items))
	for _, item := range items {
		var it instanceItem
		if json.Unmarshal(item, &it) != nil {
			return nil, ErrMalformedResponse
		}
		name := it.Name
		if name == "" {
			name = it.InstanceName
		}
		token := it.Token
		if token == "" {
			token = it.APIKey
		}
		if it.Instance != nil {
			if name == "" {
				name = it.Instance.InstanceName
			}
			if token == "" {
				token = it.Instance.APIKey
			}
		}
		instances = append(instances, Instance{Name: name, Token: token})
	}
	return instances, nil
}

// decodeArray extracts a JSON array from the top level or from the first
// present wrapper key. It is shared by the instance-list and contact-list
// parsers, which tolerate both flat arrays and `{<key>:[...]}` envelopes. Any
// undecodable body yields ErrMalformedResponse without the raw cause.
func decodeArray(raw []byte, keys ...string) ([]json.RawMessage, error) {
	var direct []json.RawMessage
	if json.Unmarshal(raw, &direct) == nil {
		return direct, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, ErrMalformedResponse
	}
	for _, key := range keys {
		value, ok := envelope[key]
		if !ok {
			continue
		}
		var arr []json.RawMessage
		if json.Unmarshal(value, &arr) == nil {
			return arr, nil
		}
	}
	return nil, ErrMalformedResponse
}
