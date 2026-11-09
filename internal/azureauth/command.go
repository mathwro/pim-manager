package azureauth

import (
	"errors"
	"strings"
)

// batchCommandLine quotes for both cmd.exe and the launcher's %* forwarding to Python.
// The result is expanded from one child-only environment variable: cmd doesn't
// recursively expand percent expressions in its value. Delayed expansion must be off.
func batchCommandLine(path string, args []string) (string, error) {
	if strings.ContainsAny(path, "\x00\r\n\"") {
		return "", errors.New("invalid Azure CLI batch path")
	}
	var line strings.Builder
	line.WriteByte('"')
	line.WriteString(path)
	line.WriteByte('"')
	for _, arg := range args {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return "", errors.New("Azure CLI batch arguments cannot contain NUL or newlines")
		}
		line.WriteByte(' ')
		writeBatchCharacter(&line, '"')
		backslashes := 0
		for _, c := range arg {
			if c == '\\' {
				backslashes++
			} else {
				if c == '"' {
					for range backslashes + 1 {
						line.WriteByte('\\')
					}
				}
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
