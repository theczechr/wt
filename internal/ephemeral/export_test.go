package ephemeral

import "context"

// SetPRStateLookupForTest swaps the gh-backed PR lookup for f and returns a
// function restoring the original. Tests must never reach the real gh.
func SetPRStateLookupForTest(f func(ctx context.Context, primary string, m Marker) (string, error)) func() {
	old := prStateLookup
	prStateLookup = f
	return func() { prStateLookup = old }
}
