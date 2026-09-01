//go:build !linux

package attest

// noopResolver is used on non-Linux platforms (the proxy-env fallback has no
// per-connection attribution).
type noopResolver struct{}

// NewResolver returns a resolver that always reports unsupported.
func NewResolver(_ *SessionRegistry) Resolver { return noopResolver{} }

func (noopResolver) Resolve(string, int) (Attribution, error) {
	return Attribution{}, ErrUnsupported
}
