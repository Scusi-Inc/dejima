package provision

import (
	"os"
	"strings"
)

// AppendLineIfAbsent appends line (plus a newline) to the file at path unless an
// identical trimmed line already exists. Creates the file 0644 if missing. Used
// for idempotent shell/SSH config edits (~/.zshenv PATH, authorized_keys,
// DEJIMA_HOST). Shared with the onboarding wizard so both write config the same
// way.
func AppendLineIfAbsent(path, line string) error {
	if existing, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(string(existing), "\n") {
			if strings.TrimSpace(l) == line {
				return nil
			}
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}
