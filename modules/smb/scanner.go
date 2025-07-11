// Package smb provides a zgrab2 module that scans for smb.
// This was ported directly from zgrab.
package smb

import (
	"context"
	"fmt"
	"net"

	log "github.com/sirupsen/logrus"

	"github.com/zmap/zgrab2"
	"github.com/zmap/zgrab2/lib/smb/smb"
)

// Flags holds the command-line configuration for the smb scan module.
// Populated by the framework.
type Flags struct {
	zgrab2.BaseFlags `group:"Basic Options"`
	// SetupSession tells the client to continue the handshake up to the point where credentials would be needed.
	SetupSession bool `long:"setup-session" description:"After getting the response from the negotiation request, send a setup session packet."`

	// Verbose requests more verbose logging / output.
	Verbose bool `long:"verbose" description:"More verbose logging, include debug fields in the scan results"`
}

// Module implements the zgrab2.Module interface.
type Module struct {
}

// Scanner implements the zgrab2.Scanner interface.
type Scanner struct {
	config            *Flags
	dialerGroupConfig *zgrab2.DialerGroupConfig
}

// RegisterModule registers the zgrab2 module.
func RegisterModule() {
	var module Module
	_, err := zgrab2.AddCommand("smb", "Server Message Block (SMB)", module.Description(), 445, &module)
	if err != nil {
		log.Fatal(err)
	}
}

// NewFlags returns a default Flags object.
func (module *Module) NewFlags() any {
	return new(Flags)
}

// NewScanner returns a new Scanner instance.
func (module *Module) NewScanner() zgrab2.Scanner {
	return new(Scanner)
}

// Description returns an overview of this module.
func (module *Module) Description() string {
	return "Probe for SMB servers (Windows filesharing / SAMBA)"
}

// Validate checks that the flags are valid.
// On success, returns nil.
// On failure, returns an error instance describing the error.
func (flags *Flags) Validate(_ []string) error {
	return nil
}

// Help returns the module's help string.
func (flags *Flags) Help() string {
	return ""
}

// Init initializes the Scanner.
func (scanner *Scanner) Init(flags zgrab2.ScanFlags) error {
	f, _ := flags.(*Flags)
	scanner.config = f
	scanner.dialerGroupConfig = &zgrab2.DialerGroupConfig{
		TransportAgnosticDialerProtocol: zgrab2.TransportTCP,
		BaseFlags:                       &f.BaseFlags,
	}
	return nil
}

// InitPerSender initializes the scanner for a given sender.
func (scanner *Scanner) InitPerSender(senderID int) error {
	return nil
}

// GetName returns the Scanner name defined in the Flags.
func (scanner *Scanner) GetName() string {
	return scanner.config.Name
}

// GetTrigger returns the Trigger defined in the Flags.
func (scanner *Scanner) GetTrigger() string {
	return scanner.config.Trigger
}

// Protocol returns the protocol identifier of the scan.
func (scanner *Scanner) Protocol() string {
	return "smb"
}

func (scanner *Scanner) GetDialerGroupConfig() *zgrab2.DialerGroupConfig {
	return scanner.dialerGroupConfig
}

// Scan performs the following:
//  1. Connect to the TCP port (default 445).
//  2. Send a negotiation packet with the default values:
//     Dialects = { DialectSmb_2_1 },
//     SecurityMode = SecurityModeSigningEnabled
//  3. Read response from server; on failure, exit with log = nil.
//     If the server returns a protocol ID indicating support for version 1, set smbv1_support = true
//     Pull out the relevant information from the response packet
//  4. If --setup-session is not set, exit with success.
//  5. Send a setup session packet to the server with appropriate values
//  6. Read the response from the server; on failure, exit with the log so far.
//  7. Return the log.
func (scanner *Scanner) Scan(ctx context.Context, dialGroup *zgrab2.DialerGroup, target *zgrab2.ScanTarget) (zgrab2.ScanStatus, any, error) {
	conn, err := dialGroup.Dial(ctx, target)
	if err != nil {
		return zgrab2.TryGetScanStatus(err), nil, fmt.Errorf("could not establish connection to SMB server %s: %w", target.String(), err)
	}
	defer zgrab2.CloseConnAndHandleError(conn)
	var result *smb.SMBLog
	setupSession := scanner.config.SetupSession
	verbose := scanner.config.Verbose
	result, err = smb.GetSMBLog(conn, setupSession, false, verbose)
	if err != nil {
		if result == nil {
			zgrab2.CloseConnAndHandleError(conn)
			var newConn net.Conn
			newConn, err = dialGroup.Dial(ctx, target)
			if err != nil {
				return zgrab2.TryGetScanStatus(err), nil, fmt.Errorf("could not establish connection to SMB server %s on 2nd attempt: %w", target.String(), err)
			}
			defer zgrab2.CloseConnAndHandleError(newConn)
			result, err = smb.GetSMBLog(newConn, setupSession, true, verbose)
			if err != nil {
				return zgrab2.TryGetScanStatus(err), result, err
			}
		} else {
			return zgrab2.TryGetScanStatus(err), result, err
		}
	}
	return zgrab2.SCAN_SUCCESS, result, nil
}
