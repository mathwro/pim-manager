package app

import (
	"net/http"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/graph"
	"github.com/mathwro/pim-manager/internal/providers/azureresources"
	"github.com/mathwro/pim-manager/internal/providers/entra"
	"github.com/mathwro/pim-manager/internal/providers/groups"
	"github.com/mathwro/pim-manager/internal/tui"
)

var newCLI = azureauth.NewCLI

var runProgram = func(model tea.Model) error {
	_, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}

func Run() error {
	auth := newCLI(nil)
	httpClient := http.DefaultClient
	graphClient := graph.NewClient(httpClient, auth)
	armClient := arm.NewClient(httpClient, auth)
	runtime := tui.Runtime{
		Entra:          entra.NewProvider(graphClient),
		AzureResources: azureresources.NewProvider(armClient),
		Groups: newLazyPrincipalProvider(auth, func(principalID string) lazyAssignmentProvider {
			provider := groups.NewProvider(graphClient, principalID)
			return lazyAssignmentProvider{discover: provider.Discover, activate: provider.Activate}
		}),
		Account: auth,
	}
	return runProgram(tui.NewModel(runtime))
}
