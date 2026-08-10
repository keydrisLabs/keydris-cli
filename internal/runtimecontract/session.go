package runtimecontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	ContractBundleVersion = "1.2.0"
	SchemaVersion         = 1
	KitFormatJWT          = "jwt_svid"
	maxResponseBytes      = 1024*1024 + 4096
)

var ulidPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// KitSessionResponse is the canonical runtime v1 response returned when a
// local, mTLS-authenticated CLI creates or renews an agent session.
type KitSessionResponse struct {
	SchemaVersion int           `json:"schema_version"`
	SessionID     string        `json:"session_id"`
	Kit           KitCredential `json:"kit"`
}

type KitCredential struct {
	Format    string `json:"kit_format"`
	SPIFFEID  string `json:"spiffe_id"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// KitSession is the CLI's validated representation of a KIT session response.
type KitSession struct {
	SessionID string
	SPIFFEID  string
	KIT       string
	ExpiresAt string
}

func DecodeKitSession(reader io.Reader) (*KitSession, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read KIT session response: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("KIT session response exceeds the v1 size limit")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	var response KitSessionResponse
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("decode KIT session response: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if err := response.Validate(); err != nil {
		return nil, err
	}

	return &KitSession{
		SessionID: response.SessionID,
		SPIFFEID:  response.Kit.SPIFFEID,
		KIT:       response.Kit.Token,
		ExpiresAt: response.Kit.ExpiresAt,
	}, nil
}

func (response KitSessionResponse) Validate() error {
	if response.SchemaVersion != SchemaVersion {
		return fmt.Errorf(
			"unsupported runtime schema_version %d (expected %d)",
			response.SchemaVersion,
			SchemaVersion,
		)
	}
	if !ulidPattern.MatchString(response.SessionID) {
		return fmt.Errorf("KIT session response has an invalid session_id")
	}
	if response.Kit.Format != KitFormatJWT {
		return fmt.Errorf("unsupported KIT format %q", response.Kit.Format)
	}
	if !strings.HasPrefix(response.Kit.SPIFFEID, "spiffe://") {
		return fmt.Errorf("KIT session response has an invalid spiffe_id")
	}
	if len(response.Kit.SPIFFEID) > 2048 {
		return fmt.Errorf("KIT session response spiffe_id exceeds the v1 size limit")
	}
	if response.Kit.Token == "" {
		return fmt.Errorf("KIT session response is missing token")
	}
	if len(response.Kit.Token) > 1024*1024 {
		return fmt.Errorf("KIT session response token exceeds the v1 size limit")
	}
	if _, err := time.Parse(time.RFC3339, response.Kit.ExpiresAt); err != nil {
		return fmt.Errorf("KIT session response has an invalid expires_at: %w", err)
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode KIT session response trailer: %w", err)
	}
	return fmt.Errorf("KIT session response contains multiple JSON values")
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := walkJSONValue(decoder); err != nil {
		return fmt.Errorf("validate JSON: %w", err)
	}
	return ensureJSONEOF(decoder)
}

// RejectDuplicateJSONKeys applies the runtime contract's duplicate-key rule to
// intercepted JSON before it is interpreted or modified. Duplicate keys are
// ambiguous across parsers and therefore fail closed at the CLI boundary.
func RejectDuplicateJSONKeys(raw []byte) error {
	return rejectDuplicateJSONKeys(raw)
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}

	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delim {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, exists := keys[key]; exists {
				return fmt.Errorf("duplicate object key %q", key)
			}
			keys[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil {
			return err
		}
		if closeToken != json.Delim('}') {
			return fmt.Errorf("object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil {
			return err
		}
		if closeToken != json.Delim(']') {
			return fmt.Errorf("array is not closed")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return nil
}
