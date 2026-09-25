package authkey_test

import (
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/remoteconfig"
)

func TestGatewayAndRemoteCredentialsAreNotInterchangeable(t *testing.T) {
	masterKey := [32]byte{1, 2, 3}
	gatewayKey, err := gatewayconfig.GenerateAPIKey(masterKey, "gateway", time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	remoteKey, err := remoteconfig.GenerateAPIKey(masterKey, "remote", time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if remoteconfig.VerifyAPIKey(masterKey, gatewayKey.Record, gatewayKey.Secret) {
		t.Fatal("Remote accepted a Gateway credential")
	}
	if gatewayconfig.VerifyAPIKey(masterKey, remoteKey.Record, remoteKey.Secret) {
		t.Fatal("Gateway accepted a Remote credential")
	}

	remotePrefixedGatewaySecret := "qrk_" + strings.TrimPrefix(gatewayKey.Secret, "qk_")
	if remoteconfig.VerifyAPIKey(masterKey, gatewayKey.Record, remotePrefixedGatewaySecret) {
		t.Fatal("Remote accepted a Gateway hash under a Remote prefix")
	}
	gatewayPrefixedRemoteSecret := "qk_" + strings.TrimPrefix(remoteKey.Secret, "qrk_")
	if gatewayconfig.VerifyAPIKey(masterKey, remoteKey.Record, gatewayPrefixedRemoteSecret) {
		t.Fatal("Gateway accepted a Remote hash under a Gateway prefix")
	}
}
