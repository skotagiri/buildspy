package main

import (
	"fmt"
	"log"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"buildspy/models"
)

// BuildSpyApp represents the main GUI application
type BuildSpyApp struct {
	window    fyne.Window
	container *container.Container
	client    *APIClient

	// Navigation
	currentView string

	// Views
	listView   *container.Container
	detailView *container.Container

	// Data
	liveBuilds      []*models.BuildRun
	completedBuilds []*models.BuildRun
}

func (app *BuildSpyApp) setupUI() {
	// Create navigation buttons
	listButton := widget.NewButton("Builds List", func() {
		app.showListView()
	})

	// Create main views
	app.listView = container.New(layout.NewVBoxLayout(),
		widget.NewLabel("📋 Build Runs"),
		widget.NewSeparator(),
		widget.NewLabel("Live Builds: 0"),
		widget.NewLabel("Completed Builds: 0"),
		widget.NewButton("Refresh", func() {
			app.refreshData()
		}),
	)

	app.detailView = container.New(layout.NewVBoxLayout(),
		widget.NewLabel("📊 Build Details"),
		widget.NewSeparator(),
		widget.NewLabel("Select a build from the list to view details"),
	)

	// Create toolbar
	toolbar := container.New(layout.NewHBoxLayout(),
		listButton,
		widget.NewButton("Refresh All", func() {
			app.refreshData()
		}),
		layout.NewSpacer(),
		widget.NewLabel("BuildSpy GUI v1.0"),
	)

	// Create main container with split layout
	content := container.New(layout.NewBorderLayout(
		toolbar, nil, nil, nil,
	),
		toolbar,
		app.listView,
	)

	app.container = content
	app.currentView = "list"
}

func (app *BuildSpyApp) showListView() {
	if app.currentView != "list" {
		app.container.Objects = app.container.Objects[:1] // Keep toolbar
		app.container.Add(app.listView)
		app.container.Refresh()
		app.currentView = "list"
	}
}

func (app *BuildSpyApp) showDetailView() {
	if app.currentView != "detail" {
		app.container.Objects = app.container.Objects[:1] // Keep toolbar
		app.container.Add(app.detailView)
		app.container.Refresh()
		app.currentView = "detail"
	}
}

func main() {
	// Create new Fyne application
	myApp := app.New()
	myApp.SetIcon(nil) // TODO: Add BuildSpy icon

	// Create main window
	myWindow := myApp.NewWindow("BuildSpy GUI")
	myWindow.Resize(fyne.NewSize(1200, 800))

	// Create API client
	client := NewAPIClient("http://localhost:8080")

	// Test daemon connection
	if err := client.CheckDaemonHealth(); err != nil {
		log.Printf("Warning: Cannot connect to daemon: %v", err)
	}

	// Create main UI components
	app := &BuildSpyApp{
		window: myWindow,
		client: client,
	}
	app.setupUI()

	myWindow.SetContent(app.container)
	// Load initial data
	app.refreshData()

	myWindow.ShowAndRun()
}

// refreshData fetches latest data from daemon
func (app *BuildSpyApp) refreshData() {
	// Fetch live builds
	if liveBuilds, err := app.client.GetLiveBuilds(); err == nil {
		app.liveBuilds = liveBuilds
	} else {
		log.Printf("Error fetching live builds: %v", err)
	}

	// Fetch completed builds
	if completedBuilds, err := app.client.GetBuildRuns("", 50); err == nil {
		app.completedBuilds = completedBuilds
	} else {
		log.Printf("Error fetching completed builds: %v", err)
	}

	app.updateUI()
}

// updateUI refreshes the UI with current data
func (app *BuildSpyApp) updateUI() {
	// Update list view labels
	if len(app.listView.Objects) >= 3 {
		liveLabel := app.listView.Objects[2].(*widget.Label)
		completedLabel := app.listView.Objects[3].(*widget.Label)

		liveLabel.SetText(fmt.Sprintf("Live Builds: %d", len(app.liveBuilds)))
		completedLabel.SetText(fmt.Sprintf("Completed Builds: %d", len(app.completedBuilds)))
	}
}
