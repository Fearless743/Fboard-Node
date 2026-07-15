package model

// SupportedOutboundProtocols returns the outbound protocols accepted by xray.
func SupportedOutboundProtocols() []string {
	return []string{
		"vmess", "vless", "trojan", "shadowsocks",
		"socks", "http", "wireguard",
		"tuic", "hysteria2", "anytls", "naive", "mieru", "sudoku",
	}
}
