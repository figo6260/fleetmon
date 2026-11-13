package main

// Disk is one mounted filesystem's space report.
type Disk struct {
	Mount string `json:"mount"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

// Proc is one top process entry.
type Proc struct {
	Name string  `json:"name"`
	CPU  float64 `json:"cpu"` // percent of one core-equivalent total (can exceed 100 on multi-core sums); normalized to total CPU capacity
	Mem  float64 `json:"mem"` // percent of total memory
}

// Service is one watched item's online state.
type Service struct {
	Name   string `json:"name"`
	Up     bool   `json:"up"`
	Detail string `json:"detail,omitempty"`
}

// Report is what an agent sends to the server every interval.
type Report struct {
	Machine  string    `json:"machine"`
	Name     string    `json:"name"`
	Role     string    `json:"role"`
	Hostname string    `json:"hostname"`
	OS       string    `json:"os"`
	Time     int64     `json:"time"` // unix seconds, agent clock
	CPU      float64   `json:"cpu"`
	MemUsed  uint64    `json:"mem_used"`
	MemTotal uint64    `json:"mem_total"`
	Disks    []Disk    `json:"disks"`
	Top      []Proc    `json:"top"`
	Services []Service `json:"services"`
	Uptime   uint64    `json:"uptime"` // seconds
	Agent    bool      `json:"agent"`  // false for external-probe pseudo reports
}

// ServiceCheck configures one watched item on an agent.
type ServiceCheck struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`             // process | port | http | systemd
	Match  string `json:"match,omitempty"`  // process: substring of comm/cmdline
	Port   int    `json:"port,omitempty"`   // port: tcp port on 127.0.0.1
	URL    string `json:"url,omitempty"`    // http: full URL
	Expect int    `json:"expect,omitempty"` // http: expected status (default 200)
	Unit   string `json:"unit,omitempty"`   // systemd: unit name
}

// AgentConfig is the per-machine agent config file.
type AgentConfig struct {
	Server   string         `json:"server"`   // base URL of fleetmon server
	Token    string         `json:"token"`    // shared bearer token
	Machine  string         `json:"machine"`  // machine id, e.g. bj-brain
	Name     string         `json:"name"`     // display name (Chinese)
	Role     string         `json:"role"`     // short role label
	Interval int            `json:"interval"` // seconds
	Mounts   []string       `json:"mounts"`   // extra mounts to report
	Services []ServiceCheck `json:"services"`
}

// MachineInfo is the server-side registry entry for expected machines.
type MachineInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Role  string `json:"role"`
	Probe string `json:"probe,omitempty"` // optional URL for agentless external probe

	// Dashboard display overrides, set by the rename action. They only
	// change what the dashboard shows; the machine id (the agent's
	// identity key) and the agent's own reported name/role never change.
	DisplayName string `json:"display_name,omitempty"` // display-name override; empty = use Name
	Desc        string `json:"desc,omitempty"`         // description override; empty = use Role
}

// EffName is the name the dashboard shows for this machine: the rename
// override first, then the registry name, then the agent-reported name,
// then the machine id.
func (m MachineInfo) EffName(rep *Report) string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	if m.Name != "" {
		return m.Name
	}
	if rep != nil && rep.Name != "" {
		return rep.Name
	}
	return m.ID
}

// EffDesc is the description line the dashboard shows: the rename
// override first, then the registry role, then the agent-reported role.
func (m MachineInfo) EffDesc(rep *Report) string {
	if m.Desc != "" {
		return m.Desc
	}
	if m.Role != "" {
		return m.Role
	}
	if rep != nil {
		return rep.Role
	}
	return ""
}

// ServerConfig is the fleetmon server config file.
type ServerConfig struct {
	Addr        string        `json:"addr"`            // listen address, e.g. 0.0.0.0:8931
	Token       string        `json:"token"`           // agent bearer token
	DataDir     string        `json:"data_dir"`        // state + history storage
	WebUser     string        `json:"web_user"`        // dashboard basic-auth user
	WebPassSHA  string        `json:"web_pass_sha256"` // hex sha256 of dashboard password
	WebBasePath string        `json:"web_base_path"`   // public URL prefix when behind a stripping reverse proxy, e.g. "/fleet" (no trailing slash); "" when served at root
	Machines    []MachineInfo `json:"machines"`        // expected fleet registry
}
