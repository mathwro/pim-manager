package cmd

import (
	"os"

	"github.com/mathwro/pim-manager/internal/app"
	"github.com/spf13/cobra"
)

func newRootCmd(runApp func() error) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pim-manager",
		Short: "Discover and activate Microsoft PIM eligibilities",
		Long:  "pim-manager opens an interactive TUI for activating eligible Entra, Azure Resource, and Group PIM assignments.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApp()
		},
	}
	return cmd
}

func Execute() {
	if err := newRootCmd(app.Run).Execute(); err != nil {
		os.Exit(1)
	}
}
