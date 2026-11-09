package azureauth

import (
	"errors"
	"strings"
)

// batchCommandLine quotes for both cmd.exe and the launcher's %* forwarding to Python.
// Double quotes are rejected: wrappers such as Scoop shims add further cmd parse
// passes, so no fixed escaping depth is safe for them. The result is expanded from
// one child-only environment variable: cmd doesn't recursively expand percent
// expressions in its value. Delayed expansion must be off.
func batchCommandLine(path string, args []string) (string, error) {
	if strings.ContainsAny(path, "\x00\r\n\"") {
		return "", errors.New("invalid Azure CLI batch path")
	}
	var line strings.Builder
	line.WriteByte('"')
	line.WriteString(path)
	line.WriteByte('"')
	for _, arg := range args {
		if strings.ContainsAny(arg, "\x00\r\n\"") {
			return "", errors.New("Azure CLI batch arguments cannot contain NUL, newlines, or double quotes")
		}
		line.WriteByte(' ')
		writeBatchCharacter(&line, '"')
		backslashes := 0
		for _, c := range arg {
			if c == '\\' {
				backslashes++
			} else {
				backslashes = 0
			}
			writeBatchCharacter(&line, c)
		}
		for range backslashes {
			line.WriteByte('\\')
		}
		writeBatchCharacter(&line, '"')
	}
	return line.String(), nil
}

// Escape once for cmd.exe and once for az.cmd's %* forwarding.
func writeBatchCharacter(line *strings.Builder, c rune) {
	if strings.ContainsRune("()[]%!^\"`<>&|;, *?\t", c) {
		line.WriteString("^^^")
	}
	line.WriteRune(c)
}
