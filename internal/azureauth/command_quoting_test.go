package azureauth

import "testing"

func TestBatchCommandLineRejectsUnrepresentableArguments(t *testing.T) {
	for _, arg := range []string{"a\x00b", "a\rb", "a\nb", `a"&echo injected&"b`, `{"claims":"quoted value"}`, `backslash\"quote`, `two""quotes`} {
		if _, err := batchCommandLine("az.cmd", []string{arg}); err == nil {
			t.Fatalf("accepted unrepresentable batch argument %q", arg)
		}
	}
}
