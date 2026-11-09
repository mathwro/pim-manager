package azureauth

import "testing"

func TestBatchCommandLineRejectsUnrepresentableArguments(t *testing.T) {
	for _, arg := range []string{"a\x00b", "a\rb", "a\nb"} {
		if _, err := batchCommandLine("az.cmd", []string{arg}); err == nil {
			t.Fatalf("accepted batch control characters in %q", arg)
		}
	}
}
