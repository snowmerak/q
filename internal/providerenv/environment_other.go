//go:build !windows

package providerenv

func userEnvironment() (map[string]string, error) {
	return nil, ErrNotConfigured
}
