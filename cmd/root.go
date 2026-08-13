package cmd

import (
	"context"
	"io"
	"os"

	"github.com/mathwro/pim-manager/internal/app"
	"github.com/mathwro/pim-manager/internal/selfupdate"
	"github.com/mathwro/pim-manager/internal/version"
	"github.com/spf13/cobra"
)

type updateFunc func(context.Context, io.Writer, io.Writer) error

func newRootCmd(runApp func() error, update updateFunc) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "pim-manager",
		Short:   "Discover and activate Microsoft PIM eligibilities",
		Long:    "Discover and activate Microsoft PIM eligibilities through an interactive TUI for eligible Entra, Azure Resource, and Group PIM assignments.",
		Version: version.String(),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApp()
		},
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.AddCommand(&cobra.Command{
		Use:   "update",
		Short: "Install the latest tagged version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return update(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	})
	return cmd
}

func Execute() {
	if err := newRootCmd(app.Run, selfupdate.InstallLatest).Execute(); err != nil {
		os.Exit(1)
	}
}
