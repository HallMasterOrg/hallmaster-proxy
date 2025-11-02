package utils

import (
	"net"
	"strings"
)

func GetServerName(hostname string) string {
	servername := hostname
	if strings.Contains(servername, ":") {
		tmpServername, _, err := net.SplitHostPort(servername)
		if err == nil {
			servername = tmpServername
		}
	}

	return servername
}
