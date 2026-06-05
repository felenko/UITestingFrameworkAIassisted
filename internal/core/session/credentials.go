package session

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const credentialsFileName = "credentials.yaml"

// mergeCredentials loads credentials.yaml (if present) and merges them into
// s.Variables. Session variables take precedence over credentials for the
// same key. Search order:
//   1. UITEST_CREDENTIALS env var (explicit path)
//   2. credentials.yaml next to the session file
//   3. credentials.yaml in the process working directory
func (s *Session) mergeCredentials() error {
	path, err := resolveCredentialsPath(s.SourcePath)
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	creds, err := readCredentialsFile(path)
	if err != nil {
		return fmt.Errorf("credentials %s: %w", path, err)
	}
	if s.Variables == nil {
		s.Variables = map[string]string{}
	}
	for k, v := range creds {
		if _, exists := s.Variables[k]; !exists {
			s.Variables[k] = v
		}
	}
	return nil
}

func resolveCredentialsPath(sessionPath string) (string, error) {
	if p := os.Getenv("UITEST_CREDENTIALS"); p != "" {
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("UITEST_CREDENTIALS file not found: %s", p)
			}
			return "", err
		}
		return p, nil
	}
	candidates := []string{}
	if sessionPath != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(sessionPath), credentialsFileName))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, credentialsFileName))
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", nil
}

func readCredentialsFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var creds map[string]string
	if err := yaml.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	if creds == nil {
		return map[string]string{}, nil
	}
	return creds, nil
}
