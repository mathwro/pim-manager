package azureauth

import "testing"

func TestBatchCommandLineQuotesForwardedArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  string
		want string
	}{
		{"empty", "", `"C:\Program Files\Azure CLI\az.cmd" ^^^"^^^"`},
		{"spaces", "two words", `"C:\Program Files\Azure CLI\az.cmd" ^^^"two^^^ words^^^"`},
		{"metacharacters", `%VALUE%`, `"C:\Program Files\Azure CLI\az.cmd" ^^^"^^^%VALUE^^^%^^^"`},
		{"quotes", `a"&echo injected&"b`, `"C:\Program Files\Azure CLI\az.cmd" ^^^"a\^^^"^^^&echo^^^ injected^^^&\^^^"b^^^"`},
		{"trailing slash", `C:\data\`, `"C:\Program Files\Azure CLI\az.cmd" ^^^"C:\data\\^^^"`},
		{"slash before quote", `a\"b`, `"C:\Program Files\Azure CLI\az.cmd" ^^^"a\\\^^^"b^^^"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, err := batchCommandLine(`C:\Program Files\Azure CLI\az.cmd`, []string{tc.arg})
			if err != nil || line != tc.want {
				t.Fatalf("line = %q, err = %v, want %q", line, err, tc.want)
			}
		})
	}
}

func TestBatchCommandLineRejectsUnrepresentableArguments(t *testing.T) {
	for _, arg := range []string{"a\x00b", "a\rb", "a\nb"} {
		if _, err := batchCommandLine("az.cmd", []string{arg}); err == nil {
			t.Fatalf("accepted batch control characters in %q", arg)
		}
	}
}
