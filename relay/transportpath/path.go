package transportpath

import "strings"

// ID identifies a transport fault domain, not a billing channel.
// Channels that share a listener/tunnel share an ID and are not failover.
type ID string

const (
	PrimaryTLS      ID = "cpa-primary-tls"
	FallbackSSH     ID = "cpa-fallback-ssh"
	LegacyLocal8317 ID = "cpa-legacy-127.0.0.1-8317"
)

func FromBaseURL(baseURL string) ID {
	u := strings.ToLower(strings.TrimSpace(baseURL))
	switch {
	case strings.Contains(u, ":8318"), strings.HasPrefix(u, "https://"):
		return PrimaryTLS
	case strings.Contains(u, "10.88.0.1:18318"), strings.Contains(u, "18318"):
		return FallbackSSH
	case strings.Contains(u, "127.0.0.1:8317"):
		return LegacyLocal8317
	default:
		return ID("url:" + u)
	}
}

func SameFaultDomain(a, b ID) bool {
	return a != "" && a == b
}

func SerialFailover(from, to ID) bool {
	return from != "" && to != "" && from != to
}
