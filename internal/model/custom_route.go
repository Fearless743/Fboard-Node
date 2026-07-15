package model

type CustomRouteRule struct {
	Name     string
	Disabled bool
	Match    RouteMatch
	Action   RouteAction
}

type RouteMatch struct {
	Domains        []string
	DomainSuffixes []string
	IPCIDRs        []string
	Ports          []string
	Networks       []string
	SourceCIDRs    []string
	SourcePorts    []string
}

type RouteAction struct {
	Type   string
	Target string
}
