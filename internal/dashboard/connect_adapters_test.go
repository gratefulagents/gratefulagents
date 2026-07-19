package dashboard

import "github.com/gratefulagents/gratefulagents/rpc/platform/platformconnect"

var _ platformconnect.PlatformServiceHandler = (*PlatformServiceConnectHandler)(nil)
