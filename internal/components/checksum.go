package components

import (
	"fmt"
	"regexp"
	"strings"
)

// hexSHA256Pattern matches a bare 64-character lowercase hex string, the
// form a sha256sum(1) line (or dl.k8s.io's bare-hash checksum files) uses.
var hexSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// parseChecksum extracts a sha256 hex digest for filename out of the
// contents of a checksum file. It supports every format rask's pinned
// component releases use:
//
//   - a bare hex digest with no filename (dl.k8s.io's "<bin>.sha256" files)
//   - one or more "<hex>  <filename>" sha256sum(1) lines, optionally
//     wrapped in PGP clearsign armor (runc's "runc.sha256sum"), matched
//     against filename
func parseChecksum(body []byte, filename string) (string, error) {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "", fmt.Errorf("components: empty checksum file")
	}

	if hexSHA256Pattern.MatchString(text) {
		return text, nil
	}

	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !hexSHA256Pattern.MatchString(fields[0]) {
			continue
		}

		if fields[1] == filename {
			return fields[0], nil
		}
	}

	return "", fmt.Errorf("components: no checksum line for %q found in checksum file", filename)
}
