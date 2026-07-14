package cmd

import (
	"bytes"
	"errors"
	"testing"
)

func TestRootCommandRunsConfiguredApp(t *testing.T) {
	var ran bool
	cmd := newRootCmd(func() error {
		ran = true
		return nil
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !ran {
		t.Fatal("expected root command to run the configured app")
	}
}

func TestRootCommandReturnsAppError(t *testing.T) {
	want := errors.New("app failed")
	cmd := newRootCmd(func() error {
		return want
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	got := cmd.Execute()
	if !errors.Is(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}
