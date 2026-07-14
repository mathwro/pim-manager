package app

import (
	"context"
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

func Run() error {
	auth := azureauth.NewCLI(nil)
	principalID, err := auth.PrincipalID(context.Background())
	if err != nil {
		return err
	}
	httpClient := http.DefaultClient
	graphClient := graph.NewClient(httpClient, auth)
	armClient := arm.NewClient(httpClient, auth)
	scopes, err := azureresources.NewScopeDiscoverer(armClient).Discover(context.Background())
	if err != nil {
		return err
	}
	runtime := tui.Runtime{
		Entra:          entra.NewProvider(graphClient),
		AzureResources: azureresources.NewProvider(armClient, principalID, scopes),
		Groups:         groups.NewProvider(graphClient, principalID),
	}
	_, err = tea.NewProgram(tui.NewModel(runtime), tea.WithAltScreen()).Run()
	return err
}
