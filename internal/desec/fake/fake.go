// Package fake is an in-memory deSEC API for tests. It copies the API rules
// that the provider depends on (status codes, the "@" apex, the order of
// token policies). It is not a full copy of the API.
package fake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/znd4/terraform-provider-desec/internal/desec"
)

// Server is a fake deSEC API.
type Server struct {
	*httptest.Server

	// Token is the only accepted Authorization token.
	Token string
	// Throttle makes the next N requests fail with 429.
	Throttle atomic.Int32
	// Canonicalize, if set, changes record values on write, as the real API
	// does for non-canonical input.
	Canonicalize func(typ, value string) string

	mu       sync.Mutex
	nextID   int
	domains  map[string]*desec.Domain
	rrsets   map[string]map[string]*desec.RRSet // domain -> "subname/type"
	tokens   map[string]*desec.Token
	policies map[string][]*desec.TokenPolicy // token ID -> policies
	Requests []string
}

// New starts a fake server. Stop it with Close.
func New() *Server {
	s := &Server{
		Token:    "fake-admin-token",
		domains:  map[string]*desec.Domain{},
		rrsets:   map[string]map[string]*desec.RRSet{},
		tokens:   map[string]*desec.Token{},
		policies: map[string][]*desec.TokenPolicy{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/domains/", s.createDomain)
	mux.HandleFunc("GET /api/v1/domains/{name}/", s.getDomain)
	mux.HandleFunc("DELETE /api/v1/domains/{name}/", s.deleteDomain)
	mux.HandleFunc("POST /api/v1/domains/{name}/rrsets/", s.createRRSet)
	mux.HandleFunc("GET /api/v1/domains/{name}/rrsets/{subname}/{type}/", s.getRRSet)
	mux.HandleFunc("PATCH /api/v1/domains/{name}/rrsets/{subname}/{type}/", s.patchRRSet)
	mux.HandleFunc("DELETE /api/v1/domains/{name}/rrsets/{subname}/{type}/", s.deleteRRSet)
	mux.HandleFunc("POST /api/v1/auth/tokens/", s.createToken)
	mux.HandleFunc("GET /api/v1/auth/tokens/{id}/", s.getToken)
	mux.HandleFunc("PATCH /api/v1/auth/tokens/{id}/", s.patchToken)
	mux.HandleFunc("DELETE /api/v1/auth/tokens/{id}/", s.deleteToken)
	mux.HandleFunc("POST /api/v1/auth/tokens/{id}/policies/rrsets/", s.createPolicy)
	mux.HandleFunc("GET /api/v1/auth/tokens/{id}/policies/rrsets/{pid}/", s.getPolicy)
	mux.HandleFunc("PATCH /api/v1/auth/tokens/{id}/policies/rrsets/{pid}/", s.patchPolicy)
	mux.HandleFunc("DELETE /api/v1/auth/tokens/{id}/policies/rrsets/{pid}/", s.deletePolicy)

	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.Requests = append(s.Requests, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		if s.Throttle.Load() > 0 {
			s.Throttle.Add(-1)
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"detail":"throttled"}`, http.StatusTooManyRequests)
			return
		}
		if r.Header.Get("Authorization") != "Token "+s.Token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"detail": "Invalid token."})
			return
		}
		mux.ServeHTTP(w, r)
	}))
	return s
}

// BaseURL is the API base for desec.New.
func (s *Server) BaseURL() string { return s.URL + "/api/v1" }

// Client returns a client for this server.
func (s *Server) Client() *desec.Client { return desec.New(s.BaseURL(), s.Token) }

// Policies returns a copy of the policies of a token.
func (s *Server) Policies(tokenID string) []desec.TokenPolicy {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []desec.TokenPolicy
	for _, p := range s.policies[tokenID] {
		out = append(out, *p)
	}
	return out
}

// TokenIDs returns the IDs of all tokens.
func (s *Server) TokenIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for id := range s.tokens {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// SetRecords changes an RRset behind the provider's back (drift).
func (s *Server) SetRecords(domain, subname, typ string, records []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rr := s.rrsets[domain][subname+"/"+typ]; rr != nil {
		rr.Records = records
	}
}

func (s *Server) id() string {
	s.nextID++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", s.nextID)
}

func now() string { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC).Format(time.RFC3339) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Not found."})
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"detail": msg})
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// --- domains ---

func (s *Server) createDomain(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil || in.Name == "" {
		badRequest(w, "invalid domain")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.domains[in.Name]; ok {
		badRequest(w, "domain exists")
		return
	}
	d := &desec.Domain{
		Name: in.Name, MinimumTTL: 3600, Created: now(), Published: now(),
		Keys: []desec.DomainKey{{DNSKey: "257 3 13 AAAA", DS: []string{"1 13 2 abcd", "1 13 4 ef01"}, Managed: true}},
	}
	s.domains[in.Name] = d
	s.rrsets[in.Name] = map[string]*desec.RRSet{
		"/NS": {Domain: in.Name, Subname: "", Name: in.Name + ".", Type: "NS", TTL: 3600, Records: []string{"ns1.desec.io.", "ns2.desec.org."}},
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) getDomain(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.domains[r.PathValue("name")]
	if !ok {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.domains, r.PathValue("name"))
	delete(s.rrsets, r.PathValue("name"))
	w.WriteHeader(http.StatusNoContent)
}

// --- rrsets ---

func subnameFromPath(r *http.Request) string {
	sub := r.PathValue("subname")
	if sub == "@" {
		return ""
	}
	return sub
}

func (s *Server) canon(typ string, records []string) []string {
	out := make([]string, len(records))
	for i, v := range records {
		if s.Canonicalize != nil {
			v = s.Canonicalize(typ, v)
		}
		out[i] = v
	}
	return out
}

func (s *Server) createRRSet(w http.ResponseWriter, r *http.Request) {
	var in desec.RRSet
	if err := decode(r, &in); err != nil {
		badRequest(w, err.Error())
		return
	}
	domain := r.PathValue("name")
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.domains[domain]
	if !ok {
		notFound(w)
		return
	}
	if in.Type == "" || in.TTL == 0 || len(in.Records) == 0 {
		badRequest(w, "type, ttl and records are required")
		return
	}
	if in.TTL < d.MinimumTTL {
		badRequest(w, "ttl below minimum")
		return
	}
	key := in.Subname + "/" + in.Type
	if _, exists := s.rrsets[domain][key]; exists {
		badRequest(w, "Another RRset with the same subdomain and type exists for this domain.")
		return
	}
	name := domain + "."
	if in.Subname != "" {
		name = in.Subname + "." + name
	}
	rr := &desec.RRSet{Domain: domain, Subname: in.Subname, Name: name, Type: in.Type, TTL: in.TTL, Records: s.canon(in.Type, in.Records), Created: now()}
	s.rrsets[domain][key] = rr
	writeJSON(w, http.StatusCreated, rr)
}

func (s *Server) getRRSet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rr := s.rrsets[r.PathValue("name")][subnameFromPath(r)+"/"+r.PathValue("type")]
	if rr == nil {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, rr)
}

func (s *Server) patchRRSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TTL     *int64   `json:"ttl"`
		Records []string `json:"records"`
	}
	if err := decode(r, &in); err != nil {
		badRequest(w, err.Error())
		return
	}
	domain := r.PathValue("name")
	key := subnameFromPath(r) + "/" + r.PathValue("type")
	s.mu.Lock()
	defer s.mu.Unlock()
	rr := s.rrsets[domain][key]
	if rr == nil {
		notFound(w)
		return
	}
	if in.Records != nil && len(in.Records) == 0 {
		delete(s.rrsets[domain], key)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if in.TTL != nil {
		rr.TTL = *in.TTL
	}
	if in.Records != nil {
		rr.Records = s.canon(rr.Type, in.Records)
	}
	writeJSON(w, http.StatusOK, rr)
}

func (s *Server) deleteRRSet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rrsets[r.PathValue("name")], subnameFromPath(r)+"/"+r.PathValue("type"))
	w.WriteHeader(http.StatusNoContent)
}

// --- tokens ---

func applyToken(t *desec.Token, in desec.TokenRequest) {
	if in.Name != nil {
		t.Name = *in.Name
	}
	if in.PermCreateDomain != nil {
		t.PermCreateDomain = *in.PermCreateDomain
	}
	if in.PermDeleteDomain != nil {
		t.PermDeleteDomain = *in.PermDeleteDomain
	}
	if in.PermManageTokens != nil {
		t.PermManageTokens = *in.PermManageTokens
	}
	if in.AllowedSubnets != nil {
		t.AllowedSubnets = in.AllowedSubnets
	}
	if in.AutoPolicy != nil {
		t.AutoPolicy = *in.AutoPolicy
	}
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var in desec.TokenRequest
	if err := decode(r, &in); err != nil {
		badRequest(w, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &desec.Token{ID: s.id(), Created: now(), Owner: "owner@example.com", AllowedSubnets: []string{"0.0.0.0/0", "::/0"}, IsValid: true}
	applyToken(t, in)
	s.tokens[t.ID] = t
	out := *t
	out.Value = "secret-" + t.ID
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) getToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[r.PathValue("id")]
	if !ok {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) patchToken(w http.ResponseWriter, r *http.Request) {
	var in desec.TokenRequest
	if err := decode(r, &in); err != nil {
		badRequest(w, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[r.PathValue("id")]
	if !ok {
		notFound(w)
		return
	}
	applyToken(t, in)
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, r.PathValue("id"))
	delete(s.policies, r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// --- token policies ---

func isDefault(p *desec.TokenPolicy) bool {
	return p.Domain == nil && p.Subname == nil && p.Type == nil
}

func samePolicyKey(a, b *desec.TokenPolicy) bool {
	eq := func(x, y *string) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	return eq(a.Domain, b.Domain) && eq(a.Subname, b.Subname) && eq(a.Type, b.Type)
}

// policyBody keeps the difference between a missing field and null.
type policyBody struct {
	Domain    json.RawMessage `json:"domain"`
	Subname   json.RawMessage `json:"subname"`
	Type      json.RawMessage `json:"type"`
	PermWrite *bool           `json:"perm_write"`
}

func rawString(raw json.RawMessage) (*string, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, true, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, true, err
	}
	return &s, true, nil
}

func (s *Server) createPolicy(w http.ResponseWriter, r *http.Request) {
	var in policyBody
	if err := decode(r, &in); err != nil {
		badRequest(w, err.Error())
		return
	}
	p := &desec.TokenPolicy{}
	for _, f := range []struct {
		raw json.RawMessage
		dst **string
	}{{in.Domain, &p.Domain}, {in.Subname, &p.Subname}, {in.Type, &p.Type}} {
		v, present, err := rawString(f.raw)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		if !present {
			// The real API requires all three fields.
			badRequest(w, "This field is required.")
			return
		}
		*f.dst = v
	}
	if in.PermWrite != nil {
		p.PermWrite = *in.PermWrite
	}
	tid := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tokens[tid]; !ok {
		notFound(w)
		return
	}
	existing := s.policies[tid]
	if !isDefault(p) {
		hasDefault := false
		for _, e := range existing {
			if isDefault(e) {
				hasDefault = true
			}
		}
		if !hasDefault {
			badRequest(w, "Policy precedence: The first policy must be the default policy.")
			return
		}
	}
	for _, e := range existing {
		if samePolicyKey(e, p) {
			badRequest(w, "Policy with this domain, subname and type already exists for this token.")
			return
		}
	}
	p.ID = s.id()
	s.policies[tid] = append(existing, p)
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) findPolicy(tid, pid string) (int, *desec.TokenPolicy) {
	for i, p := range s.policies[tid] {
		if p.ID == pid {
			return i, p
		}
	}
	return -1, nil
}

func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.findPolicy(r.PathValue("id"), r.PathValue("pid"))
	if p == nil {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) patchPolicy(w http.ResponseWriter, r *http.Request) {
	var in policyBody
	if err := decode(r, &in); err != nil {
		badRequest(w, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.findPolicy(r.PathValue("id"), r.PathValue("pid"))
	if p == nil {
		notFound(w)
		return
	}
	next := *p
	for _, f := range []struct {
		raw json.RawMessage
		dst **string
	}{{in.Domain, &next.Domain}, {in.Subname, &next.Subname}, {in.Type, &next.Type}} {
		v, present, err := rawString(f.raw)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		if present {
			*f.dst = v
		}
	}
	if in.PermWrite != nil {
		next.PermWrite = *in.PermWrite
	}
	if isDefault(p) != isDefault(&next) && len(s.policies[r.PathValue("id")]) > 1 {
		badRequest(w, "Policy precedence: Cannot change the default policy while other policies exist.")
		return
	}
	*p = next
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request) {
	tid := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	i, p := s.findPolicy(tid, r.PathValue("pid"))
	if p == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if isDefault(p) && len(s.policies[tid]) > 1 {
		badRequest(w, "Policy precedence: Can't delete default policy when there exist others.")
		return
	}
	s.policies[tid] = append(s.policies[tid][:i], s.policies[tid][i+1:]...)
	w.WriteHeader(http.StatusNoContent)
}
