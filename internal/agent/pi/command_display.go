package pi

import (
	"path/filepath"
	"strings"
)

func firstWorkingRoot(roots []string) string {
	if len(roots) == 0 || !filepath.IsAbs(roots[0]) || filepath.Clean(roots[0]) != roots[0] || roots[0] == "/" {
		return ""
	}
	return roots[0]
}

// redactHostCommand only formats display evidence. Original command digests,
// native permissions and check attribution are independent of this text.
func redactHostCommand(command, root string) string {
	if sensitiveCommand.MatchString(command) {
		return "[REDACTED]"
	}
	var out strings.Builder
	var quote byte
	for i := 0; i < len(command); {
		c := command[i]
		if c == '\\' && quote != '\'' && i+1 < len(command) {
			out.WriteString(command[i : i+2])
			i += 2
			continue
		}
		if c == '\'' || c == '"' {
			if quote == 0 {
				quote = c
			} else if quote == c {
				quote = 0
			}
			out.WriteByte(c)
			i++
			continue
		}
		if c != '/' || i > 0 && !absolutePathBoundary(command[i-1]) || i > 0 && command[i-1] == ':' && i+1 < len(command) && command[i+1] == '/' {
			out.WriteByte(c)
			i++
			continue
		}
		end := i
		var decoded strings.Builder
		for end < len(command) {
			ch := command[end]
			if quote != 0 && ch == quote || quote == 0 && absolutePathTerminator(ch) {
				break
			}
			if ch == '\\' && quote != '\'' && end+1 < len(command) {
				end++
				ch = command[end]
			}
			decoded.WriteByte(ch)
			end++
		}
		candidate := decoded.String()
		replacement := ""
		if root != "" {
			if relative, err := filepath.Rel(root, filepath.Clean(candidate)); err == nil && relative != ".." && !strings.HasPrefix(relative, "../") {
				replacement = filepath.ToSlash(relative)
				// Keep a display of a relative operand from becoming an absolute-looking
				// one or changing quoting when filenames contain spaces or punctuation.
				if quote == 0 {
					replacement = "'" + strings.ReplaceAll(replacement, "'", "'\\''") + "'"
				} else if quote == '"' {
					replacement = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "$", "\\$", "`", "\\`").Replace(replacement)
				}
			}
		}
		if replacement != "" {
			out.WriteString(replacement)
		} else if allowedAbsolutePath(candidate) {
			out.WriteString(command[i:end])
		} else {
			out.WriteString("[host path redacted]")
		}
		i = end
	}
	return out.String()
}
