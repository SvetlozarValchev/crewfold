package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"crewfold/internal/localapi"
)

// Run owns the terminal until the operator quits or the context is canceled.
func Run(ctx context.Context, config Config) error {
	model := NewModel(config, localapi.NewClient(config.SocketPath))
	model.ctx = ctx
	model.loadCancel()
	model.loadCtx, model.loadCancel = context.WithCancel(ctx)
	program := tea.NewProgram(model, tea.WithContext(ctx))
	_, err := program.Run()
	if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
