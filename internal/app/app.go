package app

import (
	"net/http"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/graph"
	"github.com/mathwro/pim-manager/internal/graphauth"
	"github.com/mathwro/pim-manager/internal/providers/azureresources"
	"github.com/mathwro/pim-manager/internal/providers/entra"
	"github.com/mathwro/pim-manager/internal/providers/groups"
	"github.com/mathwro/pim-manager/internal/selfupdate"
	"github.com/mathwro/pim-manager/internal/tui"
)

var newCLI = azureauth.NewCLI
var newARMClient = arm.NewClient
var newGraphClient = graph.NewClient
var checkLatest = selfupdate.CheckLatest

var runProgram = func(model tea.Model) error {
	_, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}

func Run() error {
	auth := newCLI(nil)
	httpClient := &http.Client{Timeout: 60 * time.Second}
	armClient := newARMClient(httpClient, auth)
	graphSession := graphauth.NewSession(auth)
	defer graphSession.Reset()
	graphClient := newGraphClient(httpClient, graphSession)
	runtime := tui.Runtime{
		Entra:               entra.NewProvider(graphClient),
		Groups:              groups.NewProvider(graphClient),
		GraphConnect:        graphSession.Connect,
		GraphConnected:      graphSession.Connected,
		GraphAuthentication: graphSession.Authentication,
		ResetGraph:          graphSession.Reset,
		AzureResources:      azureresources.NewProvider(armClient),
		Tenants:             auth,
		CheckAccount:        auth.CheckAccount,
		StepUpCommand:       azureauth.StepUpLoginCommand,
		ARMAuthentication:   auth.ARMAuthentication,
		CheckUpdate:         checkLatest,
	}
	return runProgram(tui.NewModel(runtime))
}
