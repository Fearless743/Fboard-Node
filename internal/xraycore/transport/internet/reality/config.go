package reality

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"time"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"github.com/xtls/reality"
	"github.com/fearless743/fboard-node/internal/xraycore/common/errors"
	"github.com/fearless743/fboard-node/internal/xraycore/transport/internet"
	"golang.org/x/crypto/cryptobyte"
)

func (c *Config) GetREALITYConfig() *reality.Config {
	var dialer net.Dialer
	config := &reality.Config{
		DialContext: dialer.DialContext,

		Show: c.Show,
		Type: c.Type,
		Dest: c.Dest,
		Xver: byte(c.Xver),

		PrivateKey:   c.PrivateKey,
		MinClientVer: c.MinClientVer,
		MaxClientVer: c.MaxClientVer,
		MaxTimeDiff:  time.Duration(c.MaxTimeDiff) * time.Millisecond,

		NextProtos:             nil, // should be nil
		SessionTicketsDisabled: true,

		KeyLogWriter: KeyLogWriterFromConfig(c),
	}
	if c.Mldsa65Seed != nil {
		_, key := mldsa65.NewKeyFromSeed((*[32]byte)(c.Mldsa65Seed))
		config.Mldsa65Key = key.Bytes()
	}
	if c.LimitFallbackUpload != nil {
		config.LimitFallbackUpload.AfterBytes = c.LimitFallbackUpload.AfterBytes
		config.LimitFallbackUpload.BytesPerSec = c.LimitFallbackUpload.BytesPerSec
		config.LimitFallbackUpload.BurstBytesPerSec = c.LimitFallbackUpload.BurstBytesPerSec
	}
	if c.LimitFallbackDownload != nil {
		config.LimitFallbackDownload.AfterBytes = c.LimitFallbackDownload.AfterBytes
		config.LimitFallbackDownload.BytesPerSec = c.LimitFallbackDownload.BytesPerSec
		config.LimitFallbackDownload.BurstBytesPerSec = c.LimitFallbackDownload.BurstBytesPerSec
	}
	config.ServerNames = make(map[string]bool)
	for _, serverName := range c.ServerNames {
		config.ServerNames[serverName] = true
	}
	config.ShortIds = make(map[[8]byte]bool)
	for _, shortId := range c.ShortIds {
		config.ShortIds[*(*[8]byte)(shortId)] = true
	}
	if len(c.EchServerKeys) > 0 {
		keys, err := echKeysFromKeySetList(c.EchServerKeys)
		if err != nil {
			errors.LogWarningInner(context.Background(), err, "REALITY: failed to parse echServerKeys, ECH disabled")
		} else {
			config.EncryptedClientHelloKeys = keys
		}
	}
	return config
}

// echKeysFromKeySetList converts an ECHKeySetList (the same binary format used
// by TLS's echServerKeys) into the ECH private keys expected by the REALITY
// library. Each entry is: uint16 key_len | key | uint16 config_len | config.
func echKeysFromKeySetList(data []byte) ([]reality.EncryptedClientHelloKey, error) {
	var keys []reality.EncryptedClientHelloKey
	s := cryptobyte.String(data)
	for !s.Empty() {
		if len(s) < 2 {
			return nil, errInvalidECHKeyLen
		}
		keyLen := int(binary.BigEndian.Uint16(s[:2]))
		if len(s) < keyLen+4 {
			return nil, errInvalidECHKeyLen
		}
		configLen := int(binary.BigEndian.Uint16(s[keyLen+2 : keyLen+4]))
		if len(s) < 2+keyLen+2+configLen {
			return nil, errInvalidECHKeyLen
		}
		child := cryptobyte.String(s[:2+keyLen+2+configLen])
		var sk, config cryptobyte.String
		if !child.ReadUint16LengthPrefixed(&sk) || !child.ReadUint16LengthPrefixed(&config) || !child.Empty() {
			return nil, errInvalidECHKeyLen
		}
		if !s.Skip(2 + keyLen + 2 + configLen) {
			return nil, errInvalidECHKeyLen
		}
		keys = append(keys, reality.EncryptedClientHelloKey{
			Config:     config,
			PrivateKey: sk,
		})
	}
	return keys, nil
}

var errInvalidECHKeyLen = errors.New("REALITY: invalid echServerKeys length")

func KeyLogWriterFromConfig(c *Config) io.Writer {
	if len(c.MasterKeyLog) <= 0 || c.MasterKeyLog == "none" {
		return nil
	}

	writer, err := os.OpenFile(c.MasterKeyLog, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		errors.LogErrorInner(context.Background(), err, "failed to open ", c.MasterKeyLog, " as master key log")
	}

	return writer
}

func ConfigFromStreamSettings(settings *internet.MemoryStreamConfig) *Config {
	if settings == nil {
		return nil
	}
	config, ok := settings.SecuritySettings.(*Config)
	if !ok {
		return nil
	}
	return config
}
