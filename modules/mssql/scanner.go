// Package mssql provides the zgrab2 scanner module for the MSSQL protocol.
// Default Port: 1433 (TCP)
//
// The --encrypt-mode flag allows setting an explicit client encryption mode
// (the default is ENCRYPT_ON). Note: only ENCRYPT_NOT_SUP will skip the TLS
// handshake, since even ENCRYPT_OFF uses TLS for the login step.
//
// The scan performs a PRELOGIN and if possible does a TLS handshake.
//
// The output is the the server version and instance name, and if applicable the
// TLS output.
package mssql

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/zmap/zgrab2"
)

// ScanResults contains detailed information about each step of the
// MSSQL handshake, and can be encoded to JSON.
type ScanResults struct {
	// Version is the version returned by the server in the PRELOGIN response.
	// Its format is "MAJOR.MINOR.BUILD_NUMBER".
	Version string `json:"version,omitempty"`

	// InstanceName is the value of the INSTANCE field returned by the server
	// in the PRELOGIN response. Using a pointer to distinguish between the
	// server returning an empty name and no name being returned.
	InstanceName *string `json:"instance_name,omitempty"`

	// PreloginOptions are the raw key-value pairs returned by the server in
	// response to the PRELOGIN call. Debug only.
	PreloginOptions *PreloginOptions `json:"prelogin_options,omitempty" zgrab:"debug"`

	// EncryptMode is the mode negotiated with the server.
	EncryptMode *EncryptMode `json:"encrypt_mode,omitempty"`

	// TLSLog is the shared TLS handshake/scan log.
	TLSLog *zgrab2.TLSLog `json:"tls,omitempty"`
}

// Flags defines the command-line configuration options for the module.
type Flags struct {
	zgrab2.BaseFlags `group:"Basic Options"`
	zgrab2.TLSFlags  `group:"TLS Options"`
	EncryptMode      string `long:"encrypt-mode" description:"The type of encryption to request in the pre-login step. One of ENCRYPT_ON, ENCRYPT_OFF, ENCRYPT_NOT_SUP." default:"ENCRYPT_ON"`
	Verbose          bool   `long:"verbose" description:"More verbose logging, include debug fields in the scan results"`
}

// Module is the implementation of zgrab2.Module for the MSSQL protocol.
type Module struct {
}

// Scanner is the implementation of zgrab2.Scanner for the MSSQL protocol.
type Scanner struct {
	config            *Flags
	dialerGroupConfig *zgrab2.DialerGroupConfig
}

// NewFlags returns a default Flags instance to be populated by the command
// line flags.
func (module *Module) NewFlags() any {
	return new(Flags)
}

// NewScanner returns a new Scanner instance.
func (module *Module) NewScanner() zgrab2.Scanner {
	return new(Scanner)
}

// Description returns an overview of this module.
func (module *Module) Description() string {
	return "Perform a handshake for MSSQL databases"
}

// Validate does nothing in this module.
func (flags *Flags) Validate(_ []string) error {
	return nil
}

// Help returns the help string for this module.
func (flags *Flags) Help() string {
	return ""
}

// Init initializes the Scanner instance with the given command-line flags.
func (scanner *Scanner) Init(flags zgrab2.ScanFlags) error {
	f, _ := flags.(*Flags)
	scanner.config = f
	if f.Verbose {
		log.SetLevel(log.DebugLevel)
	}
	scanner.dialerGroupConfig = &zgrab2.DialerGroupConfig{
		TransportAgnosticDialerProtocol: zgrab2.TransportTCP,
		NeedSeparateL4Dialer:            true,
		BaseFlags:                       &f.BaseFlags,
		TLSEnabled:                      true,
		TLSFlags:                        &f.TLSFlags,
	}
	return nil
}

// InitPerSender does nothing in this module.
func (scanner *Scanner) InitPerSender(senderID int) error {
	return nil
}

// Protocol returns the protocol identifer for the scanner.
func (scanner *Scanner) Protocol() string {
	return "mssql"
}

func (scanner *Scanner) GetDialerGroupConfig() *zgrab2.DialerGroupConfig {
	return scanner.dialerGroupConfig
}

// GetName returns the configured scanner name.
func (scanner *Scanner) GetName() string {
	return scanner.config.Name
}

// GetTrigger returns the Trigger defined in the Flags.
func (scanner *Scanner) GetTrigger() string {
	return scanner.config.Trigger
}

// Scan performs the MSSQL scan.
// 1. Open a TCP connection to the target port (default 1433).
// 2. Send a PRELOGIN packet to the server.
// 3. Read the PRELOGIN response from the server.
// 4. If the server encrypt mode is EncryptModeNotSupported, break.
// 5. Perform a TLS handshake, with the packets wrapped in TDS headers.
// 6. Decode the Version and InstanceName from the PRELOGIN response
func (scanner *Scanner) Scan(ctx context.Context, dialGroup *zgrab2.DialerGroup, target *zgrab2.ScanTarget) (zgrab2.ScanStatus, any, error) {
	l4Dialer := dialGroup.L4Dialer
	if l4Dialer == nil {
		return zgrab2.SCAN_INVALID_INPUTS, nil, errors.New("l4 dialer is required for mssql")
	}
	conn, err := l4Dialer(target)(ctx, "tcp", net.JoinHostPort(target.Host(), strconv.Itoa(int(target.Port))))
	if err != nil {
		return zgrab2.TryGetScanStatus(err), nil, fmt.Errorf("error dialing target %s: %w", target.String(), err)
	}
	sql := NewConnection(conn)
	defer func(sql *Connection) {
		err = sql.Close()
		if err != nil {
			log.Errorf("error closing connection to target %s: %v", target.String(), err)
		}
	}(sql)
	result := &ScanResults{}

	encryptMode, handshakeErr := sql.Handshake(ctx, target, scanner.config.EncryptMode, dialGroup.TLSWrapper)

	result.EncryptMode = &encryptMode

	if sql.tlsConn != nil {
		result.TLSLog = sql.tlsConn.GetLog()
	}

	if sql.PreloginOptions != nil {
		result.PreloginOptions = sql.PreloginOptions
		version := sql.PreloginOptions.GetVersion()
		if version != nil {
			result.Version = version.String()
		}
		name, ok := (*sql.PreloginOptions)[PreloginInstance]
		if ok {
			temp := strings.Trim(string(name), "\x00\r\n")
			result.InstanceName = &temp
		} else {
			result.InstanceName = nil
		}
	}

	if handshakeErr != nil {
		if sql.PreloginOptions == nil && !sql.readValidTDSPacket {
			// If we received no PreloginOptions and none of the packets we've
			// read appeared to be a valid TDS header, then the inference is
			// that we found no MSSQL service on the target.
			// NOTE: In the case where PreloginOptions == nil but
			// readValidTDSPacket == true, the result will be empty, but not
			// nil.
			result = nil
		}
		switch {
		case errors.Is(handshakeErr, ErrNoServerEncryption):
			return zgrab2.SCAN_APPLICATION_ERROR, result, handshakeErr
		case errors.Is(handshakeErr, ErrServerRequiresEncryption):
			return zgrab2.SCAN_APPLICATION_ERROR, result, handshakeErr
		default:
			return zgrab2.TryGetScanStatus(handshakeErr), result, handshakeErr
		}
	}
	return zgrab2.SCAN_SUCCESS, result, nil
}

// RegisterModule is called by modules/mssql.go's init()
func RegisterModule() {
	var module Module
	_, err := zgrab2.AddCommand("mssql", "Microsoft SQL Server (MSSQL)", module.Description(), 1433, &module)
	if err != nil {
		log.Fatal(err)
	}
}
