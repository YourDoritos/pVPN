package tui

import (
	"github.com/YourDoritos/pvpn/internal/api"
	"github.com/YourDoritos/pvpn/internal/vpn"
)

// Cross-view messages

type LoginSuccessMsg struct {
	VPNInfo *api.VPNInfoResponse
}

type ConnectRequestMsg struct {
	Server *api.LogicalServer
}

type ConnectedMsg struct {
	Info vpn.ConnectionInfo
	Conn *vpn.Connection
}

type DisconnectedMsg struct{}

type VPNStateChangedMsg struct {
	State vpn.State
}
