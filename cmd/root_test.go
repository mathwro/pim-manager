package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func noUpdate(context.Context, io.Writer, io.Writer) error { return nil }

func TestRootCommandRunsConfiguredApp(t *testing.T) {
	var ran bool
	cmd := newRootCmd(func() error {
		ran = true
		return nil
	}, noUpdate)
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
	}, noUpdate)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	got := cmd.Execute()
	if !errors.Is(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestRootCommandHelpIncludesPIMDescription(t *testing.T) {
	cmd := newRootCmd(func() error {
		t.Fatal("help should not run the app")
		return nil
	}, noUpdate)
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Discover and activate Microsoft PIM eligibilities") {
		t.Fatalf("expected help to include PIM description, got:\n%s", out.String())
	}
}

func TestUpdateRunsUpdaterWithoutStartingApp(t *testing.T) {
	var appRan, updateRan bool
	cmd := newRootCmd(func() error {
		appRan = true
		return nil
	}, func(context.Context, io.Writer, io.Writer) error {
		updateRan = true
		return nil
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"update"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if appRan || !updateRan {
		t.Fatalf("expected updater only, app=%v update=%v", appRan, updateRan)
	}
}

func TestUpdateReturnsUpdaterError(t *testing.T) {
	want := errors.New("update failed")
	cmd := newRootCmd(func() error {
		t.Fatal("update should not run the app")
		return nil
	}, func(context.Context, io.Writer, io.Writer) error {
		return want
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"update"})

	if err := cmd.Execute(); !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
}

func TestUpdateRejectsArguments(t *testing.T) {
	var appRan, updateRan bool
	cmd := newRootCmd(func() error {
		appRan = true
		return nil
	}, func(context.Context, io.Writer, io.Writer) error {
		updateRan = true
		return nil
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"update", "unexpected"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected argument error")
	}
	if appRan || updateRan {
		t.Fatalf("expected no runner calls, app=%v update=%v", appRan, updateRan)
	}
}
