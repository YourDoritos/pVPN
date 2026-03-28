package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/YourDoritos/pvpn/internal/api"
	"github.com/YourDoritos/pvpn/internal/config"
	"github.com/YourDoritos/pvpn/internal/tui"
	"github.com/YourDoritos/pvpn/internal/vpn"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "pvpn: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if err := config.EnsureDirs(); err != nil {
		return fmt.Errorf("setup directories: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	store, err := api.NewSessionStore(config.SessionFile())
	if err != nil {
		return fmt.Errorf("init session store: %w", err)
	}

	session, err := store.Load()
	if err != nil {
		session = nil
	}

	client := api.NewClient(session)
	client.OnTokenRefresh = func(uid, accessToken, refreshToken string) {
		s := &api.Session{UID: uid, AccessToken: accessToken, RefreshToken: refreshToken}
		store.Save(s)
	}

	// Safety net for standalone mode — clean up on exit
	defer vpn.CleanupIfNoTunnel()

	app := tui.NewApp(client, store, cfg)
	p := tea.NewProgram(app, tea.WithAltScreen())
	tui.SetProgram(p)

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}
