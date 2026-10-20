package core

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/miekg/dns"
)

var errInvalidReply = errors.New("invalid authenticated DNS reply")

type dnsFailure struct{ code int }

func (e dnsFailure) Error() string { return "DNS " + dns.RcodeToString[e.code] }
func failureKind(err error) string {
	var de dnsFailure
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.As(err, &de):
		return "dns"
	case errors.Is(err, errInvalidReply):
		return "invalid"
	default:
		return "socket"
	}
}
func (c *Client) recordFailure(err error) {
	switch failureKind(err) {
	case "timeout":
		c.timeouts.Add(1)
	case "dns":
		c.dnsErrors.Add(1)
	case "invalid":
		c.invalidReplies.Add(1)
	default:
		c.socketErrors.Add(1)
	}
}
func failureSummary(err error) string {
	if err == nil {
		return ""
	}
	var de dnsFailure
	if errors.As(err, &de) {
		return de.Error()
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return "DNS exchange timed out"
	}
	if errors.Is(err, errInvalidReply) {
		return "Invalid authenticated reply"
	}
	return fmt.Sprint(err)
}
