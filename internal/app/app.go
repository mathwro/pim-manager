package app

import (
	"net/http"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/providers/azureresources"
	"github.com/mathwro/pim-manager/internal/tui"
)

var newCLI = azureauth.NewCLI

var runProgram = func(model tea.Model) error {
	_, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}

func Run() error {
	auth := newCLI(nil)
	armClient := arm.NewClient(http.DefaultClient, auth)
	runtime := tui.Runtime{
		AzureResources: azureresources.NewProvider(armClient),
		Account:        auth,
		StepUpCommand:  azureauth.StepUpLoginCommand,
	}
	return runProgram(tui.NewModel(runtime))
}
