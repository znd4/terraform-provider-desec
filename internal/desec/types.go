package desec

import (
	"context"
	"net/http"
	"net/url"
)

// Domain is a deSEC zone.
type Domain struct {
	Name       string      `json:"name"`
	MinimumTTL int64       `json:"minimum_ttl,omitempty"`
	Created    string      `json:"created,omitempty"`
	Published  string      `json:"published,omitempty"`
	Keys       []DomainKey `json:"keys,omitempty"`
}

// DomainKey is a DNSSEC key of a domain.
type DomainKey struct {
	DNSKey  string   `json:"dnskey"`
	DS      []string `json:"ds"`
	Managed bool     `json:"managed"`
}

// RRSet is a resource record set. Subname is "" at the zone apex.
type RRSet struct {
	Domain  string   `json:"domain,omitempty"`
	Subname string   `json:"subname"`
	Name    string   `json:"name,omitempty"`
	Type    string   `json:"type"`
	TTL     int64    `json:"ttl"`
	Records []string `json:"records"`
	Created string   `json:"created,omitempty"`
}

// Token is an API token. Value (the secret) is set only in the response to
// CreateToken.
type Token struct {
	ID               string   `json:"id"`
	Created          string   `json:"created"`
	Owner            string   `json:"owner"`
	Name             string   `json:"name"`
	PermCreateDomain bool     `json:"perm_create_domain"`
	PermDeleteDomain bool     `json:"perm_delete_domain"`
	PermManageTokens bool     `json:"perm_manage_tokens"`
	AllowedSubnets   []string `json:"allowed_subnets"`
	AutoPolicy       bool     `json:"auto_policy"`
	IsValid          bool     `json:"is_valid"`
	Value            string   `json:"token,omitempty"`
}

// TokenRequest is the body for creating or changing a token. A nil field is
// not sent, so the server keeps its value or uses its default.
type TokenRequest struct {
	Name             *string  `json:"name,omitempty"`
	PermCreateDomain *bool    `json:"perm_create_domain,omitempty"`
	PermDeleteDomain *bool    `json:"perm_delete_domain,omitempty"`
	PermManageTokens *bool    `json:"perm_manage_tokens,omitempty"`
	AllowedSubnets   []string `json:"allowed_subnets,omitempty"`
	AutoPolicy       *bool    `json:"auto_policy,omitempty"`
}

// TokenPolicy is an RRset policy of a token. A nil Domain, Subname or Type
// matches all values. The policy with all three nil is the default policy.
// The fields have no omitempty: the API must receive explicit nulls.
type TokenPolicy struct {
	ID        string  `json:"id,omitempty"`
	Domain    *string `json:"domain"`
	Subname   *string `json:"subname"`
	Type      *string `json:"type"`
	PermWrite bool    `json:"perm_write"`
}

func esc(s string) string { return url.PathEscape(s) }

// rrsetPath returns the URL path of one RRset. The API needs "@" for the
// apex, because an empty path segment is normalized away.
func rrsetPath(domain, subname, typ string) string {
	if subname == "" {
		subname = "@"
	}
	return "/domains/" + esc(domain) + "/rrsets/" + esc(subname) + "/" + esc(typ) + "/"
}

// CreateDomain makes a domain.
func (c *Client) CreateDomain(ctx context.Context, name string) (*Domain, error) {
	var out Domain
	_, err := c.do(ctx, http.MethodPost, "/domains/", map[string]string{"name": name}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetDomain returns a domain. The error is a 404 *APIError if it does not exist.
func (c *Client) GetDomain(ctx context.Context, name string) (*Domain, error) {
	var out Domain
	_, err := c.do(ctx, http.MethodGet, "/domains/"+esc(name)+"/", nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteDomain deletes a domain and all its RRsets.
func (c *Client) DeleteDomain(ctx context.Context, name string) error {
	_, err := c.do(ctx, http.MethodDelete, "/domains/"+esc(name)+"/", nil, nil)
	return err
}

// CreateRRSet makes an RRset.
func (c *Client) CreateRRSet(ctx context.Context, domain string, in RRSet) (*RRSet, error) {
	var out RRSet
	_, err := c.do(ctx, http.MethodPost, "/domains/"+esc(domain)+"/rrsets/", in, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRRSet returns an RRset. The error is a 404 *APIError if it does not exist.
func (c *Client) GetRRSet(ctx context.Context, domain, subname, typ string) (*RRSet, error) {
	var out RRSet
	_, err := c.do(ctx, http.MethodGet, rrsetPath(domain, subname, typ), nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateRRSet sets the TTL and the records of an RRset. records must not be
// empty: the API deletes the RRset on an empty list.
func (c *Client) UpdateRRSet(ctx context.Context, domain, subname, typ string, ttl int64, records []string) (*RRSet, error) {
	var out RRSet
	body := map[string]any{"ttl": ttl, "records": records}
	_, err := c.do(ctx, http.MethodPatch, rrsetPath(domain, subname, typ), body, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteRRSet deletes an RRset. It succeeds if the RRset does not exist.
func (c *Client) DeleteRRSet(ctx context.Context, domain, subname, typ string) error {
	_, err := c.do(ctx, http.MethodDelete, rrsetPath(domain, subname, typ), nil, nil)
	return err
}

// CreateToken makes a token. The result has the secret in Value.
func (c *Client) CreateToken(ctx context.Context, in TokenRequest) (*Token, error) {
	var out Token
	_, err := c.do(ctx, http.MethodPost, "/auth/tokens/", in, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetToken returns a token without its secret.
func (c *Client) GetToken(ctx context.Context, id string) (*Token, error) {
	var out Token
	_, err := c.do(ctx, http.MethodGet, "/auth/tokens/"+esc(id)+"/", nil, &out)
	if err != nil {
		return nil, err
	}
	out.Value = ""
	return &out, nil
}

// UpdateToken changes a token.
func (c *Client) UpdateToken(ctx context.Context, id string, in TokenRequest) (*Token, error) {
	var out Token
	_, err := c.do(ctx, http.MethodPatch, "/auth/tokens/"+esc(id)+"/", in, &out)
	if err != nil {
		return nil, err
	}
	out.Value = ""
	return &out, nil
}

// DeleteToken deletes a token. It succeeds if the token does not exist.
func (c *Client) DeleteToken(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/auth/tokens/"+esc(id)+"/", nil, nil)
	return err
}

func policiesPath(tokenID string) string {
	return "/auth/tokens/" + esc(tokenID) + "/policies/rrsets/"
}

// CreateTokenPolicy adds an RRset policy to a token.
func (c *Client) CreateTokenPolicy(ctx context.Context, tokenID string, in TokenPolicy) (*TokenPolicy, error) {
	in.ID = ""
	var out TokenPolicy
	_, err := c.do(ctx, http.MethodPost, policiesPath(tokenID), in, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTokenPolicy returns one policy of a token.
func (c *Client) GetTokenPolicy(ctx context.Context, tokenID, policyID string) (*TokenPolicy, error) {
	var out TokenPolicy
	_, err := c.do(ctx, http.MethodGet, policiesPath(tokenID)+esc(policyID)+"/", nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateTokenPolicy replaces all fields of a policy.
func (c *Client) UpdateTokenPolicy(ctx context.Context, tokenID, policyID string, in TokenPolicy) (*TokenPolicy, error) {
	in.ID = ""
	var out TokenPolicy
	_, err := c.do(ctx, http.MethodPatch, policiesPath(tokenID)+esc(policyID)+"/", in, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteTokenPolicy deletes a policy.
func (c *Client) DeleteTokenPolicy(ctx context.Context, tokenID, policyID string) error {
	_, err := c.do(ctx, http.MethodDelete, policiesPath(tokenID)+esc(policyID)+"/", nil, nil)
	return err
}
