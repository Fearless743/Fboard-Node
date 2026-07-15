package controlplane

import (
	"fmt"

	"github.com/fearless743/fboard-node/internal/config"
	"github.com/fearless743/fboard-node/internal/model"
	"github.com/fearless743/fboard-node/internal/panel"
)

// TranslateWSEvent converts a panel WebSocket event into the controlplane Event
// shape used by Service / NodeMailbox.
func TranslateWSEvent(event panel.WSEvent, kcfg config.KernelConfig) (Event, error) {
	translated := Event{Type: EventType(event.Type), DeltaAction: event.DeltaAction, DeviceUsers: event.DeviceUsers}
	if event.Config != nil {
		var err error
		translated.Config, err = model.NodeSpecFromPanelValidated(event.Config, kcfg)
		if err != nil {
			return Event{}, fmt.Errorf("translate node config: %w", err)
		}
	}
	if event.Users != nil {
		translated.Users = model.UserSpecsFromPanel(event.Users)
	}
	if event.DeltaUsers != nil {
		translated.DeltaUsers = model.UserSpecsFromPanel(event.DeltaUsers)
	}
	return translated, nil
}
