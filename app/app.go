package app

import (
	"claude-squad/config"
	"claude-squad/keys"
	"claude-squad/log"
	"claude-squad/session"
	"claude-squad/ui"
	"claude-squad/ui/overlay"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const GlobalInstanceLimit = 10

// Run is the main entrypoint into the application.
func Run(ctx context.Context, program string, autoYes bool) error {
	p := tea.NewProgram(
		newHome(ctx, program, autoYes),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(), // Mouse scroll
	)
	_, err := p.Run()
	return err
}



type state int

const (
	stateDefault state = iota
	// stateNew is the state when the user is creating a new instance.
	stateNew
	// statePrompt is the state when the user is entering a prompt.
	statePrompt
	// stateHelp is the state when a help screen is displayed.
	stateHelp
)

type home struct {
	ctx context.Context

	program string
	autoYes bool

	// ui components
	list         *ui.List
	menu         *ui.Menu
	tabbedWindow *ui.TabbedWindow
	errBox       *ui.ErrBox
	// global spinner instance. we plumb this down to where it's needed
	spinner spinner.Model

	// storage is the interface for saving/loading data to/from the app's state
	storage *session.Storage
	// appConfig stores persistent application configuration
	appConfig *config.Config
	// appState stores persistent application state like seen help screens
	appState config.AppState

	// state
	state state
	// newInstanceFinalizer is called when the state is stateNew and then you press enter.
	// It registers the new instance in the list after the instance has been started.
	newInstanceFinalizer func()

	// promptAfterName tracks if we should enter prompt mode after naming
	promptAfterName bool

	// textInputOverlay is the component for handling text input with state
	textInputOverlay *overlay.TextInputOverlay

	// textOverlay is the component for displaying text information
	textOverlay *overlay.TextOverlay

	// keySent is used to manage underlining menu items
	keySent bool
}

func newHome(ctx context.Context, program string, autoYes bool) *home {
	// Load application config
	appConfig := config.LoadConfig()

	// Load application state
	appState := config.LoadState()

	// Initialize storage
	storage, err := session.NewStorage(appState)
	if err != nil {
		fmt.Printf("Failed to initialize storage: %v\n", err)
		os.Exit(1)
	}

	h := &home{
		ctx:          ctx,
		spinner:      spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane()),
		errBox:       ui.NewErrBox(),
		storage:      storage,
		appConfig:    appConfig,
		program:      program,
		autoYes:      autoYes,
		state:        stateDefault,
		appState:     appState,
	}
	h.list = ui.NewList(&h.spinner, autoYes)

	// Load saved instances
	instances, err := storage.LoadInstances()
	if err != nil {
		fmt.Printf("Failed to load instances: %v\n", err)
		os.Exit(1)
	}

	// Add loaded instances to the list
	for _, instance := range instances {
		// Call the finalizer immediately.
		h.list.AddInstance(instance)()
		if autoYes {
			instance.AutoYes = true
		}
	}

	return h
}

// updateHandleWindowSizeEvent sets the sizes of the components.
// The components will try to render inside their bounds.
func (m *home) updateHandleWindowSizeEvent(msg tea.WindowSizeMsg) {
	// List takes 30% of width, preview takes 70%
	listWidth := int(float32(msg.Width) * 0.3)
	tabsWidth := msg.Width - listWidth

	// Menu takes 10% of height, list and window take 90%
	contentHeight := int(float32(msg.Height) * 0.9)
	menuHeight := msg.Height - contentHeight - 1     // minus 1 for error box
	m.errBox.SetSize(int(float32(msg.Width)*0.9), 1) // error box takes 1 row

	m.tabbedWindow.SetSize(tabsWidth, contentHeight)
	m.list.SetSize(listWidth, contentHeight)

	if m.textInputOverlay != nil {
		m.textInputOverlay.SetSize(int(float32(msg.Width)*0.6), int(float32(msg.Height)*0.4))
	}
	if m.textOverlay != nil {
		m.textOverlay.SetWidth(int(float32(msg.Width) * 0.6))
	}

	previewWidth, previewHeight := m.tabbedWindow.GetPreviewSize()
	if err := m.list.SetSessionPreviewSize(previewWidth, previewHeight); err != nil {
		log.ErrorLog.Print(err)
	}
	m.menu.SetSize(msg.Width, menuHeight)
}

func (m *home) Init() tea.Cmd {
	// Start spinner, file watching, and restore instance status polling
	return tea.Batch(
		m.spinner.Tick,
		m.startFileWatcher(), // Watch for storage changes
		tickUpdateMetadataCmd, // Restore polling for existing instance status
	)
}

func (m *home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case hideErrMsg:
		m.errBox.Clear()
	case keyupMsg:
		m.menu.ClearKeydown()
		return m, nil
	case forceRefreshMsg:
		// Only refresh when explicitly requested (e.g., after user action)
		cmd := m.instanceChanged()
		// Also check for new instances when explicitly refreshing
		if err := m.loadNewInstancesFromStorage(); err != nil {
			log.WarningLog.Printf("could not load new instances: %v", err)
		}
		return m, cmd
	case storageChangedMsg:
		// Storage file changed - new instances might be available
		if err := m.loadNewInstancesFromStorage(); err != nil {
			log.WarningLog.Printf("could not load new instances after storage change: %v", err)
		}
		return m, tea.Batch(m.instanceChanged(), m.startFileWatcher()) // Restart watcher
	case tickUpdateMetadataMessage:
		// Update existing instance status - this is what was missing!
		for _, instance := range m.list.GetInstances() {
			if !instance.Started() || instance.Paused() {
				continue
			}
			updated, prompt := instance.HasUpdated()
			if updated {
				instance.SetStatus(session.Running)
			} else {
				if prompt {
					instance.TapEnter()
				} else {
					instance.SetStatus(session.Ready)
				}
			}
			if err := instance.UpdateDiffStats(); err != nil {
				log.WarningLog.Printf("could not update diff stats: %v", err)
			}
		}
		return m, tickUpdateMetadataCmd
	case tea.MouseMsg:
		// Handle mouse wheel scrolling in the diff view
		if m.tabbedWindow.IsInDiffTab() {
			if msg.Action == tea.MouseActionPress {
				switch msg.Button {
				case tea.MouseButtonWheelUp:
					m.tabbedWindow.ScrollUp()
					return m, m.instanceChanged()
				case tea.MouseButtonWheelDown:
					m.tabbedWindow.ScrollDown()
					return m, m.instanceChanged()
				}
			}
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKeyPress(msg)
	case tea.WindowSizeMsg:
		m.updateHandleWindowSizeEvent(msg)
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *home) handleQuit() (tea.Model, tea.Cmd) {
	if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
		return m, m.handleError(err)
	}
	return m, tea.Quit
}

func (m *home) handleMenuHighlighting(msg tea.KeyMsg) (cmd tea.Cmd, returnEarly bool) {
	// Handle menu highlighting when you press a button. We intercept it here and immediately return to
	// update the ui while re-sending the keypress. Then, on the next call to this, we actually handle the keypress.
	if m.keySent {
		m.keySent = false
		return nil, false
	}
	if m.state == statePrompt || m.state == stateHelp {
		return nil, false
	}
	// If it's in the global keymap, we should try to highlight it.
	name, ok := keys.GlobalKeyStringsMap[msg.String()]
	if !ok {
		return nil, false
	}

	if m.list.GetSelectedInstance() != nil && m.list.GetSelectedInstance().Paused() && name == keys.KeyEnter {
		return nil, false
	}
	if name == keys.KeyShiftDown || name == keys.KeyShiftUp {
		return nil, false
	}

	// Skip the menu highlighting if the key is not in the map or we are using the shift up and down keys.
	// TODO: cleanup: when you press enter on stateNew, we use keys.KeySubmitName. We should unify the keymap.
	if name == keys.KeyEnter && m.state == stateNew {
		name = keys.KeySubmitName
	}
	m.keySent = true
	return tea.Batch(
		func() tea.Msg { return msg },
		m.keydownCallback(name)), true
}

func (m *home) handleKeyPress(msg tea.KeyMsg) (mod tea.Model, cmd tea.Cmd) {
	cmd, returnEarly := m.handleMenuHighlighting(msg)
	if returnEarly {
		return m, cmd
	}

	if m.state == stateHelp {
		return m.handleHelpState(msg)
	}

	if m.state == stateNew {
		// Handle quit commands first. Don't handle q because the user might want to type that.
		if msg.String() == "ctrl+c" {
			m.state = stateDefault
			m.promptAfterName = false
			m.list.Kill()
			return m, tea.Sequence(
				tea.WindowSize(),
				func() tea.Msg {
					m.menu.SetState(ui.StateDefault)
					return nil
				},
			)
		}

		instance := m.list.GetInstances()[m.list.NumInstances()-1]
		switch msg.Type {
		// Start the instance (enable previews etc) and go back to the main menu state.
		case tea.KeyEnter:
			if len(instance.Title) == 0 {
				return m, m.handleError(fmt.Errorf("title cannot be empty"))
			}

			if err := instance.Start(true); err != nil {
				m.list.Kill()
				m.state = stateDefault
				return m, m.handleError(err)
			}
			// Save after adding new instance
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
			// Instance added successfully, call the finalizer.
			m.newInstanceFinalizer()
			if m.autoYes {
				instance.AutoYes = true
			}

			m.newInstanceFinalizer()
			m.state = stateDefault
			if m.promptAfterName {
				m.state = statePrompt
				m.menu.SetState(ui.StatePrompt)
				// Initialize the text input overlay
				m.textInputOverlay = overlay.NewTextInputOverlay("Enter prompt", "")
				m.promptAfterName = false
			} else {
				m.menu.SetState(ui.StateDefault)
				m.showHelpScreen(helpTypeInstanceStart, nil)
			}

			return m, tea.Batch(tea.WindowSize(), m.instanceChanged(), func() tea.Msg { return forceRefreshMsg{} })
		case tea.KeyRunes:
			if len(instance.Title) >= 32 {
				return m, m.handleError(fmt.Errorf("title cannot be longer than 32 characters"))
			}
			if err := instance.SetTitle(instance.Title + string(msg.Runes)); err != nil {
				return m, m.handleError(err)
			}
		case tea.KeyBackspace:
			if len(instance.Title) == 0 {
				return m, nil
			}
			if err := instance.SetTitle(instance.Title[:len(instance.Title)-1]); err != nil {
				return m, m.handleError(err)
			}
		case tea.KeySpace:
			if err := instance.SetTitle(instance.Title + " "); err != nil {
				return m, m.handleError(err)
			}
		case tea.KeyEsc:
			m.list.Kill()
			m.state = stateDefault
			m.instanceChanged()

			return m, tea.Sequence(
				tea.WindowSize(),
				func() tea.Msg {
					m.menu.SetState(ui.StateDefault)
					return nil
				},
			)
		default:
		}
		return m, nil
	} else if m.state == statePrompt {
		// Use the new TextInputOverlay component to handle all key events
		shouldClose := m.textInputOverlay.HandleKeyPress(msg)

		// Check if the form was submitted or canceled
		if shouldClose {
			if m.textInputOverlay.IsSubmitted() {
				// Form was submitted, process the input
				selected := m.list.GetSelectedInstance()
				if selected == nil {
					return m, nil
				}
				if err := selected.SendPrompt(m.textInputOverlay.GetValue()); err != nil {
					return m, m.handleError(err)
				}
			}

			// Close the overlay and reset state
			m.textInputOverlay = nil
			m.state = stateDefault
			return m, tea.Sequence(
				tea.WindowSize(),
				func() tea.Msg {
					m.menu.SetState(ui.StateDefault)
					m.showHelpScreen(helpTypeInstanceStart, nil)
					return nil
				},
			)
		}

		return m, nil
	}

	// Handle quit commands first
	if msg.String() == "ctrl+c" || msg.String() == "q" {
		return m.handleQuit()
	}

	name, ok := keys.GlobalKeyStringsMap[msg.String()]
	if !ok {
		return m, nil
	}

	switch name {
	case keys.KeyHelp:
		return m.showHelpScreen(helpTypeGeneral, nil)
	case keys.KeyPrompt:
		if m.list.NumInstances() >= GlobalInstanceLimit {
			return m, m.handleError(
				fmt.Errorf("you can't create more than %d instances", GlobalInstanceLimit))
		}
		instance, err := session.NewInstance(session.InstanceOptions{
			Title:   "",
			Program: m.program,
		})
		if err != nil {
			return m, m.handleError(err)
		}

		m.newInstanceFinalizer = m.list.AddInstance(instance)
		m.list.SetSelectedInstance(m.list.NumInstances() - 1)
		m.state = stateNew
		m.menu.SetState(ui.StateNewInstance)
		m.promptAfterName = true

		return m, nil
	case keys.KeyNew:
		if m.list.NumInstances() >= GlobalInstanceLimit {
			return m, m.handleError(
				fmt.Errorf("you can't create more than %d instances", GlobalInstanceLimit))
		}
		instance, err := session.NewInstance(session.InstanceOptions{
			Title:   "",
			Program: m.program,
		})
		if err != nil {
			return m, m.handleError(err)
		}

		m.newInstanceFinalizer = m.list.AddInstance(instance)
		m.list.SetSelectedInstance(m.list.NumInstances() - 1)
		m.state = stateNew
		m.menu.SetState(ui.StateNewInstance)

		return m, nil
	case keys.KeyUp:
		m.list.Up()
		return m, m.instanceChanged()
	case keys.KeyDown:
		m.list.Down()
		return m, m.instanceChanged()
	case keys.KeyShiftUp:
		if m.tabbedWindow.IsInDiffTab() {
			m.tabbedWindow.ScrollUp()
		}
		return m, m.instanceChanged()
	case keys.KeyShiftDown:
		if m.tabbedWindow.IsInDiffTab() {
			m.tabbedWindow.ScrollDown()
		}
		return m, m.instanceChanged()
	case keys.KeyTab:
		m.tabbedWindow.Toggle()
		m.menu.SetInDiffTab(m.tabbedWindow.IsInDiffTab())
		return m, m.instanceChanged()
	case keys.KeyKill:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			log.InfoLog.Printf("DELETE: No instance selected")
			return m, nil
		}
		log.InfoLog.Printf("DELETE: Attempting to delete instance: %s", selected.Title)

		// Check if instance has git worktree (agents might not)
		worktree, err := selected.GetGitWorktree()
		if err != nil {
			// If no git worktree (like agent instances), skip the checkout check
			if !strings.Contains(err.Error(), "no git worktree") {
				return m, m.handleError(err)
			}
			log.InfoLog.Printf("Deleting instance without git worktree: %s", selected.Title)
		} else {
			// Regular instance with git worktree - check if branch is checked out
			checkedOut, err := worktree.IsBranchCheckedOut()
			if err != nil {
				return m, m.handleError(err)
			}

			if checkedOut {
				return m, m.handleError(fmt.Errorf("instance %s is currently checked out", selected.Title))
			}
		}

		// Delete from storage first
		if err := m.storage.DeleteInstance(selected.Title); err != nil {
			return m, m.handleError(err)
		}

		// Then kill the instance
		m.list.Kill()
		return m, tea.Batch(m.instanceChanged(), func() tea.Msg { return forceRefreshMsg{} })
	case keys.KeySubmit:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}

		// Default commit message with timestamp
		commitMsg := fmt.Sprintf("[claudesquad] update from '%s' on %s", selected.Title, time.Now().Format(time.RFC822))
		worktree, err := selected.GetGitWorktree()
		if err != nil {
			return m, m.handleError(err)
		}
		if err = worktree.PushChanges(commitMsg, true); err != nil {
			return m, m.handleError(err)
		}

		return m, nil
	case keys.KeyCheckout:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}

		// Show help screen before pausing
		m.showHelpScreen(helpTypeInstanceCheckout, func() {
			if err := selected.Pause(); err != nil {
				m.handleError(err)
			}
			m.instanceChanged()
		})
		return m, nil
	case keys.KeyResume:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		if err := selected.Resume(); err != nil {
			return m, m.handleError(err)
		}
		return m, tea.WindowSize()
	case keys.KeyForceDelete:
		// Force delete all persistent agents
		return m, m.forceDeleteAgents()
	case keys.KeyEnter:
		if m.list.NumInstances() == 0 {
			return m, nil
		}
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Paused() || !selected.TmuxAlive() {
			return m, nil
		}
		// Show help screen before attaching
		m.showHelpScreen(helpTypeInstanceAttach, func() {
			ch, err := m.list.Attach()
			if err != nil {
				m.handleError(err)
				return
			}
			<-ch
			m.state = stateDefault
		})
		return m, nil
	default:
		return m, nil
	}
}

// instanceChanged updates the preview pane, menu, and diff pane based on the selected instance. It returns an error
// Cmd if there was any error.
func (m *home) instanceChanged() tea.Cmd {
	// selected may be nil
	selected := m.list.GetSelectedInstance()

	m.tabbedWindow.UpdateDiff(selected)
	// Update menu with current instance
	m.menu.SetInstance(selected)

	// If there's no selected instance, we don't need to update the preview.
	if err := m.tabbedWindow.UpdatePreview(selected); err != nil {
		return m.handleError(err)
	}
	return nil
}

type keyupMsg struct{}

// keydownCallback clears the menu option highlighting after 500ms.
func (m *home) keydownCallback(name keys.KeyName) tea.Cmd {
	m.menu.Keydown(name)
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}

		return keyupMsg{}
	}
}

// hideErrMsg implements tea.Msg and clears the error text from the screen.
type hideErrMsg struct{}

// forceRefreshMsg implements tea.Msg and triggers a one-time refresh
type forceRefreshMsg struct{}

// storageChangedMsg implements tea.Msg and indicates storage file was modified
type storageChangedMsg struct{}

// tickUpdateMetadataMessage implements tea.Msg for instance status polling
type tickUpdateMetadataMessage struct{}

// handleError handles all errors which get bubbled up to the app. sets the error message. We return a callback tea.Cmd that returns a hideErrMsg message
// which clears the error message after 3 seconds.
func (m *home) handleError(err error) tea.Cmd {
	log.ErrorLog.Printf("%v", err)
	m.errBox.SetError(err)
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
		case <-time.After(3 * time.Second):
		}

		return hideErrMsg{}
	}
}

func (m *home) View() string {
	listWithPadding := lipgloss.NewStyle().PaddingTop(1).Render(m.list.String())
	previewWithPadding := lipgloss.NewStyle().PaddingTop(1).Render(m.tabbedWindow.String())
	listAndPreview := lipgloss.JoinHorizontal(lipgloss.Top, listWithPadding, previewWithPadding)

	mainView := lipgloss.JoinVertical(
		lipgloss.Center,
		listAndPreview,
		m.menu.String(),
		m.errBox.String(),
	)

	if m.state == statePrompt {
		if m.textInputOverlay == nil {
			log.ErrorLog.Printf("text input overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.textInputOverlay.Render(), mainView, true, true)
	} else if m.state == stateHelp {
		if m.textOverlay == nil {
			log.ErrorLog.Printf("text overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.textOverlay.Render(), mainView, true, true)
	}

	return mainView
}

// loadNewInstancesFromStorage checks for new instances in storage and adds them to the list
func (m *home) loadNewInstancesFromStorage() error {
	// Reload state to get latest data (in case MCP server updated it)
	freshState := config.LoadState()
	freshStorage, err := session.NewStorage(freshState)
	if err != nil {
		return err
	}
	
	// Load all instances from fresh storage
	instances, err := freshStorage.LoadInstances()
	if err != nil {
		return err
	}
	
	log.InfoLog.Printf("UI: Loaded %d instances from storage", len(instances))
	
	// Get current instance titles
	currentInstances := make(map[string]bool)
	for _, instance := range m.list.GetInstances() {
		currentInstances[instance.Title] = true
	}
	
	log.InfoLog.Printf("UI: Currently have %d instances in list", len(currentInstances))
	
	// Add any new instances that aren't already in the list
	newCount := 0
	for _, instance := range instances {
		if !currentInstances[instance.Title] {
			// New instance found, add it to the list
			m.list.AddInstance(instance)()
			if m.autoYes {
				instance.AutoYes = true
			}
			log.InfoLog.Printf("UI: Added new instance from storage: %s", instance.Title)
			newCount++
		}
	}
	
	if newCount > 0 {
		log.InfoLog.Printf("UI: Added %d new instances", newCount)
	}
	
	return nil
}

// startFileWatcher watches the state file for changes and triggers updates
func (m *home) startFileWatcher() tea.Cmd {
	return func() tea.Msg {
		// Very simple approach: check occasionally for changes
		time.Sleep(3 * time.Second) // Wait a bit, then check
		return storageChangedMsg{}
	}
}

// tickUpdateMetadataCmd polls instance status every 500ms (original claude-squad behavior)
var tickUpdateMetadataCmd = func() tea.Msg {
	time.Sleep(500 * time.Millisecond) // Restore original 500ms polling
	return tickUpdateMetadataMessage{}
}

// forceDeleteAgents manually removes all agent instances from storage and UI
func (m *home) forceDeleteAgents() tea.Cmd {
	return func() tea.Msg {
		// Get config directory
		configDir, err := config.GetConfigDir()
		if err != nil {
			log.ErrorLog.Printf("Failed to get config directory: %v", err)
			return nil
		}

		// Read storage file directly
		stateFilePath := filepath.Join(configDir, "state.json")
		data, err := os.ReadFile(stateFilePath)
		if err != nil {
			log.ErrorLog.Printf("Failed to read state file: %v", err)
			return nil
		}

		// Parse JSON
		var state struct {
			HelpScreensSeen int                `json:"help_screens_seen"`
			Instances       []*session.Instance `json:"instances"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			log.ErrorLog.Printf("Failed to parse state file: %v", err)
			return nil
		}

		// Filter out all agent instances and kill them in the list
		var cleanInstances []*session.Instance
		var removedTitles []string
		for _, instance := range state.Instances {
			if !strings.HasPrefix(instance.Title, "Agent-agent-") {
				cleanInstances = append(cleanInstances, instance)
			} else {
				removedTitles = append(removedTitles, instance.Title)
				// Kill from UI list if present
				for i, listInstance := range m.list.GetInstances() {
					if listInstance.Title == instance.Title {
						m.list.SetSelectedInstance(i)
						m.list.Kill()
						break
					}
				}
			}
		}

		// Create new state
		newState := struct {
			HelpScreensSeen int                `json:"help_screens_seen"`
			Instances       []*session.Instance `json:"instances"`
		}{
			HelpScreensSeen: state.HelpScreensSeen,
			Instances:       cleanInstances,
		}

		// Write back to file
		newData, err := json.MarshalIndent(newState, "", "  ")
		if err != nil {
			log.ErrorLog.Printf("Failed to marshal new state: %v", err)
			return nil
		}

		// Backup original file first
		backupPath := stateFilePath + ".backup"
		if err := os.WriteFile(backupPath, data, 0644); err != nil {
			log.ErrorLog.Printf("Failed to create backup: %v", err)
		}

		// Write new state
		if err := os.WriteFile(stateFilePath, newData, 0644); err != nil {
			log.ErrorLog.Printf("Failed to write new state file: %v", err)
			return nil
		}

		log.InfoLog.Printf("FORCE DELETE: Removed %d agent instances: %v", len(removedTitles), removedTitles)
		log.InfoLog.Printf("FORCE DELETE: Preserved %d non-agent instances", len(cleanInstances))
		log.InfoLog.Printf("FORCE DELETE: Backup saved to: %s", backupPath)

		return forceRefreshMsg{}
	}
}
