package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	bubkey "github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/infrahq/infra/api"
)

func runRootCmd(cli *CLI) error {
	client, err := cli.apiClient()
	if err != nil {
		return err
	}

	ctx := context.Background()

	_, dests, grants, err := getUserDestinationGrants(client, "")
	if err != nil {
		return err
	}

	model := newDestinationListModel(dests, grants)

	// TODO: mouse ProgramOptions
	program := tea.NewProgram(
		model,
		tea.WithAltScreen(), // TODO: try without this
		tea.WithContext(ctx),
	)
	result, err := program.Run()
	if err != nil {
		return err
	}

	destination := result.(*destinationListModel).selection
	if destination.Name == "" {
		return nil
	}

	switch destination.Kind {
	case "ssh":
		host, port := splitHostPortSSH(destination.Connection.URL)
		cli.Output("Connecting: ssh -p %v %v", port, host)
		cmd := exec.CommandContext(ctx, "ssh", "-p", port, host)
		return runCmd(cmd)
	case "kubernetes":
		// TODO: use an env var to set KUBECONFIG instead of 'infra use'
		cmd := exec.CommandContext(ctx, "infra", "use", destination.Name)
		if err := runCmd(cmd); err != nil {
			return err
		}

		cli.Output("Starting a shell with KUBECONFIG=/tmp/kubeconfig-124xyz\n")
		cmd = exec.CommandContext(ctx, "bash")
		return runCmd(cmd)
	}
	return nil
}

func runCmd(cmd *exec.Cmd) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

type destinationItem struct {
	destination api.Destination
	access      []string
}

func (i destinationItem) Title() string { return i.destination.Name }

func (i destinationItem) Description() string {
	// TODO: add access
	return fmt.Sprintf("%v %v",
		i.destination.Kind,
		i.destination.Connection.URL)
}

func (i destinationItem) FilterValue() string {
	return i.destination.Kind + " " + i.destination.Name + " " + i.destination.Connection.URL
}

var keyBindings = struct {
	connect bubkey.Binding
	quit    bubkey.Binding
}{
	connect: bubkey.NewBinding(
		bubkey.WithKeys("enter"),
		bubkey.WithHelp("enter", "connect to destination"),
	),
	quit: bubkey.NewBinding(
		bubkey.WithKeys("esc"),
		bubkey.WithHelp("esc", "exit"),
	),
}

func newDestinationListModel(dests []api.Destination, grants []api.Grant) *destinationListModel {
	items := make([]list.Item, 0, len(dests))
	for _, d := range dests {
		items = append(items, destinationItem{
			destination: d,
			access:      nil, // TODO:
		})
	}

	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.Copy().
		BorderForeground(lipgloss.AdaptiveColor{Light: "#3360C6", Dark: "#B1C8FF"}).
		Foreground(lipgloss.AdaptiveColor{Light: "#3360C6", Dark: "#B1C8FF"})
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedTitle.Copy().
		Foreground(lipgloss.AdaptiveColor{Light: "#B1C8FF", Dark: "#7A92C6"})

	l := list.New(items, delegate, 0, 0)
	l.Title = "Infra Destinations"
	l.Styles.Title = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(lipgloss.AdaptiveColor{Light: "#9FB4FF", Dark: "#0F60FF"}).
		Padding(0, 1)

	return &destinationListModel{
		appStyle:     lipgloss.NewStyle().Padding(1, 2),
		destinations: l,
	}
}

type destinationListModel struct {
	appStyle     lipgloss.Style
	destinations list.Model
	selection    api.Destination
}

func (d *destinationListModel) Init() tea.Cmd {
	return nil
}

func (d *destinationListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h, v := d.appStyle.GetFrameSize()
		d.destinations.SetSize(msg.Width-h, msg.Height-v)

	case tea.KeyMsg:
		// Don't match any of the keys below if we're actively filtering.
		if d.destinations.FilterState() == list.Filtering {
			break
		}

		switch {
		case bubkey.Matches(msg, keyBindings.connect):
			d.selection = d.destinations.SelectedItem().(destinationItem).destination
			return d, tea.Quit
		case bubkey.Matches(msg, keyBindings.quit):
			return d, tea.Quit
		}

	}

	var cmd tea.Cmd
	d.destinations, cmd = d.destinations.Update(msg)
	return d, cmd
}

func (d *destinationListModel) View() string {
	return d.appStyle.Render(d.destinations.View())
}

var _ tea.Model = (*destinationListModel)(nil)
