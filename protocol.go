package daemon

import "fmt"

const protocolVersion = 1

type initializeParams struct {
	AppID           string `json:"appID"`
	InstanceID      string `json:"instanceID"`
	ProtocolVersion int    `json:"protocolVersion"`
	ClientVersion   string `json:"clientVersion"`
}

type initializeResult struct {
	AppID           string `json:"appID"`
	InstanceID      string `json:"instanceID"`
	ProtocolVersion int    `json:"protocolVersion"`
	ServerVersion   string `json:"serverVersion"`
	LaunchNonce     string `json:"launchNonce"`
}

func (p initializeParams) validate() error {
	if p.AppID == "" || p.InstanceID == "" || p.ClientVersion == "" {
		return fmt.Errorf("initialize: appID, instanceID and clientVersion are required")
	}
	if p.ProtocolVersion != protocolVersion {
		return fmt.Errorf("initialize: incompatible protocol version %d (want %d)", p.ProtocolVersion, protocolVersion)
	}
	return nil
}

func (r initializeResult) validate(p initializeParams) error {
	if err := p.validate(); err != nil {
		return err
	}
	if r.AppID != p.AppID || r.InstanceID != p.InstanceID {
		return fmt.Errorf("initialize: app or instance mismatch")
	}
	if r.ProtocolVersion != p.ProtocolVersion {
		return fmt.Errorf("initialize: incompatible server protocol version %d", r.ProtocolVersion)
	}
	if r.ServerVersion == "" || r.LaunchNonce == "" {
		return fmt.Errorf("initialize: serverVersion and launchNonce are required")
	}
	return nil
}
