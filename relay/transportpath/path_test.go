package transportpath

import "testing"

func TestChannelsSharingLegacy8317AreOneFaultDomain(t *testing.T) {
	ch1 := FromBaseURL("http://127.0.0.1:8317")
	ch9 := FromBaseURL("http://127.0.0.1:8317/v1")
	ch11 := FromBaseURL("http://127.0.0.1:8317")
	if !SameFaultDomain(ch1, ch9) || !SameFaultDomain(ch1, ch11) {
		t.Fatal("channels 1/9/11/12 must not count as transport failover")
	}
	if SerialFailover(ch1, ch9) {
		t.Fatal("shared path is not serial failover")
	}
}

func TestPrimaryTLSAndFallbackSSHAreIndependent(t *testing.T) {
	primary := FromBaseURL("https://38.65.93.39:8318")
	fallback := FromBaseURL("http://10.88.0.1:18318")
	if SameFaultDomain(primary, fallback) {
		t.Fatal("TLS 8318 and SSH 18318 must be independent paths")
	}
	if !SerialFailover(primary, fallback) {
		t.Fatal("expected serial failover between disjoint paths")
	}
}
