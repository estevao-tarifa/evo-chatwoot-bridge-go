package chatwoot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Kind selects the identity-matching rule of SearchContactExact.
type Kind int

const (
	KindUnknown Kind = iota
	// KindDirect is a 1:1 contact: match by phone digits or identifier.
	KindDirect
	// KindGroup is a group contact: match by identifier (the stable JID) only.
	KindGroup
)

const (
	// resultsPerPage is the Chatwoot contacts/search page size (contacts.md §3.3 M3).
	resultsPerPage = 15
	// maxSearchPages caps search pagination.
	// ponytail: 150 contacts ceiling; raise it only if a real account exceeds it.
	maxSearchPages = 10
	// maxListPages caps ListContacts pagination.
	maxListPages = 100
)

// Contact is the subset of a Chatwoot contact the bridge relies on. A null
// phone_number or identifier decodes to "".
type Contact struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	PhoneNumber string `json:"phone_number"`
	Identifier  string `json:"identifier"`
	Email       string `json:"email"`
	AvatarURL   string `json:"avatar_url"`
}

// ContactParams describes a contact to create.
type ContactParams struct {
	Kind      Kind
	Identity  string // 1:1: phone digits; group: the group JID
	Name      string // group: authoritative name required; direct: optional
	AvatarURL string // optional
}

type responseMeta struct {
	Count       *int            `json:"count"`
	CurrentPage json.RawMessage `json:"current_page"`
	HasMore     *bool           `json:"has_more"`
}

type contactsResponse struct {
	Meta    responseMeta `json:"meta"`
	Payload []Contact    `json:"payload"`
}

// UnmarshalJSON decodes the frozen contacts shape {meta, payload}
// (contacts.md §3). A body that omits either key is rejected as malformed, not
// decoded as an empty result: treating it as "no contacts" would let the engine
// CREATE a contact that already exists (G0, ROUTING_MATRIX.md §1, finding M2).
func (r *contactsResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		Meta    *responseMeta `json:"meta"`
		Payload *[]Contact    `json:"payload"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Meta == nil {
		return errors.New("chatwoot: contacts response missing meta")
	}
	if raw.Payload == nil {
		return errors.New("chatwoot: contacts response missing payload")
	}
	r.Meta = *raw.Meta
	r.Payload = *raw.Payload
	return nil
}

type createContactResponse struct {
	Payload struct {
		Contact Contact `json:"contact"`
	} `json:"payload"`
}

type searchContactsResponse struct {
	contactsResponse
	expectedPage int
}

func (r *searchContactsResponse) UnmarshalJSON(data []byte) error {
	if err := r.contactsResponse.UnmarshalJSON(data); err != nil {
		return err
	}
	page, err := parsePage(r.Meta.CurrentPage)
	if err != nil || page != r.expectedPage || r.Meta.HasMore == nil || r.Meta.Count == nil ||
		*r.Meta.Count != len(r.Payload) || *r.Meta.Count > resultsPerPage ||
		(*r.Meta.HasMore && *r.Meta.Count != resultsPerPage) {
		return errors.New("chatwoot: invalid search pagination")
	}
	return nil
}

// parsePage accepts a positive page number rendered either as a JSON integer or
// as a decimal string (v4.12.1 does not coerce a supplied query page). Absent,
// null, non-numeric or non-positive values are rejected; the decode error is
// never propagated, since it could carry upstream text.
func parsePage(raw json.RawMessage) (int, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0, errors.New("chatwoot: missing page")
	}
	if text[0] == '"' {
		var quoted string
		if err := json.Unmarshal([]byte(text), &quoted); err != nil {
			return 0, errors.New("chatwoot: invalid page")
		}
		text = quoted
	}
	if text == "" || strings.Trim(text, "0123456789") != "" {
		return 0, errors.New("chatwoot: invalid page")
	}
	page, err := strconv.Atoi(text)
	if err != nil || page <= 0 {
		return 0, errors.New("chatwoot: invalid page")
	}
	return page, nil
}

// UnmarshalJSON decodes the frozen create-contact shape {payload:{contact}}
// (contacts.md §4). A 2xx body missing payload.contact, or carrying a
// non-positive contact id, is rejected as malformed: returning a zero Contact
// would look like a successful creation with id 0 and corrupt the mapping
// (G0, ROUTING_MATRIX.md §1).
func (r *createContactResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		Payload *struct {
			Contact *Contact `json:"contact"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Payload == nil {
		return errors.New("chatwoot: create contact response missing payload")
	}
	if raw.Payload.Contact == nil {
		return errors.New("chatwoot: create contact response missing contact")
	}
	if raw.Payload.Contact.ID <= 0 {
		return errors.New("chatwoot: create contact response has non-positive id")
	}
	r.Payload.Contact = *raw.Payload.Contact
	return nil
}

// SearchContactExact returns a unique exact contact or nil after every page has
// been traversed. Errors and ambiguous identities never authorize creation.
func (c *Client) SearchContactExact(ctx context.Context, identity string, kind Kind) (*Contact, error) {
	if strings.TrimSpace(identity) == "" {
		return nil, errors.New("chatwoot: search identity is empty")
	}
	if kind != KindDirect && kind != KindGroup {
		return nil, fmt.Errorf("chatwoot: invalid contact kind %d", kind)
	}

	var match *Contact
	for page := 1; page <= maxSearchPages; page++ {
		contacts, hasMore, err := c.searchContacts(ctx, identity, page)
		if err != nil {
			return nil, err // never "not found"
		}
		for i := range contacts {
			if matchesIdentity(contacts[i], identity, kind) {
				if contacts[i].ID <= 0 {
					return nil, fmt.Errorf("%w: matched contact has invalid id", ErrInconclusive)
				}
				if match != nil && match.ID != contacts[i].ID {
					return nil, ErrIdentityConflict
				}
				found := contacts[i]
				match = &found
			}
		}
		if !hasMore {
			return match, nil
		}
	}
	return nil, fmt.Errorf("%w: contact search pagination exhausted", ErrInconclusive)
}

// ErrIdentityConflict means multiple distinct contacts claim the same identity.
var ErrIdentityConflict = errors.New("chatwoot: conflicting contact identity")

// ErrInconclusive means the response could not establish a trusted result
// (invalid matched id, exhausted pagination ceiling, a partial index). It is
// never a conclusive miss, so callers must fail closed and must not create an
// entity (G0, ADR-0009). These calls are reads, so it never implies a mutation
// had no effect.
var ErrInconclusive = errors.New("chatwoot: inconclusive response")

func (c *Client) searchContacts(ctx context.Context, query string, page int) ([]Contact, bool, error) {
	params := url.Values{
		"q":                {query},
		"include_contacts": {"true"},
		"page":             {strconv.Itoa(page)},
	}
	resp := searchContactsResponse{expectedPage: page}
	path := c.accountURL("contacts/search?" + params.Encode())
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, false, err
	}
	return resp.Payload, *resp.Meta.HasMore, nil
}

// ListContacts returns every contact in the account, following pagination.
func (c *Client) ListContacts(ctx context.Context) ([]Contact, error) {
	contacts := []Contact{}
	for page := 1; page <= maxListPages; page++ {
		var resp contactsResponse
		path := c.accountURL("contacts?" + url.Values{"page": {strconv.Itoa(page)}}.Encode())
		if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		currentPage, err := parsePage(resp.Meta.CurrentPage)
		if err != nil || currentPage != page || resp.Meta.Count == nil || *resp.Meta.Count < len(contacts)+len(resp.Payload) {
			return nil, fmt.Errorf("%w: invalid contacts index pagination", ErrInconclusive)
		}
		contacts = append(contacts, resp.Payload...)
		if len(contacts) == *resp.Meta.Count {
			return contacts, nil
		}
		if len(resp.Payload) == 0 {
			return nil, fmt.Errorf("%w: contacts index stopped before total", ErrInconclusive)
		}
	}
	return nil, fmt.Errorf("%w: contacts index pagination exhausted", ErrInconclusive)
}

// matchesIdentity applies the frozen matching rules (ROUTING_MATRIX.md §5):
// group = exact case-sensitive identifier; 1:1 = exact identifier OR exact
// phone digits.
func matchesIdentity(contact Contact, identity string, kind Kind) bool {
	if kind == KindGroup {
		return contact.Identifier == identity
	}
	if contact.Identifier == identity {
		return true
	}
	digits := digitsOnly(identity)
	return digits != "" && digitsOnly(contact.PhoneNumber) == digits
}

// CreateContact creates a contact using the payloads pinned by the contract
// (contacts.md §4).
func (c *Client) CreateContact(ctx context.Context, p ContactParams) (*Contact, error) {
	payload, err := contactPayload(p)
	if err != nil {
		return nil, err
	}
	var resp createContactResponse
	if err := c.doJSON(ctx, http.MethodPost, c.accountURL("contacts"), payload, &resp); err != nil {
		return nil, err
	}
	return &resp.Payload.Contact, nil
}

// UpdateContactName sets a contact's name. An empty name is rejected so the
// bridge can never blank or placeholder a name (AGENTS.md §7 rule 4).
//
// The caller (engine) MUST gate this call: only a real, non-placeholder name for
// the correct entity may be applied, and never on an externally-sent message
// (E2/E6, ROUTING_MATRIX.md §4). This method enforces the non-empty rule only;
// the policy decision belongs to the engine.
func (c *Client) UpdateContactName(ctx context.Context, contactID int, name string) error {
	if contactID <= 0 {
		return errors.New("chatwoot: contact id must be positive")
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("chatwoot: contact name must not be empty")
	}
	path := c.accountURL("contacts/" + strconv.Itoa(contactID))
	return c.doJSON(ctx, http.MethodPut, path, map[string]string{"name": name}, nil)
}

type contactBody struct {
	Name        string `json:"name"`
	PhoneNumber string `json:"phone_number,omitempty"`
	Identifier  string `json:"identifier"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

func contactPayload(p ContactParams) (contactBody, error) {
	identity := strings.TrimSpace(p.Identity)
	if identity == "" {
		return contactBody{}, errors.New("chatwoot: contact identity is required")
	}

	switch p.Kind {
	case KindGroup:
		// A group is created only from an authoritative, non-placeholder name
		// (groupData.Name). The placeholder "Grupo" (any case), the raw JID, a
		// JID-like or raw-phone value and a blank name are never display names
		// (ADR-0009 §1, AGENTS.md §7 rule 4).
		name := strings.TrimSpace(p.Name)
		if !validGroupDisplayName(name, identity) {
			return contactBody{}, errors.New("chatwoot: authoritative group name required")
		}
		return contactBody{Name: name, Identifier: identity, AvatarURL: p.AvatarURL}, nil

	case KindDirect:
		digits := digitsOnly(identity)
		if digits == "" {
			return contactBody{}, errors.New("chatwoot: direct contact identity has no digits")
		}
		name := p.Name
		if name == "" {
			name = digits
		}
		return contactBody{
			Name:        name,
			PhoneNumber: "+" + digits,
			Identifier:  digits,
			AvatarURL:   p.AvatarURL,
		}, nil

	default:
		return contactBody{}, fmt.Errorf("chatwoot: invalid contact kind %d", p.Kind)
	}
}

// digitsOnly strips every non-digit rune, mirroring the legacy normalisation.
func digitsOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// minPhoneDigits is the shortest all-digit run treated as a raw phone number.
// Shorter numeric names (e.g. "2026") stay valid.
const minPhoneDigits = 5

// validGroupDisplayName reports whether name is an acceptable group display name:
// not blank, not the placeholder "Grupo" (rejected case-insensitively, so
// "grupo"/"GRUPO" too), not the identity itself, and neither JID-like (contains
// "@") nor a raw phone number. A group's identity is its stable JID; the display
// name must be the authoritative groupData.Name (ADR-0009 §1, AGENTS.md §7
// rule 4).
func validGroupDisplayName(name, identity string) bool {
	if name == "" || strings.EqualFold(name, "Grupo") || name == identity {
		return false
	}
	if strings.Contains(name, "@") {
		return false
	}
	return !looksLikePhone(name)
}

// looksLikePhone reports whether name is a raw phone number: an optional single
// leading "+" followed by at least minPhoneDigits digits. Short numeric names are
// deliberately not treated as phones to avoid false positives.
func looksLikePhone(name string) bool {
	digits := strings.TrimPrefix(name, "+")
	return len(digits) >= minPhoneDigits && strings.Trim(digits, "0123456789") == ""
}
