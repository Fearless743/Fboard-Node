package model

// SupportedRouteMatchers returns route match fields accepted by xray.
func SupportedRouteMatchers() []string {
	return []string{
		"domains",
		"domain_suffixes",
		"ip_cidrs",
		"ports",
		"networks",
		"source_cidrs",
		"source_ports",
	}
}

// SupportedRouteActions returns route action types accepted by xray.
func SupportedRouteActions() []string {
	return []string{"block", "direct", "route"}
}
