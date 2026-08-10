package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// NewListenerFactory contributes the production current-user local IPC
// listener through ordinary generated constructor injection.
//
// @Bean(name="daemonListenerFactory")
func NewListenerFactory() ListenerFactory { return localListenerFactory{} }
